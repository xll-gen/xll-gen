#pragma once

// xll_rtd_once.h — runtime support for mode:"rtd-once" functions.
//
// rtd-once wraps a normal (sync-shaped) Go handler in an RTD topic lifecycle:
// the cell shows #GETTING_DATA, the handler runs exactly once on the server
// and pushes a single value back through the RTD server, and the cell then
// resolves to that value. See AGENTS.md §19.3 for the full mechanism.
//
// This header is included by the generated xll_main.cpp only when the project
// declares at least one rtd-once function (and RTD is therefore enabled). It is
// guarded by XLL_RTD_ENABLED for parity with the rest of the RTD assets.
//
// THREADING: the wrapper runs on Excel calc threads; ProcessRtdUpdate runs on
// the IPC thread; CalculationEnded/xlAutoClose run on the main STA thread. All
// access to the registry's maps goes through a single mutex.

#ifdef XLL_RTD_ENABLED

#include <windows.h>
#include <oaidl.h>
#include <string>
#include <vector>
#include <map>
#include <set>
#include <mutex>
#include "types/xlcall.h"
#include "types/mem.h"

namespace xll {

// Excel's cell error code for "still being fetched", returned as the initial
// RTD value so the cell reads #GETTING_DATA. The COM VARIANT carries it as
// VT_ERROR with scode 2043 — IDENTICAL to rtd/server.h's RefreshData
// placeholder for not-yet-delivered topics (kept byte-for-byte in sync; do not
// diverge). xlerrGettingData on the XLL side is 43; the +2000 COM mapping
// Excel uses for cell errors makes it 2043.
inline VARIANT MakeGettingDataVariant() {
    VARIANT v;
    VariantInit(&v);
    v.vt = VT_ERROR;
    v.scode = 2043; // xlErrGettingData (matches rtd/server.h RefreshData)
    return v;
}

// Joins topic strings (t0=function name, t1.. = stringified scalar args) into a
// stable key. The unit separator (\x1f) cannot appear in a numeric arg string
// and is vanishingly unlikely in a string arg, so collisions across distinct
// (function, args) tuples are not a practical concern. The wrapper and
// ConnectData MUST build the key identically.
inline std::wstring MakeRtdOnceKey(const std::vector<std::wstring>& topicStrings) {
    std::wstring key;
    for (size_t i = 0; i < topicStrings.size(); ++i) {
        if (i) key.push_back(L'\x1f');
        key += topicStrings[i];
    }
    return key;
}

// Converts a completed rtd-once VARIANT result (as produced by
// ProcessRtdUpdate: VT_R8 / VT_BSTR / VT_I4 / VT_BOOL / VT_ERROR / VT_EMPTY)
// into a fresh DLL-managed XLOPER12 the wrapper returns directly to Excel
// (xlbitDLLFree set; xlAutoFree12 reclaims it), mirroring AnyToXLOPER12's
// allocation contract. Returns nullptr on an unrecognized VARIANT type.
inline LPXLOPER12 RtdOnceResultToXLOPER12(const VARIANT& v) {
    switch (v.vt) {
    case VT_BSTR: {
        // BSTR is a wide string with a length prefix; build an Excel string.
        std::wstring ws = v.bstrVal ? std::wstring(v.bstrVal, SysStringLen(v.bstrVal)) : std::wstring();
        return NewExcelString(ws);
    }
    case VT_R8: {
        LPXLOPER12 op = NewXLOPER12();
        op->xltype = xltypeNum | xlbitDLLFree;
        op->val.num = v.dblVal;
        return op;
    }
    case VT_I4: {
        LPXLOPER12 op = NewXLOPER12();
        op->xltype = xltypeInt | xlbitDLLFree;
        op->val.w = v.lVal;
        return op;
    }
    case VT_BOOL: {
        LPXLOPER12 op = NewXLOPER12();
        op->xltype = xltypeBool | xlbitDLLFree;
        op->val.xbool = (v.boolVal != VARIANT_FALSE) ? 1 : 0;
        return op;
    }
    case VT_ERROR: {
        LPXLOPER12 op = NewXLOPER12();
        op->xltype = xltypeErr | xlbitDLLFree;
        // Map the COM error scode back to an Excel cell error. 2043 is the
        // #GETTING_DATA placeholder (never stored in practice — it is only the
        // ConnectData initial value); every other scode (e.g. the
        // unsupported-value scode) collapses to #VALUE!.
        op->val.err = (v.scode == 2043) ? xlerrGettingData : xlerrValue;
        return op;
    }
    default: {
        // VT_EMPTY / VT_NULL / anything unexpected -> empty cell.
        LPXLOPER12 op = NewXLOPER12();
        op->xltype = xltypeNil | xlbitDLLFree;
        return op;
    }
    }
}

// RtdOnceRegistry holds the completed one-shot results plus the bookkeeping to
// know which RTD topics belong to rtd-once functions. Single global instance.
class RtdOnceRegistry {
public:
    static RtdOnceRegistry& Instance() {
        static RtdOnceRegistry inst;
        return inst;
    }

