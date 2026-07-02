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

std::string CacheManager::GetOrComputeRefHash(const XLOPER12* pRef, std::function<std::string(const XLOPER12*)> computeFn) {
    if (!pRef || (pRef->xltype & xltypeRef) == 0) return "";

    std::stringstream ss;

    if (pRef->xltype == xltypeRef) {
        IDSHEET sheetId = pRef->val.mref.idSheet;
        // Correct access: lpmref points to XLMREF12 which contains 'count' and 'reftbl'
        DWORD count = pRef->val.mref.lpmref->count;
        for (DWORD i = 0; i < count; ++i) {
            const XLREF12& rect = pRef->val.mref.lpmref->reftbl[i];
            RefKey key = {sheetId, rect.rwFirst, rect.rwLast, rect.colFirst, rect.colLast};

            // Try to find in cache
            bool found = false;
            std::string hashVal;
            refCache_.if_contains(key, [&](const std::pair<RefKey, std::string>& val) {
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
            ss << hashVal << ";";
        }
    } else if (pRef->xltype == xltypeSRef) {
         ss << computeFn(pRef);
    } else {
        // Not a ref
        ss << computeFn(pRef);
    }

    return ss.str();
}

// Simple FNV-1a hash for string
static uint64_t Fnv1a(const std::string& s) {
    uint64_t hash = 14695981039346656037ULL;
    for (char c : s) {
        hash ^= (unsigned char)c;
        hash *= 1099511628211ULL;
    }
    return hash;
}

// Simple FNV-1a hash for bytes
static uint64_t Fnv1a(const void* data, size_t len) {
    uint64_t hash = 14695981039346656037ULL;
    const unsigned char* p = (const unsigned char*)data;
    for (size_t i = 0; i < len; ++i) {
        hash ^= p[i];
        hash *= 1099511628211ULL;
    }
    return hash;
}

// Streaming FNV-1a continuation: feeds more bytes through an existing digest.
// Use this (NOT xor-combining independent digests) when hashing multiple
// fields into one token — xor-combining is order-insensitive and cancels the
// basis, strictly weakening collision resistance (reviewer MED, 2026-06-12).
static uint64_t Fnv1aUpdate(uint64_t hash, const void* data, size_t len) {
    const unsigned char* p = (const unsigned char*)data;
    for (size_t i = 0; i < len; ++i) {
        hash ^= p[i];
        hash *= 1099511628211ULL;
    }
    return hash;
}

std::string SerializeXLOPER(const XLOPER12* px) {
    if (!px) return "null";
    std::stringstream ss;

    switch (px->xltype & ~(xlbitXLFree | xlbitDLLFree)) {
        case xltypeNum:
            // Round-trip precision (max_digits10 = 17): the default stream
            // precision (~6 sig-figs) collapses distinct doubles that agree to
            // 6 figures onto one cache key -> a stale result for a different
            // input. This matters for `date` args (they ride this Num branch),
            // whose serials carry sub-day fractional time exceeding 6 figures,
            // so two distinct timestamps on the same day would otherwise collide.
            ss << "Num:" << std::setprecision(17) << px->val.num;
            break;
        case xltypeStr:
            {
                 std::wstring ws = PascalToWString(px->val.str);
                 ss << "Str:" << ws.length() << ":" << ConvertExcelString(ws.c_str());
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
static std::string FormatHashToken(char typeTag, uint64_t h) {
    std::stringstream ss;
    ss << "h:" << typeTag << std::hex << std::setw(16) << std::setfill('0') << h;
    return ss.str();
}

std::string ContentHashToken(char typeTag, const XLOPER12* px) {
    // SerializeXLOPER coerces xltypeRef/xltypeSRef to the underlying cell
    // values (xlCoerce → xltypeMulti) before serializing, so the hash tracks
    // CONTENT, not reference coordinates: editing a cell inside a range arg
    // changes the serialization → changes the token → a fresh RTD topic.
    std::string s = SerializeXLOPER(px);
    return FormatHashToken(typeTag, Fnv1a(s));
}

std::string ContentHashTokenFP12(const FP12* fp) {
    if (!fp) return FormatHashToken('n', Fnv1a("FP12:null", 9));
    // Hash geometry then payload through ONE continuous FNV-1a stream so the
    // result has full avalanche and is order-sensitive (a 1x4 and a 2x2 with
    // identical bytes differ). Raw double bytes keep NaN/-0.0 bit-stable
    // across recalcs of identical content.
    uint64_t h = 14695981039346656037ULL;
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
    // SerializeXLOPER's ref handling above.
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
    std::stringstream ss;
    ss << funcName << "(";
    for (const auto& arg : args) {
        if (!arg) {
             ss << "null,";
             continue;
        }

        if (arg->xltype & (xltypeRef | xltypeSRef)) {
            // Use RefCache
            std::string refHash = CacheManager::Instance().GetOrComputeRefHash(arg, [](const XLOPER12* pRef) {
                 std::string s = SerializeXLOPER(pRef);
                 uint64_t h = Fnv1a(s);
                 std::stringstream hss;
                 hss << "RefHash(" << std::hex << h << ")";
                 return hss.str();
            });
            ss << refHash << ",";
        } else {
            ss << SerializeXLOPER(arg) << ",";
        }
    }
    ss << ")";
    return ss.str();
}

} // namespace xll
