#pragma once
#include <windows.h>
#include <string>
#include <vector>
#include <chrono>
#include <memory>
#include <mutex>
#include <functional>
#include "types/xlcall.h"
// converters.h pulls in flatbuffers + protocol_generated.h, giving us
// flatbuffers::Offset<protocol::Grid> and ConvertGrid for ConvertGridArg below.
#include "types/converters.h"

// Parallel Hashmap
// We use the parallel_flat_hash_map for thread-safe concurrent access.
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

    // Get or Compute hash for a Range reference.
    // If the reference (Sheet + Rect) is already in the RefCache (for this cycle), returns the cached hash.
    // Otherwise, invokes the callback to compute the hash and stores it.
    std::string GetOrComputeRefHash(const XLOPER12* pRef, std::function<std::string(const XLOPER12*)> computeFn);

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
    // Key: Function Signature + serialized args
    phmap::parallel_flat_hash_map<std::string, CacheEntry> cache_;

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
            size_t h = 17;
            h = h * 31 + k.sheetId;
            h = h * 31 + k.rwFirst;
            h = h * 31 + k.rwLast;
            h = h * 31 + k.colFirst;
            h = h * 31 + k.colLast;
            return h;
        }
    };

    phmap::parallel_flat_hash_map<RefKey, std::string, RefKeyHash> refCache_;
};

// Helper to generate key for a function call
std::string MakeCacheKey(const std::string& funcName, const std::vector<LPXLOPER12>& args);

// Helper to serialize an XLOPER12 to a string (for caching)
std::string SerializeXLOPER(const XLOPER12* px);

// ContentHashToken computes a deterministic, content-addressed RTD topic token
// for a composite argument (grid / range / any XLOPER12). The XLOPER12's value
// is serialized (refs are coerced to their cell values via SerializeXLOPER) and
// FNV-1a hashed; the result is "h:<typeTag><hex>". Identical content always
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