    // Populated once at xlAutoOpen with the set of rtd-once function names
    // (topicStrings[0]); separately, the subset declared memoize:true; and the
    // subset declared memoize_ttl, mapped name -> ttl in milliseconds (>0).
    // The three subsets are mutually exclusive per the rtd-once lifecycle
    // triad (once / memoize_ttl / memoize), enforced at config time. Cheap
    // copy; called before any topic can connect.
    void SetFunctionNames(const std::vector<std::wstring>& names,
                          const std::vector<std::wstring>& memoizeNames,
                          const std::vector<std::pair<std::wstring, unsigned long long>>& ttlNames) {
        std::lock_guard<std::mutex> lock(m_mutex);
        m_funcNames.clear();
        for (const auto& n : names) m_funcNames.insert(n);
        m_memoizeNames.clear();
        for (const auto& n : memoizeNames) m_memoizeNames.insert(n);
        m_ttlNames.clear();
        for (const auto& nt : ttlNames) m_ttlNames[nt.first] = nt.second;
    }

    // True if the first topic string names an rtd-once function.
    bool IsOnceFunction(const std::wstring& funcName) const {
        std::lock_guard<std::mutex> lock(m_mutex);
        return m_funcNames.count(funcName) != 0;
    }

    // Records the topicID -> key mapping at ConnectData time so a later
    // RtdUpdate (which only carries topicID) can be routed to the right key.
    // INVARIANT: many topicIDs may map to ONE key (Excel does not guarantee
    // topic sharing when the identical formula sits in several cells). The
    // mapping must stay topicID->key; reversing it to key->topicID would drop
    // duplicates and break UnregisterTopic/ClearNonMemoized liveness checks.
    void RegisterTopic(long topicID, const std::wstring& key) {
        std::lock_guard<std::mutex> lock(m_mutex);
        m_topicToKey[topicID] = key;
    }

    // Drops the topicID->key mapping (DisconnectData). The stored result (if
    // any) is intentionally retained: it lives under the key, and erasing it is
    // governed by the once/memoize lifecycle (Clear), not by disconnect.
    void UnregisterTopic(long topicID) {
        std::lock_guard<std::mutex> lock(m_mutex);
        m_topicToKey.erase(topicID);
    }

    // Looks up the key for a topicID. Returns true and fills `key` if the topic
    // belongs to an rtd-once function.
    bool KeyForTopic(long topicID, std::wstring& key) const {
        std::lock_guard<std::mutex> lock(m_mutex);
        auto it = m_topicToKey.find(topicID);
        if (it == m_topicToKey.end()) return false;
        key = it->second;
        return true;
    }

    // Stores the completed value for a key (called from ProcessRtdUpdate for
    // rtd-once topics). Takes a copy of the VARIANT and stamps it with a
    // monotonic tick (GetTickCount64, NEVER wall-clock) so memoize_ttl expiry
    // can be evaluated on read and at calc-end.
    void StoreResult(const std::wstring& key, const VARIANT& value) {
        std::lock_guard<std::mutex> lock(m_mutex);
        auto it = m_results.find(key);
        if (it == m_results.end()) {
            Entry e;
            VariantInit(&e.value);
            VariantCopy(&e.value, const_cast<VARIANT*>(&value));
            e.storedTick = GetTickCount64();
            m_results.emplace(key, std::move(e));
        } else {
            VariantClear(&it->second.value);
            VariantCopy(&it->second.value, const_cast<VARIANT*>(&value));
            it->second.storedTick = GetTickCount64();
        }
    }

    // Returns true and copies the completed value into `out` if present and not
    // expired. memoize_ttl expiry is evaluated HERE (read time): if the entry's
    // function declares a TTL, the key has NO live topic, and the entry's age
    // exceeds the TTL, the entry is erased and a miss is reported — the wrapper
    // then re-issues xlfRtd to recompute fresh. The liveness guard is preserved:
    // an entry whose key still has a connected topic is NEVER expired here (same
    // stuck-at-#GETTING_DATA race as ClearNonMemoized; see AGENTS.md §19.3).
    bool TryGetResult(const std::wstring& key, VARIANT* out) {
        std::lock_guard<std::mutex> lock(m_mutex);
        auto it = m_results.find(key);
        if (it == m_results.end()) return false;
        std::wstring fn = FuncNameOfKey(key);
        auto ttlIt = m_ttlNames.find(fn);
        if (ttlIt != m_ttlNames.end() && !KeyHasLiveTopic(key)) {
            ULONGLONG age = GetTickCount64() - it->second.storedTick;
            if (age > ttlIt->second) {
                VariantClear(&it->second.value);
                m_results.erase(it);
                return false;
            }
        }
        if (out) {
            VariantInit(out);
            VariantCopy(out, const_cast<VARIANT*>(&it->second.value));
        }
        return true;
    }

