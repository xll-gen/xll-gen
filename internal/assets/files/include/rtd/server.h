#ifndef RTD_SERVER_H
#define RTD_SERVER_H

#include <mutex>
#include <atomic>
#include <vector>
#include <map>
#include <algorithm>
#include <string>
#include "defs.h"
#include "module.h"
#include "types/utility.h"

// Forward declaration of logging
namespace xll { void LogDebug(const std::string& msg); }
// Unload flag (defined in xll_lifecycle.cpp). Read in NotifyUpdate as a
// defense-in-depth guard (review MED-1, 2026-06-17): the in-flight-snapshot
// window is closed by BOTH the m_callback null-out (in ServerTerminate, before
// JoinWorker) AND this flag, so a future edit that reorders the null-out cannot
// re-open a NotifyUpdate-vs-teardown race.
namespace xll { extern std::atomic<bool> g_isUnloading; }
// Close-time ghost fix (AGENTS.md §23.6 Stage 4, remediation 2026-06-17):
// RtdServer::ServerTerminate is the correctly-timed, COM-apartment-safe,
// naturally-serialized point at which to run the DEFERRED destructive teardown.
// Excel calls ServerTerminate ON THE STA, AFTER all DisconnectData, once its RTD
// handshake against a still-live g_phost completes. ServerTerminate releases
// m_callback (its normal job) and then drives the destructive teardown directly:
//   - SetRtdServerTerminated(): records handshake completion (diagnosability /
//     idempotence). Defined in xll_lifecycle.cpp.
//   - RunDestructiveTeardown(): the §23.0-ordered destructive body (set
//     g_isUnloading, StopWorker/JoinWorker, drains, delete g_phost, reap server),
//     self-guarded by an internal CAS so it runs exactly once. Defined in
//     xll_lifecycle.cpp; safe to call here (this is the STA, not the loader lock).
namespace xll { void SetRtdServerTerminated(); void RunDestructiveTeardown(); }
// §23.6 host-shutdown teardown gate (remediation 2026-06-18). Excel calls
// ServerTerminate WHENEVER the RTD server's live topic count drops to zero — NOT
// only at host shutdown. On an ordinary workbook close (Application stays alive)
// the destructive teardown must be SKIPPED, or it kills the Go server mid-session
// and the next reopen hits a dead server (RPC 0x800706BA / AV). This accessor is
// true ONLY when GracefulTeardownOnce armed it from its confirmed host-shutdown
// Phase-1 branch. Defined in xll_lifecycle.cpp.
namespace xll { bool HostShutdownTeardownArmed(); }

namespace rtd {

    /**
     * @brief Base class for implementing an RTD Server.
     * Handles IUnknown, IDispatch (Stub), topic management, and batch update logic.
     */
    class RtdServerBase : public IRtdServer {
    private:
        long m_refCount;

    protected:
        IRTDUpdateEvent* m_callback;
        std::mutex m_callbackMutex; // Protects m_callback

        // Topic Management
        std::map<long, VARIANT> m_topicData;
        std::vector<long> m_dirtyTopics;
        std::mutex m_topicMutex; // Protects m_topicData and m_dirtyTopics


    public:
        RtdServerBase() : m_refCount(1), m_callback(nullptr) {
            xll::LogDebug("RtdServerBase constructor");
            GlobalModule::Lock();
        }
        virtual ~RtdServerBase() {
            xll::LogDebug("RtdServerBase destructor");
            // No need to lock mutexes; object is being destroyed (RefCount=0)
            if (m_callback) m_callback->Release();

            // Clean up stored VARIANTs
            for (auto const& [topicId, value] : m_topicData) {
                VariantClear((VARIANT*)&value);
            }
            m_topicData.clear();
            GlobalModule::Unlock();
        }

        // --- IUnknown ---
        HRESULT __stdcall QueryInterface(REFIID riid, void** ppv) override {
            if (!ppv) return E_POINTER;
            *ppv = nullptr;

            // Check for IUnknown, IDispatch, or our specific IRtdServer IID
            // Note: IID_IRtdServer is defined in defs.h and matches {EC0E6191-DB51-11D3-8F3E-00C04F3651B8}
            if (IsEqualGUID(riid, IID_IUnknown) || IsEqualGUID(riid, IID_IDispatch) || IsEqualGUID(riid, IID_IRtdServer)) {
                *ppv = static_cast<IRtdServer*>(this);
                AddRef();
                return S_OK;
            }
            return E_NOINTERFACE;
        }

