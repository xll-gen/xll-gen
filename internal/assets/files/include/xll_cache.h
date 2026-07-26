#pragma once
#include <windows.h>
#include <string>
#include <vector>
#include <chrono>
#include <cstdint>
#include <memory>
#include <mutex>
#include <functional>
#include <type_traits>
#include "types/xlcall.h"
// converters.h pulls in flatbuffers + protocol_generated.h, giving us
// flatbuffers::Offset<protocol::Grid> and ConvertGrid for ConvertGridArg below.
#include "types/converters.h"

// Parallel Hashmap
//
// The maps below use the `_m` ("mutex") aliases — `parallel_flat_hash_map_m<K,V,...>`
// is `parallel_flat_hash_map<K,V,Hash,Eq,Alloc,N,std::mutex>` — so each of the
// 2**N submaps carries its OWN std::mutex and every phmap entry point
// (`if_contains`, `insert_or_assign`, `clear`, ...) locks the submap it touches.
// This is LOAD-BEARING, not cosmetic: phmap's 7th template parameter defaults to
// `phmap::NullMutex` ("use std::mutex to enable internal locks" —
// `parallel_hashmap/phmap_fwd_decl.h`), i.e. a map spelled with only <K,V> does
// NO locking at all. Cache-enabled functions are registered thread-safe (`$`),
// so Excel's multi-threaded recalculation calls Get/Put concurrently from
// several calculation threads — with NullMutex that is a real data race.
// The `_m` aliases keep N at its default 4 (16 submaps), so lock granularity /
// footprint are unchanged versus the previous (unlocked) spelling.
#pragma warning(push)
#pragma warning(disable: 4100 4127) // Disable warnings from external lib
#include <parallel_hashmap/phmap.h>
#pragma warning(pop)

namespace xll {

struct CacheConfig {
    bool enabled = false;
    std::chrono::milliseconds ttl{0};
    std::chrono::milliseconds jitter{0};
};

class CacheManager {
public:
    static CacheManager& Instance();

    // Check cache. Returns true if hit, populating result.
    bool Get(const std::string& key, std::vector<uint8_t>& result);

    // Store in cache.
    void Put(const std::string& key, const std::vector<uint8_t>& data, const CacheConfig& config);

    // Get or Compute the content hash of a Range reference.
    // If the reference (Sheet + Rect) is already in the RefCache (for this cycle), returns the cached hash.
    // Otherwise, invokes the callback to compute the hash and stores it.
    // A multi-rect xltypeRef folds its per-rect hashes through ONE continuous
    // FNV-1a stream (order-sensitive, full avalanche) rather than concatenating
    // text, so the result is a constant-size digest regardless of rect count.
    // computeFn is invoked with NO map lock held (it may call back into Excel).
    uint64_t GetOrComputeRefHash(const XLOPER12* pRef, const std::function<uint64_t(const XLOPER12*)>& computeFn);

    // Clear the Ref cache (call on CalculationEnded)
    void ClearRefCache();

private:
    CacheManager() = default;
    ~CacheManager() = default;
    CacheManager(const CacheManager&) = delete;
    CacheManager& operator=(const CacheManager&) = delete;

    struct CacheEntry {
        std::vector<uint8_t> data;
        std::chrono::steady_clock::time_point expiry;
    };

    // Main result cache
    // Key: "<funcName>#<16 hex digits>" (see MakeCacheKey) — CONSTANT size, so
    // hashing/comparing a key is O(1) instead of O(argument content).
    phmap::parallel_flat_hash_map_m<std::string, CacheEntry> cache_;

    // Compile-time regression gate for the locking above. `_m` and the plain
    // spelling differ ONLY in the Mutex template argument, so if someone
    // "simplifies" this back to `parallel_flat_hash_map<std::string, CacheEntry>`
    // the two types become identical and this fires. Reverting the alias silently
    // reinstates phmap::NullMutex — i.e. an unsynchronized map read and written
    // from several Excel calculation threads (verified to corrupt the map: an 8
    // thread Get/Put stress trips phmap's own `i < capacity_` assertion).
    static_assert(!std::is_same<decltype(cache_),
                                phmap::parallel_flat_hash_map<std::string, CacheEntry>>::value,
                  "cache_ must keep the _m (std::mutex) alias: the plain "
                  "parallel_flat_hash_map spelling defaults to phmap::NullMutex, "
                  "which performs NO locking");

    // Ref content hash cache (Cycle scoped)
    // Key: SheetID + Rect
    struct RefKey {
        IDSHEET sheetId;
        RW rwFirst;
        RW rwLast;
        COL colFirst;
        COL colLast;

