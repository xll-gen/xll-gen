#pragma once

// xll_rtd_once_grid.h — runtime support for mode:"rtd-once" functions that
// return grid/numgrid and SPILL.
//
// RTD can only deliver a scalar, so the readiness signal rides RTD while the
// grid itself is delivered guest->host (Go->C++) into this registry as a
// serialized protocol::Any (Grid or NumGrid). The RTD update re-runs the cell's
// wrapper, which pulls the cached grid bytes back out of here and returns them
// as an xltypeMulti / FP12 so Excel spills. See AGENTS.md §19.3 for the scalar
// rtd-once mechanism this mirrors.
//
// This registry is the byte-buffer twin of RtdOnceRegistry (xll_rtd_once.h): it
// copies that registry's mutex discipline, topic bookkeeping, the
// once/memoize_ttl/memoize lifecycle triad AND the TRANSIENT (error) entry
// semantics EXACTLY, but stores std::vector<uint8_t> payloads instead of
// VARIANTs. It keeps its OWN topic map (m_topicToKey) independent of the scalar
// registry.
//
// This header is included by the generated xll_main.cpp only when the project
// declares at least one grid-returning rtd-once function (and RTD is therefore
// enabled). It is guarded by XLL_RTD_ENABLED for parity with the rest of the
// RTD assets.
//
// THREADING: the wrapper runs on Excel calc threads; the guest->host store runs
// on the IPC thread; CalculationEnded/xlAutoClose run on the main STA thread.
// All access to the registry's maps goes through a single mutex.

#ifdef XLL_RTD_ENABLED

#include <windows.h>
#include <string>
#include <vector>
#include <map>
#include <set>
#include <mutex>
#include <cstddef>
#include <cstdint>

namespace xll {

// Outcome of a RtdOnceGridRegistry::TryGet. An entry is EITHER a completed grid
// payload OR a TRANSIENT error, and the two must never be confused:
//   * a bool "hit" plus an out-string could not tell a kError entry whose
//     message happens to be empty from a kResult entry holding zero bytes, and
//     flatbuffers::GetRoot over zero bytes is undefined behaviour;
//   * kMiss is the ONLY outcome that may lead the wrapper to (re-)issue xlfRtd.
//     Returning kMiss for an error would re-issue xlfRtd against a topic that is
//     still CONNECTED, so ConnectData would never re-fire, the one-shot handler
//     would never re-run, and the cell would be stuck at the loading placeholder
//     forever — the exact trap AGENTS.md §19.3 documents for the scalar path.
// enum class (not a plain enum) so a stale `if (TryGet(...))` fails to compile
// instead of silently treating kMiss as false and kError as true.
enum class OnceGridLookup {
    kMiss = 0, // no entry (absent, expired, or a reclaimed transient error)
    kResult,   // completed payload: `out` holds the serialized RtdOnceGridResult
    kError,    // transient error: `errOut` holds the message the handler failed with
};

// RtdOnceGridRegistry holds the completed one-shot grid payloads (serialized
// protocol::Any bytes) plus the bookkeeping to know which RTD topics belong to
// grid-returning rtd-once functions. Single global instance.
//
// Entries come in two flavours, mirroring the scalar RtdOnceRegistry:
//   * NORMAL    — a completed grid payload delivered guest->host via
//                 MSG_RTD_ONCE_GRID. Retention follows the function's declared
//                 lifecycle (once / memoize_ttl / memoize).
//   * TRANSIENT — an ERROR pushed in place of a result (handler error, ctx
//                 cancellation, composite-arg resolve miss, SendOnceGrid
//                 failure). It arrives as an RtdUpdate with is_error=true and is
//                 routed here by ProcessRtdUpdate (xll_rtd.cpp) instead of into
//                 the scalar registry, because the grid-once wrapper reads ONLY
//                 this registry. Stored so the cell paints something diagnosable
//                 (grid: the message text; numgrid: an empty 0x0 FP12 — the FP12
//                 ABI cannot carry text or a cell error — plus the message in the
//                 log) and, crucially, so the wrapper takes the HIT path and does
//                 NOT re-issue xlfRtd: the cell then drops its RTD reference,
//                 Excel disconnects the topic, the transient entry is reclaimed
//                 as if the function were plain `once` (whatever it declared),
//                 and the next recalc re-runs the handler. See StoreError /
//                 ClearNonMemoized / TryGet and AGENTS.md §19.3.
class RtdOnceGridRegistry {
public:
    static RtdOnceGridRegistry& Instance() {
        static RtdOnceGridRegistry inst;
        return inst;
    }