        ULONG __stdcall AddRef() override {
            return InterlockedIncrement(&m_refCount);
        }

        ULONG __stdcall Release() override {
            ULONG res = InterlockedDecrement(&m_refCount);
            if (res == 0) delete this;
            return res;
        }

        // --- IDispatch ---
        HRESULT __stdcall GetTypeInfoCount(UINT* pctinfo) override {
            if (!pctinfo) return E_POINTER;
            *pctinfo = 0;
            return S_OK;
        }
        HRESULT __stdcall GetTypeInfo(UINT, LCID, ITypeInfo**) override { return E_NOTIMPL; }
        HRESULT __stdcall GetIDsOfNames(REFIID, LPOLESTR* rgszNames, UINT cNames, LCID, DISPID* rgDispId) override {
            if (!rgDispId || !rgszNames || cNames == 0) return E_POINTER;
            std::wstring name(rgszNames[0]);
            if (name == L"ServerStart") *rgDispId = 10;
            else if (name == L"ConnectData") *rgDispId = 11;
            else if (name == L"RefreshData") *rgDispId = 12;
            else if (name == L"DisconnectData") *rgDispId = 13;
            else if (name == L"Heartbeat") *rgDispId = 14;
            else if (name == L"ServerTerminate") *rgDispId = 15;
            else return DISP_E_UNKNOWNNAME;
            return S_OK;
        }

        HRESULT __stdcall Invoke(DISPID dispIdMember, REFIID, LCID, WORD, DISPPARAMS* pDispParams, VARIANT* pVarResult, EXCEPINFO*, UINT*) override {
            xll::LogDebug("RtdServer::Invoke DISPID: " + std::to_string(dispIdMember) + " Args: " + std::to_string(pDispParams->cArgs));
            
            switch (dispIdMember) {
            case 10: // ServerStart(Callback, pfRes)
                if (pDispParams->cArgs < 2) return DISP_E_BADPARAMCOUNT;
                // Args: [1]Callback, [0]pfRes (Reverse order)
                return ServerStart((IRTDUpdateEvent*)pDispParams->rgvarg[1].punkVal, pDispParams->rgvarg[0].plVal);

            case 11: // ConnectData(TopicID, Strings, GetNewValues, pvarOut)
                if (pDispParams->cArgs < 4) return DISP_E_BADPARAMCOUNT;
                // Args: [3]TopicID, [2]Strings, [1]GetNewValues, [0]pvarOut
                return ConnectData(pDispParams->rgvarg[3].lVal, (SAFEARRAY**)pDispParams->rgvarg[2].pparray, pDispParams->rgvarg[1].pboolVal, pDispParams->rgvarg[0].pvarVal);

            case 12: // RefreshData(TopicCount, parrayOut)
                if (pDispParams->cArgs < 2) return DISP_E_BADPARAMCOUNT;
                // Args: [1]TopicCount, [0]parrayOut
                return RefreshData(pDispParams->rgvarg[1].plVal, (SAFEARRAY**)pDispParams->rgvarg[0].pparray);

            case 13: // DisconnectData(TopicID)
                if (pDispParams->cArgs < 1) return DISP_E_BADPARAMCOUNT;
                return DisconnectData(pDispParams->rgvarg[0].lVal);

            case 14: // Heartbeat(pfRes)
                if (pDispParams->cArgs < 1) return DISP_E_BADPARAMCOUNT;
                return Heartbeat(pDispParams->rgvarg[0].plVal);

            case 15: // ServerTerminate()
                return ServerTerminate();

            default:
                return DISP_E_MEMBERNOTFOUND;
            }
        }