        bool operator==(const RefKey& other) const {
            return sheetId == other.sheetId &&
                   rwFirst == other.rwFirst && rwLast == other.rwLast &&
                   colFirst == other.colFirst && colLast == other.colLast;
        }
    };

    struct RefKeyHash {
        size_t operator()(const RefKey& k) const {
            uint64_t h = 17;
            h = h * 31 + (uint64_t)k.sheetId;
            h = h * 31 + (uint64_t)(uint32_t)k.rwFirst;
            h = h * 31 + (uint64_t)(uint32_t)k.rwLast;
            h = h * 31 + (uint64_t)(uint32_t)k.colFirst;
            h = h * 31 + (uint64_t)(uint32_t)k.colLast;
            // splitmix64 finalizer. The polynomial above leaves most of its
            // entropy in the HIGH bits (sheetId is scaled by 31^4) while phmap
            // picks the submap from bits 8..31 (`subidx()`) and the control byte
            // from bits 7..14 — so without a finalizer whole sheets funnel into
            // one submap. That was merely a bucket imbalance while the map was
            // unlocked; now that each submap owns a std::mutex it would also be
            // lock contention across calculation threads.
            h ^= h >> 30; h *= 0xbf58476d1ce4e5b9ULL;
            h ^= h >> 27; h *= 0x94d049bb133111ebULL;
            h ^= h >> 31;
            return (size_t)h;
        }
    };

    phmap::parallel_flat_hash_map_m<RefKey, uint64_t, RefKeyHash> refCache_;