    // Clears completed results on CalculationEnded so the next user-initiated
    // recalc recomputes (F9 semantics). Per-function granularity is keyed off
    // the first key segment (the function name). The rtd-once lifecycle triad:
    //   * once (default):   erase if no live topic.
    //   * memoize_ttl:      erase if no live topic AND expired (age > ttl).
    //   * memoize:true:     never erase (retained until process teardown).
    //
    // LIVENESS GUARD: a result whose key still has a connected topic (a live
    // topicID->key mapping) is NEVER cleared here. Without this, a
    // CalculationEnded firing in the window between StoreResult and the
    // NotifyUpdate-driven recalc would erase the value before the wrapper ever
    // reads it; the wrapper would then re-issue xlfRtd against the
    // still-connected topic, Excel would replay the #GETTING_DATA initial
    // value, and — the one-shot handler having already run — no further update
    // would arrive: the cell would be stuck. Tying clearing to "topic already
    // disconnected" closes that race; the entry is reclaimed on the first
    // CalculationEnded after DisconnectData instead. The guard applies to TTL
    // functions identically: a live-topic TTL entry is kept even when expired.
    void ClearNonMemoized() {
        std::lock_guard<std::mutex> lock(m_mutex);
        std::set<std::wstring> liveKeys;
        for (const auto& kv : m_topicToKey) liveKeys.insert(kv.second);
        ULONGLONG now = GetTickCount64();
        for (auto it = m_results.begin(); it != m_results.end();) {
            std::wstring fn = FuncNameOfKey(it->first);
            bool live = liveKeys.count(it->first) != 0;
            bool erase = false;
            if (!live) {
                if (m_memoizeNames.count(fn) != 0) {
                    // memoize:true — never erase.
                    erase = false;
                } else {
                    auto ttlIt = m_ttlNames.find(fn);
                    if (ttlIt != m_ttlNames.end()) {
                        // memoize_ttl — erase only once expired.
                        erase = (now - it->second.storedTick) > ttlIt->second;
                    } else {
                        // once (default) — erase.
                        erase = true;
                    }
                }
            }
            if (erase) {
                VariantClear(&it->second.value);
                it = m_results.erase(it);
            } else {
                ++it;
            }
        }
    }

private:
    // A completed one-shot result plus the monotonic tick it was stored at
    // (for memoize_ttl expiry). Copying is deleted: a copy would alias the
    // VARIANT (and its BSTR) and invite a double VariantClear. Moves are
    // byte-wise; the registry alone manages the VARIANT's lifetime
    // (StoreResult / TryGetResult / ClearNonMemoized).
    struct Entry {
        VARIANT value;
        ULONGLONG storedTick = 0;
        Entry() = default;
        Entry(Entry&&) = default;
        Entry& operator=(Entry&&) = default;
        Entry(const Entry&) = delete;
        Entry& operator=(const Entry&) = delete;
    };

    // Extracts the leading function-name segment from a key built by
    // MakeRtdOnceKey (segments joined with \x1f).
    static std::wstring FuncNameOfKey(const std::wstring& key) {
        size_t sep = key.find(L'\x1f');
        return (sep == std::wstring::npos) ? key : key.substr(0, sep);
    }

    // True if any live topicID currently maps to this key. Caller must hold
    // m_mutex. Used by both expiry paths to honor the liveness guard.
    bool KeyHasLiveTopic(const std::wstring& key) const {
        for (const auto& kv : m_topicToKey) {
            if (kv.second == key) return true;
        }
        return false;
    }

    RtdOnceRegistry() = default;
    // Trivial dtor ON PURPOSE (§20.2 "leak, don't crash"): this is a Meyers
    // singleton whose dtor runs among static destructors at DLL teardown. On a
    // forced unload, oleaut32 may already be gone — VariantClear on a VT_BSTR
    // would call SysFreeString post-teardown. The stored VARIANTs are
    // intentionally leaked; the process heap reclaims them at exit.
    ~RtdOnceRegistry() = default;
    RtdOnceRegistry(const RtdOnceRegistry&) = delete;
    RtdOnceRegistry& operator=(const RtdOnceRegistry&) = delete;

    mutable std::mutex m_mutex;
    std::set<std::wstring> m_funcNames;
    std::set<std::wstring> m_memoizeNames;
    std::map<std::wstring, unsigned long long> m_ttlNames; // name -> ttl ms (>0)
    std::map<long, std::wstring> m_topicToKey;
    std::map<std::wstring, Entry> m_results;
};

} // namespace xll

#endif // XLL_RTD_ENABLED
