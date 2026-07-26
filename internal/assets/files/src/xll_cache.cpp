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
    kTagRefIdent= 'i',  // reference IDENTITY folded AHEAD of the value digest
    kTagMRef    = 'm',  // ...identity of the xltypeRef shape (idSheet + rects)
    kTagSRefErr = 'x',  // ...identity of the xltypeSRef shape (one rect, no sheet)
    kTagNoIdent = 'o',  // ...a reference carrying no usable coordinates
    kTagUnknown = '?',  // anything else (xltype folded in)
    kTagDepth   = '!',  // recursion cap hit
};

// Excel never nests xltypeMulti inside xltypeMulti, but the XLOPER12 is RAW
// EXTERNAL INPUT (AGENTS.md §22 boundary rule) and the coerce path recurses too,
// so cap the depth rather than trusting the shape. Hitting the cap folds in a
// distinct marker, which keeps the digest defined (never unbounded stack use).
constexpr int kMaxHashDepth = 16;

uint64_t HashXLOPERIntoDepth(uint64_t h, const XLOPER12* px, int depth);

// Streams the coordinates that IDENTIFY a reference — the sheet id plus the rect
// table — into an existing digest. Emits NO leading tag: each caller prefixes its
// own so the "coerce failed" stream and the "identity + values" stream (below)
// can never alias.
//
// The two reference SHAPES are deliberately distinguished, because they carry
// different information (the same distinction GetOrComputeRefHash makes):
//   * xltypeRef  — idSheet + an ARRAY of rects (multi-area). All of it is folded,
//                  in order, so a 2-area ref cannot alias a differently split
//                  pair covering the same cells.
//   * xltypeSRef — ONE rect and NO sheet id (Excel passes this shape for a
//                  same-sheet reference), so only the rect exists to fold.
// A ref with no usable coordinates (xltypeRef with a null lpmref) folds a
// distinct marker rather than nothing, so it cannot alias "identity absent".
uint64_t HashRefIdentityFields(uint64_t h, const XLOPER12* px, DWORD ty) {
    if (ty == xltypeRef && px->val.mref.lpmref) {
        h = Fnv1aByte(h, kTagMRef);
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
    } else {
        h = Fnv1aByte(h, kTagNoIdent);
    }
    return h;
}