    // Same gate as for cache_ above. The RefCache is populated from the
    // thread-safe (`$`) UDF wrappers too, so it needs real locking as much as the
    // result cache does.
    static_assert(!std::is_same<decltype(refCache_),
                                phmap::parallel_flat_hash_map<RefKey, uint64_t, RefKeyHash>>::value,
                  "refCache_ must keep the _m (std::mutex) alias: the plain "
                  "parallel_flat_hash_map spelling defaults to phmap::NullMutex, "
                  "which performs NO locking");
};

// MakeCacheKey builds the result-cache key for one call. The key is
// "<funcName>#<16 hex digits>": a CONSTANT-SIZE digest of (funcName, arg count,
// per-arg content) rather than the full serialization of every argument. The
// funcName prefix is kept so keys stay greppable in logs AND so two different
// functions can never share a key even under a digest collision.
//
// Reference args (xltypeRef/xltypeSRef) route through
// CacheManager::GetOrComputeRefHash so a range hashed once in a calculation
// cycle is not re-coerced for every call that mentions it, and are digested with
// HashXLOPERContentWithRefIdentity — the key must separate two same-valued
// ranges because a `range`/`any` argument ships its COORDINATES to the handler.
//
// Callers may append their own suffixes (the generated wrapper appends
// "|numgrid:<token>" for FP12 args, which cannot enter the LPXLOPER12 vector).
std::string MakeCacheKey(const std::string& funcName, const std::vector<LPXLOPER12>& args);

// HashXLOPERInto streams an XLOPER12's CONTENT through an existing FNV-1a digest
// and returns the updated digest. It allocates nothing and never materializes an
// intermediate string: every field is fed as a one-byte type tag plus its raw
// little-endian bytes (doubles as their 8 IEEE-754 bytes, strings as a uint32
// length plus the raw UTF-16 code units). xltypeRef/xltypeSRef are coerced to
// their cell VALUES first (xlCoerce -> xltypeMulti), so the digest tracks CONTENT
// rather than reference coordinates; a coercion failure folds in the reference
// IDENTITY instead so two distinct failing refs still hash apart.
//
// The one-byte tags are part of the on-wire cache-key/topic-token identity:
// changing one invalidates every in-memory cache entry and every RTD topic.
uint64_t HashXLOPERInto(uint64_t seed, const XLOPER12* px);

// HashXLOPERContent is HashXLOPERInto seeded with the FNV-1a offset basis.
uint64_t HashXLOPERContent(const XLOPER12* px);

// HashXLOPERWithRefIdentity is HashXLOPERInto plus the reference IDENTITY: for an
// xltypeRef/xltypeSRef it folds the sheet id (xltypeRef only — an xltypeSRef
// carries no sheet) and the rect table into the SAME continuous FNV-1a stream
// AHEAD of the coerced cell values. A non-reference streams byte-identically to
// HashXLOPERInto (an inline value array has no identity to fold).
//
// Use this wherever the digest keys a payload that carries COORDINATES — a
// `range` argument (types' ConvertRange emits sheet + rect table) or an `any`
// argument holding a reference (ConvertAny emits a Range for those). Hashing
// only the coerced VALUES there let two DISTINCT ranges holding the same numbers
// share one token/key, so the second payload was deduped away and the consumer
// resolved the FIRST range's coordinates (reviewer HIGH, 2026-07-26).
//
// Identity is folded IN ADDITION to the values, never instead of them: the value
// part keeps "edited cell -> new digest -> new RTD topic -> fresh compute" alive
// (AGENTS.md §19.3), which a coordinates-only digest would break.
uint64_t HashXLOPERWithRefIdentity(uint64_t seed, const XLOPER12* px);

// HashXLOPERContentWithRefIdentity is HashXLOPERWithRefIdentity seeded with the
// FNV-1a offset basis.
uint64_t HashXLOPERContentWithRefIdentity(const XLOPER12* px);

// SerializeXLOPER renders an XLOPER12 as a human-readable string.
//
// DIAGNOSTICS ONLY — it is deliberately NOT on the cache-key / RTD-token path
// any more (see HashXLOPERInto, which hashes the same content without
// allocating). Keep the two in agreement about which fields are significant, but
// note that only HashXLOPERInto defines cache identity.
std::string SerializeXLOPER(const XLOPER12* px);

// ContentHashToken computes a deterministic, content-addressed RTD topic token
// for a composite argument (grid / range / any XLOPER12). The XLOPER12's value
// is streamed through FNV-1a by HashXLOPERInto (refs are coerced to their cell
// values first); the result is "h:<typeTag><hex>". Identical content always
// produces the same token (so the same grid maps to the same RTD topic and
// edited content produces a fresh token → fresh compute). Used by the rtd /
// rtd-once wrappers for the content-hash payload path (AGENTS.md §19.3).
//
// typeTag namespaces the hash by the WIRE PAYLOAD shape ('g' grid, 'r' range,
// 'a' any, 'n' numgrid). The SAME range A1:B2 serialized as a grid (values) vs
// a range (coordinates) is a DIFFERENT payload; without the tag both would map
// to the same token and one cell's payload would satisfy the other's lookup
// with the wrong union type. The tag keeps each (content, target-type) pair on
// its own topic and its own RefCache entry.
//
// The tag also SELECTS the digest, because the digest must cover everything the
// payload it keys carries:
//   'g' / 'n' → payload is the cell VALUES → HashXLOPERContent (coordinates are
//               not in the payload, so two equal-valued ranges correctly share
//               one topic and one ship).
//   'r' / 'a' → payload is the COORDINATES (ConvertRange; ConvertAny of a ref
//               emits a Range too) → HashXLOPERContentWithRefIdentity, which
//               folds sheet + rects on top of the values. Without it two
//               distinct ranges holding the same numbers produced one token,
//               SendRefCachePayloadOnce skipped the second ship, and the handler
//               received the FIRST range's coordinates.
std::string ContentHashToken(char typeTag, const XLOPER12* px);

// ContentHashTokenFP12 is the FP12* (numgrid) overload of ContentHashToken: it
// hashes the rows/cols + raw double payload of the floating-point grid under
// the 'n' type tag.
std::string ContentHashTokenFP12(const FP12* fp);

// ConvertGridArg serializes a `grid`-typed RTD argument into a protocol::Grid.
// A grid arg is registered `U`, so Excel passes a REFERENCE (xltypeRef/SRef)
// for a range like A1:B2; types' ConvertGrid only handles xltypeMulti and would
// otherwise emit a 1x1 Nil grid. This helper coerces a reference to its cell
// VALUES (xlCoerce → xltypeMulti) first, then defers to ConvertGrid, so the Go
// handler receives the populated grid. A non-reference (already a value array)
// is passed through unchanged. Declared here (not in types) so the coercion
// lives with the wrapper that needs it, without a types release.
//
// coerceOk (optional): set to false when a reference arg's xlCoerce FAILED
// (xlretUncalced etc.) — the returned offset is then a degenerate 1x1 grid and
// the caller must NOT ship it as the token's payload (skip the send; the Go
// dispatch surfaces an explicit miss error instead of silently delivering a
// wrong-shaped grid).
flatbuffers::Offset<protocol::Grid> ConvertGridArg(const XLOPER12* op, flatbuffers::FlatBufferBuilder& builder, bool* coerceOk = nullptr);

} // namespace xll