        // --- IRtdServer Default Implementations ---
        HRESULT __stdcall ServerStart(IRTDUpdateEvent* Callback, long* pfRes) override {
            xll::LogDebug("RtdServer::ServerStart");
            if (!pfRes) return E_POINTER;
            std::lock_guard<std::mutex> lock(m_callbackMutex);
            if (m_callback) m_callback->Release();
            m_callback = Callback;
            if (m_callback) m_callback->AddRef();
            *pfRes = 1;
            return S_OK;
        }

        HRESULT __stdcall ServerTerminate() override {
            xll::LogDebug("RtdServer::ServerTerminate");
            // Record that Excel has completed its RTD teardown handshake (AGENTS.md
            // §23.6 Stage 4). Plain atomic store; safe + idempotent.
            xll::SetRtdServerTerminated();

            // Release OUR ref to Excel's IRTDUpdateEvent callback — ServerTerminate's
            // normal job, performed here ON THE STA in the correct COM apartment.
            // Scope the mutex so it is NOT held across the destructive teardown below
            // (teardown does not need m_callbackMutex; holding it would needlessly
            // serialize against any stray STA ServerStart, and the drains can block).
            {
                std::lock_guard<std::mutex> lock(m_callbackMutex);
                if (m_callback) {
                    m_callback->Release();
                    m_callback = nullptr;
                }
            }

            // §23.6 Stage-4 remediation (2026-06-17): on a CONFIRMED host shutdown,
            // drive the DEFERRED destructive teardown HERE, on the STA, at the
            // correctly-timed point — Excel has now delivered every DisconnectData and
            // this ServerTerminate AFTER they all completed against a still-live
            // g_phost, so MSG_RTD_DISCONNECT reached the server and Excel's RTD
            // machinery considers itself torn down. This is the SAME thread-class
            // (STA) and SAME blocking profile the original synchronous teardown had
            // inside OnBeginShutdown — just correctly timed. RunDestructiveTeardown
            // self-guards with its g_destructiveDone CAS, so it runs exactly once.
            //
            // §23.6 GATE (remediation 2026-06-18): ServerTerminate fires not ONLY at
            // host shutdown but ALSO whenever the live RTD topic count drops to zero —
            // including on an ordinary workbook close while the Excel Application stays
            // alive (e.g. a COM-automation client holds the Application ref, so
            // OnBeginShutdown / GracefulTeardownOnce never fire). On that NON-shutdown
            // close, running the destructive teardown would set g_isUnloading,
            // Stop/Join the worker, delete g_phost, and CloseHandle(hJob)
            // (KILL_ON_JOB_CLOSE) — killing the Go server while the XLL is still loaded
            // and Excel is NOT quitting; the next reopen would hit a dead server / null
            // g_phost → RPC 0x800706BA → AV. So we run RunDestructiveTeardown ONLY when
            // xll::HostShutdownTeardownArmed() is true (armed by GracefulTeardownOnce's
            // confirmed host-shutdown Phase-1 branch). Otherwise this is a zero-topic
            // blip on a live host: we have already released m_callback above (the
            // correct, normal ServerTerminate behavior), and we return S_OK WITHOUT
            // destroying g_phost / the server, leaving the add-in fully usable so a
            // later workbook reopen re-subscribes against a live server.
            // NOTE: do NOT separately call ReleaseCallbackForTeardown here — we already
            // released m_callback above on the STA; RunDestructiveTeardown's own
            // (idempotent, null-checked) ReleaseCallbackForTeardown is the
            // belt-and-suspenders cover on the armed path.
            if (xll::HostShutdownTeardownArmed()) {
                xll::RunDestructiveTeardown();
            } else {
                xll::LogDebug("RtdServer::ServerTerminate: host-shutdown teardown NOT "
                              "armed (ordinary workbook-close / zero-topic termination) "
                              "— releasing m_callback only, leaving g_phost and the "
                              "server intact (AGENTS.md §23.6).");
            }
            return S_OK;
        }

