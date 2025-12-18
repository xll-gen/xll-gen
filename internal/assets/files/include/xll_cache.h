#pragma once
#include <windows.h>
#ifndef INT32
#include <basetsd.h>
#endif

// Fallback for MinGW if INT32 is still missing
#ifndef INT32
typedef int INT32;
#endif

#include <string>
#include <vector>
#include <chrono>
#include <memory>
#include <mutex>
#include <functional>
#include "xlcall.h"

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

} // namespace xll