    // Populated once at xlAutoOpen with the set of grid-once function names
    // (topicStrings[0]); separately, the subset declared memoize:true; and the
    // subset declared memoize_ttl, mapped name -> ttl in milliseconds (>0). The
    // three subsets are mutually exclusive per the rtd-once lifecycle triad
    // (once / memoize_ttl / memoize), enforced at config time. Cheap copy;
    // called before any topic can connect.
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

    // True if the first topic string names a grid-returning rtd-once function.
    bool IsOnceGridFunction(const std::wstring& funcName) const {
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

    // Drops the topicID->key mapping (DisconnectData). The stored payload (if
    // any) is intentionally retained: it lives under the key, and erasing it is
    // governed by the once/memoize lifecycle (Clear), not by disconnect.
    void UnregisterTopic(long topicID) {
        std::lock_guard<std::mutex> lock(m_mutex);
        m_topicToKey.erase(topicID);
    }

    // Looks up the key for a topicID. Returns true and fills `key` if the topic
    // belongs to a grid-once function.
    bool KeyForTopic(long topicID, std::wstring& key) const {
        std::lock_guard<std::mutex> lock(m_mutex);
        auto it = m_topicToKey.find(topicID);
        if (it == m_topicToKey.end()) return false;
        key = it->second;
        return true;
    }

    // Stores the serialized grid payload for a key (called from the guest->host
    // RtdOnceGridResult handler). Copies `len` bytes (the serialized
    // protocol::Any the wrapper will later feed to GridToXLOPER12/NumGridToFP12)
    // and stamps a monotonic tick (GetTickCount64, NEVER wall-clock) so
    // memoize_ttl expiry can be evaluated on read and at calc-end.
    //
    // A completed payload always PROMOTES the entry back to normal retention:
    // the transient flag is re-stamped false and any previous error text is
    // dropped, so a retry that succeeds is memoized exactly like a first-try
    // success (twin of RtdOnceRegistry::StoreResult's re-stamp).
    void Store(const std::wstring& key, const uint8_t* data, size_t len) {
        std::lock_guard<std::mutex> lock(m_mutex);
        auto it = m_results.find(key);
        if (it == m_results.end()) {
            Entry e;
            e.bytes.assign(data, data + len);
            e.storedTick = GetTickCount64();
            m_results.emplace(key, std::move(e));
        } else {
            it->second.bytes.assign(data, data + len);
            it->second.errorText.clear();
            it->second.transient = false;
            it->second.storedTick = GetTickCount64();
        }
    }

    // Stores an ERROR for a key as a TRANSIENT entry (called from
    // ProcessRtdUpdate when an RtdUpdate for a grid-once topic carries
    // is_error=true). There is no grid to store — `bytes` is cleared — but the
    // entry MUST exist, for the same reason the scalar path stores its errors:
    // the grid-once wrapper puts a value in the cell only on a TryGet HIT, so
    // with no entry every recalc misses, re-issues xlfRtd against the STILL
    // CONNECTED topic, re-paints the loading placeholder, and the cell can never
    // recover (ConnectData never re-fires, so the one-shot handler never
    // re-runs).
    //
    // `transient` governs RETENTION, not storage: the entry is reclaimed like a
    // plain `once` entry no matter what the function declared, so memoize:true
    // cannot freeze the error until XLL reload and memoize_ttl cannot freeze it
    // for the TTL window (see ClearNonMemoized / TryGet).
    void StoreError(const std::wstring& key, const std::wstring& text) {
        std::lock_guard<std::mutex> lock(m_mutex);
        auto it = m_results.find(key);
        if (it == m_results.end()) {
            Entry e;
            e.errorText = text;
            e.transient = true;
            e.storedTick = GetTickCount64();
            m_results.emplace(key, std::move(e));
        } else {
            it->second.bytes.clear();
            it->second.errorText = text;
            it->second.transient = true;
            it->second.storedTick = GetTickCount64();
        }
    }

    // Looks the key up and reports which KIND of entry (if any) is there:
    // kResult copies the stored payload into `out`, kError copies the message
    // into `errOut` (and clears `out`), kMiss means the wrapper must issue
    // xlfRtd. memoize_ttl expiry is evaluated HERE (read time): if the entry's
    // function declares a TTL, the key has NO live topic, and the entry's age
    // exceeds the TTL, the entry is erased and a miss is reported — the wrapper
    // then re-issues xlfRtd to recompute fresh. The liveness guard is preserved:
    // an entry whose key still has a connected topic is NEVER expired here (same
    // stuck-at-#GETTING_DATA race as ClearNonMemoized; see AGENTS.md §19.3).
    //
    // TRANSIENT (error) entries expire the same way with a ZERO TTL: readable
    // while the topic that produced them is still connected — that is the one
    // recalc in which the cell must paint the failure — and a MISS afterwards.
    // This read-side rule is the twin of ClearNonMemoized's transient rule and
    // covers the DisconnectData/CalculationEnded ordering window: if
    // CalculationEnded fires BEFORE DisconnectData the liveness guard keeps the
    // entry, and without this rule the *next* recalc would re-serve the stale
    // error for one extra cycle instead of recomputing.
    OnceGridLookup TryGet(const std::wstring& key, std::vector<uint8_t>* out,
                          std::wstring* errOut = nullptr) {
        std::lock_guard<std::mutex> lock(m_mutex);
        auto it = m_results.find(key);
        if (it == m_results.end()) return OnceGridLookup::kMiss;
        if (it->second.transient && !KeyHasLiveTopic(key)) {
            m_results.erase(it);
            return OnceGridLookup::kMiss;
        }
        std::wstring fn = FuncNameOfKey(key);
        auto ttlIt = m_ttlNames.find(fn);
        if (ttlIt != m_ttlNames.end() && !KeyHasLiveTopic(key)) {
            ULONGLONG age = GetTickCount64() - it->second.storedTick;
            if (age > ttlIt->second) {
                m_results.erase(it);
                return OnceGridLookup::kMiss;
            }
        }
        if (it->second.transient) {
            if (out) out->clear();
            if (errOut) *errOut = it->second.errorText;
            return OnceGridLookup::kError;
        }
        if (out) {
            *out = it->second.bytes;
        }
        return OnceGridLookup::kResult;
    }

    // Clears completed payloads on CalculationEnded so the next user-initiated
    // recalc recomputes (F9 semantics). Per-function granularity is keyed off
    // the first key segment (the function name). The rtd-once lifecycle triad:
    //   * once (default):   erase if no live topic.
    //   * memoize_ttl:      erase if no live topic AND expired (age > ttl).
    //   * memoize:true:     never erase (retained until process teardown).
    // TRANSIENT entries (errors) OVERRIDE all three and are treated as `once`:
    // erase as soon as the topic is gone. An error is never a memoizable result
    // — retaining it would pin the cell on the failure until XLL reload
    // (memoize:true) or for the whole TTL window (memoize_ttl), and because the
    // one-shot handler only re-runs on a fresh ConnectData the user would have no
    // way to retry. Erasing turns the next recalc into a cache miss -> xlfRtd ->
    // new topic -> handler re-runs.
    //
    // LIVENESS GUARD: a payload whose key still has a connected topic (a live
    // topicID->key mapping) is NEVER cleared here. Without this, a
    // CalculationEnded firing in the window between Store and the
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
                if (it->second.transient) {
                    // TRANSIENT (error) — treated as `once` REGARDLESS of the
                    // function's memoize / memoize_ttl policy.
                    erase = true;
                } else if (m_memoizeNames.count(fn) != 0) {
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
                it = m_results.erase(it);
            } else {
                ++it;
            }
        }
    }

private:
    // A one-shot grid entry: EITHER a completed payload (serialized
    // RtdOnceGridResult bytes, transient=false) OR a TRANSIENT error message
    // (bytes empty, errorText set, transient=true), plus the monotonic tick it
    // was stored at (for memoize_ttl expiry). The two are discriminated by
    // `transient`, never by "bytes is empty" — see OnceGridLookup. Unlike the
    // scalar registry's VARIANT Entry, std::vector<uint8_t>/std::wstring own
    // their storage and copy/move safely, so the default special members are
    // correct and no copy/move is deleted.
    struct Entry {
        std::vector<uint8_t> bytes;
        std::wstring errorText;
        ULONGLONG storedTick = 0;
        bool transient = false;
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

    RtdOnceGridRegistry() = default;
    // Trivial dtor ON PURPOSE (§20.2 "leak, don't crash"): this is a Meyers
    // singleton whose dtor runs among static destructors at DLL teardown. The
    // stored payloads are std::vector<uint8_t> with real destructors, so unlike
    // the scalar VARIANT registry there is NO oleaut32 dependency to fear — but
    // we still default the dtor and lean on the trivial-teardown discipline: on
    // a forced unload the maps are simply abandoned and the process heap
    // reclaims them at exit.
    ~RtdOnceGridRegistry() = default;
    RtdOnceGridRegistry(const RtdOnceGridRegistry&) = delete;
    RtdOnceGridRegistry& operator=(const RtdOnceGridRegistry&) = delete;

    mutable std::mutex m_mutex;
    std::set<std::wstring> m_funcNames;
    std::set<std::wstring> m_memoizeNames;
    std::map<std::wstring, unsigned long long> m_ttlNames; // name -> ttl ms (>0)
    std::map<long, std::wstring> m_topicToKey;
    std::map<std::wstring, Entry> m_results;
};

} // namespace xll

#endif // XLL_RTD_ENABLED