        /**
         * @brief Proactively release OUR reference to Excel's IRTDUpdateEvent
         *        callback on the CONFIRMED-teardown path, breaking the COM
         *        reference cycle (Excel <-> this RtdServer).
         *
         * Belt-and-suspenders for the §23.6 Stage-4 deferred teardown. On a normal
         * host shutdown ServerTerminate now arrives (the deferred Phase-1/Phase-2
         * split keeps g_phost alive across Excel's RTD handshake) and releases
         * m_callback itself; this method is the cover for the timeout path (the
         * Phase-2 watcher ran without seeing ServerTerminate) and for the
         * add-in-disable path. It mirrors ServerTerminate's release under
         * m_callbackMutex and is fully idempotent (null-checked), so it is SAFE if
         * ServerTerminate also ran — no double-release / double-free.
         *
         * CONCURRENCY: this must be called AFTER the worker thread that drives
         * NotifyUpdate()/ProcessRtdUpdate() has been joined (see
         * xll::GracefulTeardownOnce -> JoinWorker), so no in-flight UpdateNotify
         * can race the release. The mutex additionally serializes against any STA
         * ServerStart/ServerTerminate. It does NOT delete the instance itself
         * (Excel still owns the IRtdServer ref; CoRevokeClassObject handles the
         * factory) — it only drops OUR ref to Excel's callback.
         */
        void ReleaseCallbackForTeardown() {
            std::lock_guard<std::mutex> lock(m_callbackMutex);
            if (m_callback) {
                m_callback->Release();
                m_callback = nullptr;
                xll::LogDebug("RtdServer::ReleaseCallbackForTeardown: m_callback released");
            } else {
                xll::LogDebug("RtdServer::ReleaseCallbackForTeardown: no callback (already released)");
            }
        }

        /**
         * @brief Thread-safe helper to notify Excel of updates.
         */
        void NotifyUpdate() {
            // Defense-in-depth (review MED-1): bail if teardown has begun. The
            // m_callback null-out already covers the race, but this closes the
            // snapshot window against future reorderings too.
            if (xll::g_isUnloading.load(std::memory_order_acquire)) return;
            IRTDUpdateEvent* tempCallback = nullptr;
            {
                std::lock_guard<std::mutex> lock(m_callbackMutex);
                if (m_callback) {
                    tempCallback = m_callback;
                    tempCallback->AddRef();
                }
            }
            if (tempCallback) {
                xll::LogDebug("RtdServer::NotifyUpdate calling UpdateNotify");
                tempCallback->UpdateNotify();
                tempCallback->Release();
            } else {
                xll::LogDebug("RtdServer::NotifyUpdate skipped (no callback)");
            }
        }

        /**
         * @brief Helper to create the standard 2D SafeArray for RefreshData.
         * The array is [topicCount][2].
         * Dimension 1: [0]=TopicID, [1]=Value (Fastest changing)
         * Dimension 2: Topic Index (0 to topicCount-1)
         */
        static HRESULT CreateRefreshDataArray(long topicCount, SAFEARRAY** ppArray) {
            if (!ppArray) return E_POINTER;
            if (topicCount < 0) return E_INVALIDARG;

            if (topicCount == 0) {
                 *ppArray = nullptr;
                 return S_OK;
            }

            SAFEARRAYBOUND bounds[2];
            // bounds[0] is the right-most (fastest changing) dimension.
            // bounds[1] is the left-most dimension.

            // Dimension 1 (Fastest): Row index (0=TopicID, 1=Value)
            bounds[0].cElements = 2;
            bounds[0].lLbound = 0;

            // Dimension 2 (Slowest): Column index (Topic Index)
            bounds[1].cElements = topicCount;
            bounds[1].lLbound = 0;

            *ppArray = SafeArrayCreate(VT_VARIANT, 2, bounds);
            if (!*ppArray) return E_OUTOFMEMORY;
            return S_OK;
        }

        HRESULT __stdcall DisconnectData(long TopicID) override {
            std::lock_guard<std::mutex> lock(m_topicMutex);
            auto it = m_topicData.find(TopicID);
            if (it != m_topicData.end()) {
                VariantClear(&(it->second));
                m_topicData.erase(it);
            }
            return S_OK;
        }

