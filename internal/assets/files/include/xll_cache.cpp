#include "xll_cache.h"
#include "xll_log.h"
#include "xll_utility.h"
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

    // Iterate over all references in the XLOPER (could be multi-area)
    // But for simplicity and common case, we treat each SRef in MRef.
    // Actually, MakeCacheKey handles iterating over args.
    // Here we assume pRef points to a single Ref (or we handle the whole thing).

    // If it is a reference, we need to extract the SheetID and Rect.
    // If it's a Multi-Ref (xltypeRef), it has an array of XLREF12.
    // We will hash each rectangle.

    std::stringstream ss;

    if (pRef->xltype == xltypeRef) {
        IDSHEET sheetId = pRef->val.mref.idSheet;
        for (DWORD i = 0; i < pRef->val.mref.cReferences; ++i) {
            const auto& rect = pRef->val.mref.lpmref[i];
            RefKey key = {sheetId, rect.rwFirst, rect.rwLast, rect.colFirst, rect.colLast};

            // Try to find in cache
            bool found = false;
            std::string hashVal;
            refCache_.if_contains(key, [&](const std::pair<RefKey, std::string>& val) {
                hashVal = val.second;
                found = true;
            });

            if (!found) {
                // Compute hash (requires reading values)
                // We construct a temporary XLOPER just for this rect to pass to computeFn?
                // Or computeFn assumes it reads the *value* of the ref.
                // We need to read the value of this specific rect.

                XLOPER12 xRef;
                xRef.xltype = xltypeRef;
                xRef.val.mref.lpmref = const_cast<XLREF12*>(&rect); // Safe-ish, we just read
                xRef.val.mref.idSheet = sheetId;
                xRef.val.mref.cReferences = 1;

                hashVal = computeFn(&xRef);

                // Store in cache
                refCache_.insert_or_assign(key, hashVal);
            }
            ss << hashVal << ";";
        }
    } else if (pRef->xltype == xltypeSRef) {
        // Single Ref (no sheet ID usually, usually assumes active sheet, but xltypeSRef is rare as input?
        // Excel passes xltypeRef usually for args.
        // But if we get SRef, we treat it.
        // SRef doesn't have sheet ID in struct. It implies current sheet?
        // Let's assume input is Ref.
        // If SRef, we can't reliably cache per cycle if sheet changes?
        // But cycle is short.
        // Let's ignore SRef caching for now or handle simple case.
        // SRef is usually output.
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

    // We only strictly need to handle types passed as arguments (Int, Num, Bool, Str, Ref, Err)
    // Multi and Missing are also possible.

    switch (px->xltype & ~(xlbitXLFree | xlbitDLLFree)) {
        case xltypeNum:
            ss << "Num:" << px->val.num;
            break;
        case xltypeStr:
            ss << "Str:" << PascalToWString(px->val.str).length() << ":"; // Encode length for safety
             // Append content. WString to UTF8 usually.
             // For cache key, pure bytes is fine.
             // Pascal string: first char is len.
             {
                 std::wstring ws = PascalToWString(px->val.str);
                 // Convert to simple string for key
                 ss << ConvertExcelString(ws.c_str());
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
             // References should be handled by GetOrComputeRefHash before calling this?
             // Or we just read values here.
             // If we are here, we are "computing the hash" of the value.
             // So we coerce to value (Array/Multi) and hash that.
             {
                 XLOPER12 xVal;
                 // xlCoerce to get value
                 // We coerce to Multi (xltypeMulti) to get all values.
                 XLOPER12 xType; xType.xltype = xltypeInt; xType.val.w = xltypeMulti;
                 if (Excel12(xlCoerce, &xVal, 2, px, &xType) == xlretSuccess) {
                     // Recursively serialize the Multi
                     if (xVal.xltype == xltypeMulti) {
                         ss << "Grid:" << xVal.val.array.rows << "x" << xVal.val.array.columns << "{";
                         DWORD count = xVal.val.array.rows * xVal.val.array.columns;
                         // Hash the content to keep key short?
                         // User wants "Human Readable".
                         // If grid is huge, human readable key is too long.
                         // But for cache key, we probably want Hash of the grid content if it's large.

                         // Let's serialize strictly.
                         for (DWORD i=0; i < count; ++i) {
                             ss << SerializeXLOPER(&xVal.val.array.lparray[i]) << ",";
                         }
                         ss << "}";
                     } else {
                         // Coerced to single value?
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
                 // Compute hash of the value
                 // We use SerializeXLOPER which coerces to value and serializes.
                 // Then we hash the string to keep it compact in the main key?
                 // Or keep it human readable?
                 // User said "Key should be human readable".
                 // But Ref content can be huge. "RangeHash(HASHVAL)" is readable enough.

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