// Folds the reference identity under the "coercion failed" tag, so two DISTINCT
// failing refs still hash apart instead of collapsing onto one RTD topic + one
// RefCache entry (reviewer HIGH, 2026-06-12).
uint64_t HashRefIdentity(uint64_t h, const XLOPER12* px, DWORD ty) {
    h = Fnv1aByte(h, kTagRefErr);
    return HashRefIdentityFields(h, px, ty);
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

uint64_t HashXLOPERWithRefIdentity(uint64_t seed, const XLOPER12* px) {
    if (!px) return Fnv1aByte(seed, kTagNull);
    const DWORD ty = px->xltype & ~(xlbitXLFree | xlbitDLLFree);
    uint64_t h = seed;
    if (ty == xltypeRef || ty == xltypeSRef) {
        // Identity FIRST, then the value stream, all in ONE continuous FNV-1a
        // digest (never xor-combined — see the note on the primitives above).
        h = Fnv1aByte(h, kTagRefIdent);
        h = HashRefIdentityFields(h, px, ty);
    }
    // Non-references stream byte-identically to HashXLOPERInto: an inline value
    // array has no identity to fold, so the two entry points agree there.
    return HashXLOPERIntoDepth(h, px, 0);
}

uint64_t HashXLOPERContentWithRefIdentity(const XLOPER12* px) {
    return HashXLOPERWithRefIdentity(kFnvBasis, px);
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

// Memory ordering for iterativeCalc_ / refPathUsed_ (both written on the STA at
// calc end / xlAutoOpen and read on Excel's calculation threads): stores are
// RELEASE, loads and the flag-consuming exchange are ACQUIRE. On x86 this
// compiles to the SAME instructions as relaxed (plain MOV; XCHG is already
// locked), so the pairing costs nothing and states the intent — the gate is
// read next to the refCache_ map operations it guards, and relaxed would have
// left "why is this safe?" resting on the target ISA.
void CacheManager::SetIterativeCalcMode(bool on) {
    iterativeCalc_.store(on, std::memory_order_release);
}

bool CacheManager::IterativeCalcMode() const {
    return iterativeCalc_.load(std::memory_order_acquire);
}

void CacheManager::RefreshIterativeCalcMode() {
    // Only pay for the Excel round-trip when a reference argument actually went
    // through the RefCache this cycle (see the header). Consumes the flag.
    if (!refPathUsed_.exchange(false, std::memory_order_acquire)) return;
    QueryIterativeCalcMode();
}

void CacheManager::ForceRefreshIterativeCalcMode() {
    // Unconditional twin of RefreshIterativeCalcMode: it does NOT consult (or
    // consume) refPathUsed_, because at its one call site — xlAutoOpen — no
    // calculation has happened yet, so the flag is necessarily false and the
    // gated version would return without ever asking Excel.
    //
    // Why that mattered: the gate was refreshed at calc END only, so opening a
    // workbook that was SAVED with iterative calculation enabled ran its FIRST
    // recalculation with memoization still on — precisely the stale-across-
    // iterations defect the gate exists to prevent — and the wrong value then
    // survived for the whole session unless the cell was dirtied again. Priming
    // the gate at load closes that first cycle. xlAutoOpen is a valid command
    // context for GET.DOCUMENT (§19.4 / §23.6); if it fails (no active workbook
    // yet) the query leaves the gate untouched and the calc-end refresh takes
    // over from the next cycle, i.e. exactly the previous behavior.
    QueryIterativeCalcMode();
}

void CacheManager::QueryIterativeCalcMode() {
    // GET.DOCUMENT(15) = "TRUE if iterative calculation is enabled" for the
    // ACTIVE workbook; 16 is MaxIterations and 17 is MaxChange. Empirically
    // confirmed against Excel 16.0.20131.20154 by dumping GET.DOCUMENT(1..90)
    // with Application.Iteration false vs true: type_num 15 (BOOL 0 -> 1) and
    // 16 (100 -> the MaxIterations value) were the ONLY entries that moved.
    // The same dump run from inside the xleventCalculationEnded callback
    // returned identical values, which is what makes this call legal here —
    // GET.DOCUMENT is macro-sheet only, and the event callback IS a command
    // context (AGENTS.md §19.4 / §23.6).
    //
    // Failure (no active workbook, wrong context, older host) leaves the gate
    // at its previous value: this query may only ever DISABLE an optimization,
    // never change a digest, so "unknown" safely means "keep what we had".
    XLOPER12 xRes;
    xRes.xltype = 0;
    if (xll::CallExcel(xlfGetDocument, &xRes, 15) != xlretSuccess) return;

    const DWORD ty = xRes.xltype & ~(xlbitXLFree | xlbitDLLFree);
    bool on = false;
    bool known = false;
    if (ty == xltypeBool) {
        on = xRes.val.xbool != 0;
        known = true;
    } else if (ty == xltypeNum) { // defensive: some hosts answer numerically
        on = xRes.val.num != 0.0;
        known = true;
    }
    xll::CallExcel(xlFree, nullptr, &xRes);
    if (!known) return;

    // NOTE: deliberately no logging here. xll_cache.cpp reaches Excel only
    // through xlCoerce/xlFree/xlfGetDocument (all satisfied by a stub Excel12v)
    // and calls into NO other xll_* translation unit, which is what lets
    // internal/assets/testdata/cache_native_test.cpp compile and RUN this file
    // offline with g++. Adding an xll::LogInfo call here breaks that link
    // (undefined reference); the gate transition is logged by the caller in
    // xll_events.cpp instead.
    iterativeCalc_.store(on, std::memory_order_release);
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

    // This IS the RefCache path — record it even when the gate below bypasses
    // the map, so calc end keeps re-querying Excel and the gate can be turned
    // back OFF when the user disables iterative calculation.
    refPathUsed_.store(true, std::memory_order_release);

    // Iterative (circular-reference) calculation re-evaluates the same cells
    // MANY times within ONE calculation cycle while CalculationEnded — the only
    // clear point — fires once for the whole cycle, so a memoized pass-1 digest
    // would be served to passes 2..N with the cell values already changed. Skip
    // the memoization (not the hash) while that mode is on. See
    // CacheManager::RefreshIterativeCalcMode in xll_cache.h.
    const bool bypassMemo = iterativeCalc_.load(std::memory_order_acquire);

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
        if (!bypassMemo) {
            refCache_.if_contains(key, [&](const std::pair<RefKey, uint64_t>& val) {
                hashVal = val.second;
                found = true;
            });
        }

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

            // Store in cache (skipped under the iterative-calculation gate: the
            // digest is still folded into `acc` below, so the KEY BYTES are
            // identical in both modes — only the memoization is dropped).
            if (!bypassMemo) refCache_.insert_or_assign(key, hashVal);
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
    //
    // The typeTag names the WIRE-PAYLOAD SHAPE (AGENTS.md §19.3), and the digest
    // MUST cover everything that payload carries — the token is the only key the
    // payload is shipped and looked up under (SendRefCachePayloadOnce dedups on
    // it; the Go RefCache is keyed by it):
    //   'g' grid / 'n' numgrid → payload is the CELL VALUES (ConvertGridArg /
    //       ConvertNumGrid). Coordinates are absent from the payload, so they
    //       stay out of the digest: two equal-valued ranges are the same payload
    //       and correctly share one topic + one ship ("same grid → same topic").
    //   'r' range / 'a' any-of-a-reference → payload is the COORDINATES
    //       (ConvertRange, and ConvertAny of a ref also emits a Range: sheet
    //       name + rect table). A value-only digest gave two DISTINCT ranges
    //       holding equal numbers the SAME token, so the second ship was deduped
    //       away and the Go side resolved the FIRST range's coordinates for the
    //       second argument — a silent wrong answer (reviewer HIGH, 2026-07-26).
    //       Identity is folded IN ADDITION to the values, not instead of them:
    //       the values keep "edited cell → new token → new topic → fresh
    //       compute" alive, which is the RTD freshness contract of §19.3 (a
    //       coordinates-only digest would freeze such a topic across edits).
    const bool coordinatePayload = (typeTag == 'r' || typeTag == 'a');
    const uint64_t h = coordinatePayload ? HashXLOPERContentWithRefIdentity(px)
                                         : HashXLOPERContent(px);
    return FormatHashToken(typeTag, h);
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

const char* GridArgStatusText(GridArgStatus s) {
    switch (s) {
        case GridArgStatus::kOk:           return "ok";
        case GridArgStatus::kCoerceFailed: return "xlCoerce failed";
        case GridArgStatus::kNotAnArray:   return "xlCoerce did not yield a cell array "
                                                  "(multi-area or otherwise unflattenable reference)";
        case GridArgStatus::kMultiArea:    return "multi-area (union) reference — a grid is one rectangle";
        case GridArgStatus::kTooLarge:     return "reference area exceeds the grid-argument cell limit";
    }
    return "unknown";
}

namespace {

// Cells covered by one rectangle. XLREF12's bounds are INCLUSIVE and Excel's
// grid maxes out at 2^20 rows x 2^14 columns, so the product cannot overflow
// 64 bits; an inverted/degenerate rect contributes nothing.
uint64_t RectCells(const XLREF12& r) {
    if (r.rwLast < r.rwFirst || r.colLast < r.colFirst) return 0;
    const uint64_t rows = (uint64_t)(int64_t)(r.rwLast - r.rwFirst) + 1;
    const uint64_t cols = (uint64_t)(int64_t)(r.colLast - r.colFirst) + 1;
    return rows * cols;
}

} // namespace

void MeasureRefArg(const XLOPER12* op, uint64_t* outCells, uint32_t* outAreas) {
    uint64_t cells = 0;
    uint32_t areas = 0;
    if (op) {
        const DWORD ty = op->xltype & ~(xlbitXLFree | xlbitDLLFree);
        if ((ty & xltypeRef) && op->val.mref.lpmref) {
            // xltypeRef: idSheet + a TABLE of rects. More than one rect is a
            // union reference.
            const XLMREF12* m = op->val.mref.lpmref;
            areas = (uint32_t)m->count;
            for (WORD i = 0; i < m->count; ++i) cells += RectCells(m->reftbl[i]);
        } else if (ty & xltypeSRef) {
            // xltypeSRef: exactly ONE rect and no sheet id — the shape Excel
            // passes for a same-sheet reference, and the one a whole-column
            // reference ($P:$R -> 1,048,576 x 3) arrives as.
            areas = 1;
            cells = RectCells(op->val.sref.ref);
        }
    }
    if (outCells) *outCells = cells;
    if (outAreas) *outAreas = areas;
}

flatbuffers::Offset<protocol::Grid> ConvertGridArg(const XLOPER12* op,
                                                   flatbuffers::FlatBufferBuilder& builder,
                                                   GridArgStatus* status,
                                                   uint64_t maxCells) {
    if (status) *status = GridArgStatus::kOk;

    // Every refusal returns the SAME well-formed empty grid. The caller checks
    // the status and never ships it; returning a valid offset (rather than a
    // default-constructed one) keeps the builder's invariants intact whatever
    // the caller does next.
    auto refuse = [&](GridArgStatus s) {
        if (status) *status = s;
        return protocol::CreateGrid(builder, 0, 0, 0);
    };

    // types' ConvertGrid dereferences its argument unconditionally (inside a
    // catch(...) that a null deref does not raise), so a null must not reach it.
    if (!op) return refuse(GridArgStatus::kNotAnArray);

    const DWORD ty = op->xltype & ~(xlbitXLFree | xlbitDLLFree);

    // A grid arg passed as a range reference must be coerced to its cell
    // VALUES before ConvertGrid (which only understands xltypeMulti). Mirrors
    // HashXLOPERInto's ref handling above.
    if (ty & (xltypeRef | xltypeSRef)) {
        uint64_t cells = 0;
        uint32_t areas = 0;
        MeasureRefArg(op, &cells, &areas);

        // (a) Union reference. Refused STRUCTURALLY, before Excel is asked:
        // xlCoerce cannot produce the union, and whatever it does return would
        // be interpreted as data. See GridArgStatus::kMultiArea.
        if (areas > 1) return refuse(GridArgStatus::kMultiArea);

        // (b) Over-large reference. This check is the whole point of doing the
        // measurement up front: =SumGrid($P:$R) is ~3.1M cells, and merely
        // ASKING Excel to coerce that is ~100 MB of XLOPER12 before we ever get
        // to serialize it. Refuse without allocating anything.
        if (cells > maxCells) return refuse(GridArgStatus::kTooLarge);

        XLOPER12 xVal;
        xVal.xltype = 0;
        XLOPER12 xType; xType.xltype = xltypeInt; xType.val.w = xltypeMulti;
        if (xll::CallExcel(xlCoerce, &xVal, op, &xType) != xlretSuccess) {
            // Coerce failed (xlretUncalced etc.): signal the caller so the
            // wrapper SKIPS shipping a payload entirely — the Go dispatch then
            // misses the token and pushes an explicit error to the topic,
            // instead of the handler silently receiving a degenerate 1x1 grid
            // (reviewer MED, 2026-06-12).
            return refuse(GridArgStatus::kCoerceFailed);
        }

        // xlCoerce answered SUCCESS — but did it answer with what we asked for?
        // Anything other than xltypeMulti (an xltypeErr #VALUE! for a shape
        // Excel will not flatten, most commonly) must NOT reach ConvertGrid:
        // ConvertGrid's non-multi fall-through wraps it as a 1x1 grid, the
        // handler sums it to 0, and the user gets a WRONG NUMBER with no error
        // anywhere. Refuse instead.
        const DWORD vt = xVal.xltype & ~(xlbitXLFree | xlbitDLLFree);
        if (vt != xltypeMulti) {
            xll::CallExcel(xlFree, nullptr, &xVal);
            return refuse(GridArgStatus::kNotAnArray);
        }

        auto off = ConvertGrid(&xVal, builder);
        xll::CallExcel(xlFree, nullptr, &xVal);
        return off;
    }

    // Not a reference. An array LITERAL ({1,2;3,4}) already arrives as
    // xltypeMulti and a scalar as itself; both go straight to ConvertGrid. The
    // literal is bounded by the same cap — it reaches the same fixed-size SHM
    // arena, and Excel is not the only possible producer of one.
    if (ty == xltypeMulti) {
        const uint64_t cells = (uint64_t)(uint32_t)op->val.array.rows *
                               (uint64_t)(uint32_t)op->val.array.columns;
        if (cells > maxCells) return refuse(GridArgStatus::kTooLarge);
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
            //
            // HashXLOPERContentWithRefIdentity (not HashXLOPERContent): a `range`
            // or `any` argument ships its COORDINATES on the wire (ConvertRange /
            // ConvertAny), so a value-only key let a cached call on A1:A10 be
            // served the result computed for C1:C10 whenever the two columns held
            // the same numbers — the cache-key twin of the RTD topic-token defect
            // documented in ContentHashToken above.
            //
            // MakeCacheKey cannot see the DECLARED argument type (the generated
            // wrapper pushes grid/range/any into one LPXLOPER12 vector), so the
            // identity is folded for EVERY reference argument. The trade is
            // deliberate and one-directional: for a `grid` arg this
            // over-discriminates (two equal-valued ranges miss each other's cache
            // entry and recompute — a perf loss), whereas under-discriminating a
            // `range` arg returns the WRONG ANSWER.
            const uint64_t refHash = CacheManager::Instance().GetOrComputeRefHash(
                arg, [](const XLOPER12* pRef) { return HashXLOPERContentWithRefIdentity(pRef); });
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