        HRESULT __stdcall RefreshData(long* TopicCount, SAFEARRAY** parrayOut) override {
            xll::LogDebug("RtdServer::RefreshData entry");
            if (!TopicCount || !parrayOut) return E_POINTER;

            std::vector<long> dirtyTopics;
            std::vector<VARIANT> topicValues;

            {
                std::lock_guard<std::mutex> lock(m_topicMutex);
                if (m_dirtyTopics.empty()) {
                    xll::LogDebug("RtdServer::RefreshData: No dirty topics");
                    *TopicCount = 0;
                    *parrayOut = nullptr;
                    return S_OK;
                }
                dirtyTopics.swap(m_dirtyTopics);

                topicValues.reserve(dirtyTopics.size());
                for (long topicId : dirtyTopics) {
                    VARIANT value;
                    VariantInit(&value);
                    auto it = m_topicData.find(topicId);
                    if (it != m_topicData.end()) {
                        VariantCopy(&value, &it->second);
                    } else {
                        value.vt = VT_ERROR;
                        value.scode = 2043; // xlErrGettingData
                    }
                    topicValues.push_back(value);
                }
            }

            long count = static_cast<long>(dirtyTopics.size());
            xll::LogDebug("RtdServer::RefreshData: Updating " + std::to_string(count) + " topics");
            SAFEARRAY* psa = nullptr;
            HRESULT hr = CreateRefreshDataArray(count, &psa);
            if (FAILED(hr)) {
                for(auto& v : topicValues) VariantClear(&v);
                return hr;
            }

            for (long i = 0; i < count; ++i) {
                long indices[2];
                // indices[0] refers to bounds[0] (Fastest changing: Row)
                // indices[1] refers to bounds[1] (Slowest changing: Topic Index)

                indices[0] = 0; // Row 0: TopicID
                indices[1] = i; // Column i
                VARIANT vTopicId;
                vTopicId.vt = VT_I4;
                vTopicId.lVal = dirtyTopics[i];
                SafeArrayPutElement(psa, indices, &vTopicId);

                indices[0] = 1; // Row 1: Value
                indices[1] = i; // Column i
                SafeArrayPutElement(psa, indices, &topicValues[i]);

                if (topicValues[i].vt == VT_BSTR) {
                    xll::LogDebug("RTD: RefreshData Topic " + std::to_string(dirtyTopics[i]) + " = " + WideToUtf8(topicValues[i].bstrVal));
                } else {
                    xll::LogDebug("RTD: RefreshData Topic " + std::to_string(dirtyTopics[i]) + " (type " + std::to_string(topicValues[i].vt) + ")");
                }
            }

            for(auto& v : topicValues) VariantClear(&v);

            *TopicCount = count;
            *parrayOut = psa;
            xll::LogDebug("RtdServer::RefreshData success");
            return S_OK;
        }

        HRESULT __stdcall Heartbeat(long* pfRes) override {
            if (!pfRes) return E_POINTER;
            *pfRes = 1;
            return S_OK;
        }

    public: // --- Topic Management ---

        /**
         * @brief Updates the value for a given topic and marks it for refresh.
         * @param topicId The ID of the topic to update.
         * @param value The new value for the topic.
         * @return HRESULT S_OK on success.
         */
        HRESULT UpdateTopic(long topicId, const VARIANT& value) {
            std::lock_guard<std::mutex> lock(m_topicMutex);

            // Store the new value
            auto it = m_topicData.find(topicId);
            if (it == m_topicData.end()) {
                // This can happen if Update is called before ConnectData completes.
                // For simplicity, we'll allow it.
                m_topicData[topicId];
                it = m_topicData.find(topicId);
                VariantInit(&it->second);
            }
             VariantCopy(&it->second, const_cast<VARIANT*>(&value));

            // Mark as dirty (if not already)
            if (std::find(m_dirtyTopics.begin(), m_dirtyTopics.end(), topicId) == m_dirtyTopics.end()) {
                m_dirtyTopics.push_back(topicId);
            }
            return S_OK;
        }

        // User must implement:
        // ConnectData
        virtual HRESULT __stdcall ConnectData(long TopicID, SAFEARRAY** Strings, VARIANT_BOOL* GetNewValues, VARIANT* pvarOut) override = 0;
    };

} // namespace rtd

#endif // RTD_SERVER_H
