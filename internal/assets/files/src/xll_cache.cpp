#include "xll_cache.h"
#include "xll_log.h"
#include "xll_excel.h"
#include "types/utility.h"
#include "types/xlcall.h"
#include <sstream>
#include <random>
#include <iomanip>

// Guard the assumption used by the in-place XLMREF12 buffer below.
// XLMREF12 is { WORD count; XLREF12 reftbl[1]; } so sizeof(XLMREF12) is at
// least sizeof(WORD) + sizeof(XLREF12); compilers will typically pad
// `count` up to the alignment of XLREF12 (4 bytes), giving sizeof()==20
// rather than the naive 18. If this assertion ever fires the mrefBuf
// allocation in GetOrComputeRefHash must be revisited.
static_assert(sizeof(XLMREF12) >= sizeof(WORD) + sizeof(XLREF12),
              "XLMREF12 size assumption invalidated; recheck mrefBuf allocation");

namespace xll {

namespace {

// ---------------------------------------------------------------------------
// FNV-1a streaming primitives
// ---------------------------------------------------------------------------
// Everything content-addressed in this file (cache keys, RTD topic tokens, the
// per-cycle ref hashes) is one continuous FNV-1a stream. NEVER xor-combine
// independent digests instead: xor is order-insensitive and cancels the basis,
// strictly weakening collision resistance (reviewer MED, 2026-06-12).
constexpr uint64_t kFnvBasis = 14695981039346656037ULL;
constexpr uint64_t kFnvPrime = 1099511628211ULL;

inline uint64_t Fnv1aUpdate(uint64_t hash, const void* data, size_t len) {
    const unsigned char* p = static_cast<const unsigned char*>(data);
    for (size_t i = 0; i < len; ++i) {
        hash ^= p[i];
        hash *= kFnvPrime;
    }
    return hash;
}

inline uint64_t Fnv1aByte(uint64_t hash, unsigned char b) {
    hash ^= b;
    hash *= kFnvPrime;
    return hash;
}

inline uint64_t Fnv1aU32(uint64_t hash, uint32_t v) {
    return Fnv1aUpdate(hash, &v, sizeof(v));
}

inline uint64_t Fnv1aU64(uint64_t hash, uint64_t v) {
    return Fnv1aUpdate(hash, &v, sizeof(v));
}

// One-byte XLOPER12 type tags. They are the ONLY thing separating one kind of
// value from another inside the digest, so they must stay distinct and stable —
// changing a tag silently invalidates every in-memory cache key and every RTD
// topic token (cold-cache only; nothing is persisted to disk).
enum : unsigned char {
    kTagNull    = 'z',  // null XLOPER12*
    kTagNum     = 'N',  // xltypeNum      (raw IEEE-754 bytes)
    kTagStr     = 'S',  // xltypeStr      (uint32 length + raw UTF-16 units)
    kTagBool    = 'B',  // xltypeBool
    kTagErr     = 'E',  // xltypeErr
    kTagInt     = 'I',  // xltypeInt
    kTagNil     = '_',  // xltypeMissing / xltypeNil (both mean "no value")
    kTagRefVal  = 'R',  // xltypeRef/SRef successfully coerced to cell VALUES
    kTagMulti   = 'A',  // an inline xltypeMulti value array
    kTagRefErr  = 'X',  // coercion failed -> reference IDENTITY folded in
    kTagSRefErr = 'x',  // ...for the xltypeSRef shape
    kTagUnknown = '?',  // anything else (xltype folded in)
    kTagDepth   = '!',  // recursion cap hit
};

// Excel never nests xltypeMulti inside xltypeMulti, but the XLOPER12 is RAW
// EXTERNAL INPUT (AGENTS.md §22 boundary rule) and the coerce path recurses too,
// so cap the depth rather than trusting the shape. Hitting the cap folds in a
// distinct marker, which keeps the digest defined (never unbounded stack use).
constexpr int kMaxHashDepth = 16;

uint64_t HashXLOPERIntoDepth(uint64_t h, const XLOPER12* px, int depth);

// Folds the coordinates that IDENTIFY a reference (used when xlCoerce fails, so
// two DISTINCT failing refs still hash apart instead of collapsing onto one RTD
// topic + one RefCache entry — reviewer HIGH, 2026-06-12).
uint64_t HashRefIdentity(uint64_t h, const XLOPER12* px, DWORD ty) {
    h = Fnv1aByte(h, kTagRefErr);
    if (ty == xltypeRef && px->val.mref.lpmref) {
        h = Fnv1aU64(h, (uint64_t)px->val.mref.idSheet);
        const XLMREF12* m = px->val.mref.lpmref;
        h = Fnv1aU32(h, (uint32_t)m->count);
        for (WORD ri = 0; ri < m->count; ++ri) {
            const XLREF12& r = m->reftbl[ri];
            const int32_t rect[4] = { r.rwFirst, r.rwLast, r.colFirst, r.colLast };
            h = Fnv1aUpdate(h, rect, sizeof(rect));
        }
    } else if (ty == xltypeSRef) {
        h = Fnv1aByte(h, kTagSRefErr);
        const XLREF12& r = px->val.sref.ref;
        const int32_t rect[4] = { r.rwFirst, r.rwLast, r.colFirst, r.colLast };
        h = Fnv1aUpdate(h, rect, sizeof(rect));
    }
    return h;
}

uint64_t HashXLOPERIntoDepth(uint64_t h, const XLOPER12* px, int depth) {
    if (!px) return Fnv1aByte(h, kTagNull);
    if (depth > kMaxHashDepth) return Fnv1aByte(h, kTagDepth);

    const DWORD ty = px->xltype & ~(xlbitXLFree | xlbitDLLFree);
    switch (ty) {
        case xltypeNum: {
            // Raw IEEE-754 bytes, not a formatted decimal. The old textual form
            // needed std::setprecision(17) to avoid collapsing distinct doubles
            // (notably `date` serials, which ride this branch) onto one key; raw
            // bytes are exact by construction, allocation-free, and free of the
            // iostream locale path (hundreds of ns per cell).
            const double d = px->val.num;
            h = Fnv1aByte(h, kTagNum);
            h = Fnv1aUpdate(h, &d, sizeof(d));
            break;
        }
        case xltypeStr: {
            // An Excel XLOPER12 string is Pascal-shaped: val.str[0] is the length
            // in UTF-16 code units and the body follows. Feed the length plus the
            // RAW UTF-16 code units straight into the digest.
            //
            // This is the fix for the stack out-of-bounds READ that used to live
            // here: the old code did
            //     std::wstring ws = PascalToWString(px->val.str);   // prefix STRIPPED
            //     ss << ws.length() << ConvertExcelString(ws.c_str());
            // but ConvertExcelString is Pascal-ONLY — it reads wstr[0] AS the
            // length. Handed an already-stripped plain wstring it took the first
            // CHARACTER as the length ('H' = 72; a CJK lead such as U+D55C =
            // 54620) and transcoded that many code units out of the SSO/heap
            // buffer, i.e. up to ~109 KB past the end (MAX_STRING_SIZE = 10 MB
            // never trips). The resulting token depended on adjacent stack
            // garbage, so any string-bearing cache key / RTD topic token was
            // non-deterministic across runs.
            //
            // Hashing the raw code units also removes the UTF-8 transcode
            // entirely, which makes embedded NULs and lone surrogates byte-exact.
            const XCHAR* ps = px->val.str;
            const uint32_t len = ps ? (uint32_t)(uint16_t)ps[0] : 0u;
            h = Fnv1aByte(h, kTagStr);
            h = Fnv1aU32(h, len);
            if (ps && len) h = Fnv1aUpdate(h, ps + 1, (size_t)len * sizeof(XCHAR));
            break;
        }
        case xltypeBool:
            h = Fnv1aByte(h, kTagBool);
            h = Fnv1aByte(h, px->val.xbool ? 1u : 0u);
            break;
        case xltypeErr:
            h = Fnv1aByte(h, kTagErr);
            h = Fnv1aU32(h, (uint32_t)px->val.err);
            break;
        case xltypeInt:
            h = Fnv1aByte(h, kTagInt);
            h = Fnv1aU32(h, (uint32_t)px->val.w);
            break;
        case xltypeMissing:
        case xltypeNil:
            h = Fnv1aByte(h, kTagNil);
            break;
        case xltypeRef:
        case xltypeSRef: {
            XLOPER12 xVal;
            XLOPER12 xType; xType.xltype = xltypeInt; xType.val.w = xltypeMulti;
            if (xll::CallExcel(xlCoerce, &xVal, px, &xType) == xlretSuccess) {
                // kTagRefVal wraps the coerced value so a REFERENCE to A1:B2 and
                // an inline array literal with the same cells stay distinguishable
                // (they are distinct wire payloads for `range` vs `grid`).
                h = Fnv1aByte(h, kTagRefVal);
                h = HashXLOPERIntoDepth(h, &xVal, depth + 1);
                xll::CallExcel(xlFree, nullptr, &xVal);
            } else {
                // Coerce failed (xlretUncalced mid-calc, cross-sheet restriction...).
                h = HashRefIdentity(h, px, ty);
            }
            break;
        }
        case xltypeMulti: {
            h = Fnv1aByte(h, kTagMulti);
            h = Fnv1aU32(h, (uint32_t)px->val.array.rows);
            h = Fnv1aU32(h, (uint32_t)px->val.array.columns);
            const size_t count = (size_t)px->val.array.rows * (size_t)px->val.array.columns;
            const XLOPER12* cells = px->val.array.lparray;
            if (!cells) {
                h = Fnv1aByte(h, kTagNull);
                break;
            }
            for (size_t i = 0; i < count; ++i) {
                h = HashXLOPERIntoDepth(h, &cells[i], depth + 1);
            }
            break;
        }
        default:
            h = Fnv1aByte(h, kTagUnknown);
            h = Fnv1aU32(h, (uint32_t)px->xltype);
            break;
    }
    return h;
}

// Appends exactly 16 lowercase hex digits. Hand-rolled instead of
// `std::hex << std::setw(16) << std::setfill('0')` so token formatting costs no
// stringstream construction and no locale lookup.
inline void AppendHex16(std::string& out, uint64_t h) {
    static const char kHex[] = "0123456789abcdef";
    for (int shift = 60; shift >= 0; shift -= 4) {
        out.push_back(kHex[(h >> shift) & 0xFu]);
    }
}

} // namespace

uint64_t HashXLOPERInto(uint64_t seed, const XLOPER12* px) {
    return HashXLOPERIntoDepth(seed, px, 0);
}

uint64_t HashXLOPERContent(const XLOPER12* px) {
    return HashXLOPERIntoDepth(kFnvBasis, px, 0);
}

CacheManager& CacheManager::Instance() {
    static CacheManager instance;
    return instance;
}

bool CacheManager::Get(const std::string& key, std::vector<uint8_t>& result) {
    bool hit = false;
    cache_.if_contains(key, [&](const std::pair<std::string, CacheEntry>& val) {
        if (std::chrono::steady_clock::now() < val.second.expiry) {
            result = val.second.data;
            hit = true;
        }
    });

    // Lazy cleanup? phmap doesn't support automatic expiration.
    // We treat expired items as miss. They will be overwritten on next Put.
    return hit;
}

void CacheManager::Put(const std::string& key, const std::vector<uint8_t>& data, const CacheConfig& config) {
    if (!config.enabled) return;

    auto now = std::chrono::steady_clock::now();
    auto expiry = now + config.ttl;

    if (config.jitter.count() > 0) {
        // Add random jitter: [-Jitter, +Jitter]
        static thread_local std::mt19937 generator(std::random_device{}());
        std::uniform_int_distribution<long long> distribution(-config.jitter.count(), config.jitter.count());
        expiry += std::chrono::milliseconds(distribution(generator));
    }

    CacheEntry entry;
    entry.data = data;
    entry.expiry = expiry;

    cache_.insert_or_assign(key, std::move(entry));
}

void CacheManager::ClearRefCache() {
    refCache_.clear();
}

uint64_t CacheManager::GetOrComputeRefHash(const XLOPER12* pRef, const std::function<uint64_t(const XLOPER12*)>& computeFn) {
    if (!pRef) return Fnv1aByte(kFnvBasis, kTagNull);

    // The xltype narrowing here is LOAD-BEARING, not a duplicate of the caller's
    // (AGENTS.md §22 "Confirmed-Correct Decisions"): xltypeRef (0x0008) and
    // xltypeSRef (0x0400) are distinct bits, so an SRef-only input passes the
    // caller's `xltypeRef | xltypeSRef` test and is selected here. Only an
    // xltypeRef carries the (idSheet, rect-table) pair the per-cycle RefCache is
    // keyed on, so anything else is hashed DIRECTLY (no memoization) — the
    // behavior-selecting branch AGENTS.md describes.
    //
    // The direct path used to `return ""`, which made MakeCacheKey contribute
    // NOTHING for an xltypeSRef argument: every single-area reference argument
    // collapsed onto one cache key, so a cache-enabled function called on A1:B2
    // could serve the result computed for C5:D9. The unreachable
    // `else if (xltype == xltypeSRef) ss << computeFn(pRef)` branch that used to
    // sit below shows the original intent was to compute; that is restored here.
    const DWORD refTy = pRef->xltype & ~(xlbitXLFree | xlbitDLLFree);
    if (refTy != xltypeRef) return computeFn(pRef);
    if (!pRef->val.mref.lpmref) return computeFn(pRef);

    uint64_t acc = kFnvBasis;
    IDSHEET sheetId = pRef->val.mref.idSheet;
    // Correct access: lpmref points to XLMREF12 which contains 'count' and 'reftbl'
    DWORD count = pRef->val.mref.lpmref->count;
    // Fold the sheet + rect count so a 1-rect ref cannot alias the single rect's
    // own digest, and a 2-rect ref cannot alias a differently split pair covering
    // the same cells.
    acc = Fnv1aU64(acc, (uint64_t)sheetId);
    acc = Fnv1aU32(acc, (uint32_t)count);
    for (DWORD i = 0; i < count; ++i) {
        const XLREF12& rect = pRef->val.mref.lpmref->reftbl[i];
        RefKey key = {sheetId, rect.rwFirst, rect.rwLast, rect.colFirst, rect.colLast};

        // Try to find in cache. NOTE: computeFn must NOT run inside if_contains —
        // the submap mutex is held for the duration of the callback and computeFn
        // calls back into Excel (xlCoerce).
        bool found = false;
        uint64_t hashVal = 0;
        refCache_.if_contains(key, [&](const std::pair<RefKey, uint64_t>& val) {
            hashVal = val.second;
            found = true;
        });

        if (!found) {
            // Construct a temporary XLOPER just for this rect to pass to computeFn
            XLOPER12 xRef;
            xRef.xltype = xltypeRef;

            // We need to construct a valid mref for the single rect
            // We cannot modify the existing lpmref structure in place.
            // But we can create a temporary buffer for XLMREF12 with 1 rect.
            // Or if computeFn only needs *value*, we can just pass the rect pointer?
            // computeFn takes XLOPER12*.

            // Allocate temp XLMREF12 on stack (safe because we control size)
            // XLMREF12 has flexible array member.
            // struct { WORD count; XLREF12 reftbl[1]; } tempMRef;
            // But we must match layout.

            // Use sizeof(XLMREF12) (not WORD + XLREF12) and align the
            // buffer to XLMREF12's alignment. The previous code allocated
            // only sizeof(WORD)+sizeof(XLREF12) bytes which, due to
            // padding of `count` to XLREF12's 4-byte alignment, overran
            // the stack buffer by 2 bytes on common ABIs. See the
            // file-scope static_assert above for the size invariant.
            alignas(XLMREF12) char mrefBuf[sizeof(XLMREF12)];
            XLMREF12* tempMRef = reinterpret_cast<XLMREF12*>(mrefBuf);
            tempMRef->count = 1;
            tempMRef->reftbl[0] = rect;

            xRef.val.mref.lpmref = tempMRef;
            xRef.val.mref.idSheet = sheetId;

            hashVal = computeFn(&xRef);

            // Store in cache
            refCache_.insert_or_assign(key, hashVal);
        }
        acc = Fnv1aU64(acc, hashVal);
    }

    return acc;
}

// DIAGNOSTICS ONLY. Cache keys and RTD topic tokens are produced by
// HashXLOPERInto above, which streams the same content through FNV-1a without
// allocating a stringstream per cell. This renderer stays because a readable dump
// of an XLOPER12 is invaluable when debugging a cache miss, but nothing on the
// hot path may call it: a 100x100 xltypeMulti costs 10k stringstream
// constructions and 10k std::string allocations here.
std::string SerializeXLOPER(const XLOPER12* px) {
    if (!px) return "null";
    std::stringstream ss;

    switch (px->xltype & ~(xlbitXLFree | xlbitDLLFree)) {
        case xltypeNum:
            // Round-trip precision (max_digits10 = 17): the default stream
            // precision (~6 sig-figs) collapses distinct doubles that agree to
            // 6 figures onto one rendering, which would make two distinct `date`
            // serials on the same day look identical in a dump. (The hash path
            // sidesteps the question entirely by feeding the raw IEEE-754 bytes.)
            ss << "Num:" << std::setprecision(17) << px->val.num;
            break;
        case xltypeStr:
            {
                 // PascalToWString ALREADY strips the Pascal length prefix, so the
                 // result must NOT be fed to ConvertExcelString (Pascal-only: it
                 // reads wstr[0] AS the length). Doing so read the first character
                 // as a count — 'H' = 72, a CJK lead such as U+D55C = 54620 — and
                 // transcoded that many code units past the end of the wstring
                 // buffer (~144 B for ASCII, ~109 KB for CJK; the 10 MB
                 // MAX_STRING_SIZE guard never trips), so the output depended on
                 // adjacent stack garbage. WideToUtf8 is the plain-wstring
                 // counterpart and also preserves embedded NULs (it converts
                 // wstr.size() units, not up to a terminator).
                 std::wstring ws = PascalToWString(px->val.str);
                 ss << "Str:" << ws.length() << ":" << WideToUtf8(ws);
            }
            break;
        case xltypeBool:
            ss << "Bool:" << (px->val.xbool ? "1" : "0");
            break;
        case xltypeErr:
            ss << "Err:" << px->val.err;
            break;
        case xltypeInt:
            ss << "Int:" << px->val.w;
            break;
        case xltypeMissing:
        case xltypeNil:
            ss << "Nil";
            break;
        case xltypeRef:
        case xltypeSRef:
             {
                 XLOPER12 xVal;
                 XLOPER12 xType; xType.xltype = xltypeInt; xType.val.w = xltypeMulti;
                 if (xll::CallExcel(xlCoerce, &xVal, px, &xType) == xlretSuccess) {
                     if (xVal.xltype == xltypeMulti) {
                         ss << "Grid:" << xVal.val.array.rows << "x" << xVal.val.array.columns << "{";
                         DWORD count = xVal.val.array.rows * xVal.val.array.columns;
                         for (DWORD i=0; i < count; ++i) {
                             ss << SerializeXLOPER(&xVal.val.array.lparray[i]) << ",";
                         }
                         ss << "}";
                     } else {
                         ss << SerializeXLOPER(&xVal);
                     }
                     xll::CallExcel(xlFree, nullptr, &xVal);
                 } else {
                     // Coerce failed (xlretUncalced mid-calc, cross-sheet
                     // restriction, ...). Include the REFERENCE IDENTITY so two
                     // DISTINCT failing refs hash apart — a constant marker
                     // collapsed them onto one RTD topic + one RefCache entry
                     // (reviewer HIGH, 2026-06-12).
                     ss << "RefError:";
                     DWORD ty = px->xltype & ~(xlbitXLFree | xlbitDLLFree);
                     if (ty == xltypeRef && px->val.mref.lpmref) {
                         ss << (unsigned long long)px->val.mref.idSheet;
                         const XLMREF12* m = px->val.mref.lpmref;
                         for (WORD ri = 0; ri < m->count; ++ri) {
                             const XLREF12& r = m->reftbl[ri];
                             ss << ":" << r.rwFirst << "," << r.colFirst
                                << "-" << r.rwLast << "," << r.colLast;
                         }
                     } else if (ty == xltypeSRef) {
                         const XLREF12& r = px->val.sref.ref;
                         ss << "S:" << r.rwFirst << "," << r.colFirst
                            << "-" << r.rwLast << "," << r.colLast;
                     }
                 }
             }
             break;
        case xltypeMulti:
             {
                 ss << "Multi:" << px->val.array.rows << "x" << px->val.array.columns << "{";
                 DWORD count = px->val.array.rows * px->val.array.columns;
                 for (DWORD i=0; i < count; ++i) {
                     ss << SerializeXLOPER(&px->val.array.lparray[i]) << ",";
                 }
                 ss << "}";
             }
             break;
        default:
            ss << "Unknown:" << px->xltype;
    }
    return ss.str();
}


// Formats a 64-bit FNV-1a hash as the content-addressed RTD topic token
// "h:<typeTag><hex>". The "h:" prefix is collision-proof against any token a
// plain (scalar) string argument could legitimately produce, so the Go side
// could branch on it if needed — but in practice it decodes composite
// positions by the generator-known arg type, not by sniffing the prefix. The
// typeTag namespaces the hash by wire-payload shape (see header).
// Output is byte-identical to the previous stringstream formulation
// ("h:" + tag + 16 lowercase zero-padded hex digits) — only the cost changed.
static std::string FormatHashToken(char typeTag, uint64_t h) {
    std::string s;
    s.reserve(19); // "h:" + tag + 16 hex digits
    s += "h:";
    s.push_back(typeTag);
    AppendHex16(s, h);
    return s;
}

std::string ContentHashToken(char typeTag, const XLOPER12* px) {
    // HashXLOPERInto coerces xltypeRef/xltypeSRef to the underlying cell values
    // (xlCoerce → xltypeMulti) before hashing, so the token tracks CONTENT, not
    // reference coordinates: editing a cell inside a range arg changes the digest
    // → changes the token → a fresh RTD topic. No intermediate string is
    // materialized (a 100x100 grid used to cost 10k stringstreams + 10k strings).
    return FormatHashToken(typeTag, HashXLOPERContent(px));
}

std::string ContentHashTokenFP12(const FP12* fp) {
    if (!fp) return FormatHashToken('n', Fnv1aUpdate(kFnvBasis, "FP12:null", 9));
    // Hash geometry then payload through ONE continuous FNV-1a stream so the
    // result has full avalanche and is order-sensitive (a 1x4 and a 2x2 with
    // identical bytes differ). Raw double bytes keep NaN/-0.0 bit-stable
    // across recalcs of identical content.
    uint64_t h = kFnvBasis;
    int32_t dims[2] = { fp->rows, fp->columns };
    h = Fnv1aUpdate(h, dims, sizeof(dims));
    const size_t count = static_cast<size_t>(fp->rows) * static_cast<size_t>(fp->columns);
    h = Fnv1aUpdate(h, fp->array, count * sizeof(double));
    return FormatHashToken('n', h);
}

flatbuffers::Offset<protocol::Grid> ConvertGridArg(const XLOPER12* op, flatbuffers::FlatBufferBuilder& builder, bool* coerceOk) {
    if (coerceOk) *coerceOk = true;
    if (!op) return ConvertGrid(const_cast<LPXLOPER12>(op), builder);

    // A grid arg passed as a range reference must be coerced to its cell
    // VALUES before ConvertGrid (which only understands xltypeMulti). Mirrors
    // HashXLOPERInto's ref handling above.
    if (op->xltype & (xltypeRef | xltypeSRef)) {
        XLOPER12 xVal;
        XLOPER12 xType; xType.xltype = xltypeInt; xType.val.w = xltypeMulti;
        if (xll::CallExcel(xlCoerce, &xVal, op, &xType) == xlretSuccess) {
            auto off = ConvertGrid(&xVal, builder);
            xll::CallExcel(xlFree, nullptr, &xVal);
            return off;
        }
        // Coerce failed (xlretUncalced etc.): signal the caller so the wrapper
        // SKIPS shipping a payload entirely — the Go dispatch then misses the
        // token and pushes an explicit error to the topic, instead of the
        // handler silently receiving a degenerate 1x1 grid (reviewer MED,
        // 2026-06-12). The fall-through below still returns a valid offset so
        // legacy callers without the out-param keep compiling/working.
        if (coerceOk) *coerceOk = false;
    }
    return ConvertGrid(const_cast<LPXLOPER12>(op), builder);
}

std::string MakeCacheKey(const std::string& funcName, const std::vector<LPXLOPER12>& args) {
    // The key used to embed the FULL serialization of every argument, so its size
    // grew with the argument CONTENT: a 100x100 grid arg produced a multi-hundred-KB
    // key that phmap then had to hash and memcmp on EVERY Get and Put, on top of
    // the 10k stringstreams SerializeXLOPER needed to build it. The key is now a
    // constant-size digest, so Get/Put are O(1) in the argument size.
    uint64_t h = kFnvBasis;

    // Fold the name's LENGTH before its bytes so ("AB", args) cannot collide with
    // ("A", <a "B"-shaped arg>), and the arg COUNT so a trailing omitted argument
    // cannot alias a shorter call.
    h = Fnv1aU32(h, (uint32_t)funcName.size());
    h = Fnv1aUpdate(h, funcName.data(), funcName.size());
    h = Fnv1aU32(h, (uint32_t)args.size());

    for (const auto& arg : args) {
        if (!arg) {
            h = Fnv1aByte(h, kTagNull);
            continue;
        }

        if (arg->xltype & (xltypeRef | xltypeSRef)) {
            // Route references through the per-cycle RefCache so a range that
            // several cached calls share is coerced (and hashed) once per
            // calculation cycle instead of once per call.
            const uint64_t refHash = CacheManager::Instance().GetOrComputeRefHash(
                arg, [](const XLOPER12* pRef) { return HashXLOPERContent(pRef); });
            h = Fnv1aByte(h, kTagRefVal);
            h = Fnv1aU64(h, refHash);
        } else {
            h = Fnv1aByte(h, 'v');
            h = HashXLOPERInto(h, arg);
        }
    }

    // Keep the function name as a literal prefix: it makes keys greppable in a
    // log AND it means two DIFFERENT functions can never share a key even under a
    // 64-bit digest collision. Its length is a per-callsite constant, so the key
    // is still O(1) in the argument content. Callers may append their own
    // suffixes (the generated wrapper appends "|numgrid:<token>").
    std::string key;
    key.reserve(funcName.size() + 17);
    key += funcName;
    key.push_back('#');
    AppendHex16(key, h);
    return key;
}

} // namespace xll
