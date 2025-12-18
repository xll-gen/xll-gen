#include "include/xll_cache.h"
#include "include/xll_log.h"
#include "include/xll_utility.h"
#include "include/PascalString.h"
#include <sstream>
#include <random>
#include <iomanip>

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

                // Easier: Use a small buffer.
                char mrefBuf[sizeof(WORD) + sizeof(XLREF12)];
                XLMREF12* tempMRef = (XLMREF12*)mrefBuf;
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

std::string SerializeXLOPER(const XLOPER12* px) {
    if (!px) return "null";
    std::stringstream ss;

    switch (px->xltype & ~(xlbitXLFree | xlbitDLLFree)) {
        case xltypeNum:
            ss << "Num:" << px->val.num;
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
                 if (Excel12(xlCoerce, &xVal, 2, px, &xType) == xlretSuccess) {
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
                     Excel12(xlFree, 0, 1, &xVal);
                 } else {
                     ss << "RefError";
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
