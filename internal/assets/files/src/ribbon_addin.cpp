#include "com/ribbon_addin.h"
#include "com/dispatch_helpers.h"
#include "rtd/module.h"
#include "xll_log.h"
#include "xll_ipc.h"
#include "xll_lifecycle.h"
#include "types/utility.h"
#include "SHMAllocator.h"
#include "shm/DirectHost.h"
#include "shm/IPCUtils.h"
#include "types/protocol_generated.h"
#include <atomic>
#include <thread>
#include <chrono>

namespace {
    // Set once during xlAutoOpen (before the COM add-in connects and before
    // any command proc is invocable), read-only afterwards — no lock needed.
    std::wstring g_ribbonXml;
    std::vector<std::wstring> g_commandNames;

    // Ribbon callback DISPIDs start here; DISPID -> g_commandNames[dispid - kDispIdBase].
    constexpr DISPID kDispIdBase = 1000;

    std::atomic<int> g_commandInFlight{0};

    struct CommandInFlightGuard {
        CommandInFlightGuard() noexcept { g_commandInFlight.fetch_add(1, std::memory_order_acq_rel); }
        ~CommandInFlightGuard() noexcept { g_commandInFlight.fetch_sub(1, std::memory_order_acq_rel); }
        CommandInFlightGuard(const CommandInFlightGuard&) = delete;
        CommandInFlightGuard& operator=(const CommandInFlightGuard&) = delete;
    };
}

namespace xll { namespace ribbon {

    void SetRibbonXml(const wchar_t* xml) { g_ribbonXml = xml ? xml : L""; }
    void SetCommands(std::vector<std::wstring> commandNames) { g_commandNames = std::move(commandNames); }

    bool WaitForCommandDrain(unsigned int timeoutMs) {
        using clock = std::chrono::steady_clock;
        auto deadline = clock::now() + std::chrono::milliseconds(timeoutMs);
        while (g_commandInFlight.load(std::memory_order_acquire) > 0) {
            if (clock::now() >= deadline) return false;
            std::this_thread::sleep_for(std::chrono::milliseconds(1));
        }
        return true;
    }

    void SendCommandInvoke(const std::string& commandNameUtf8, const std::string& controlIdUtf8) {
        // Log on the calling (STA) thread, not in the detached lambda, so
        // logging never races teardown.
        xll::LogDebug("CommandInvoke dispatch: " + commandNameUtf8);

        // Detached fire-and-forget thread; mirrors xll_rtd.cpp::ConnectData.
        // Re-checks xll::g_isUnloading at every yield point so that on a graceful
        // close WaitForCommandDrain() can drain in-flight threads before g_phost
        // teardown, and on a forced unload the thread bails before touching SHM.
        std::thread([commandNameUtf8, controlIdUtf8]() {
            CommandInFlightGuard inflight;

            // First-click race: a ribbon click can land in the window between
            // the server process being launched (xlAutoOpen) and the Go guest
            // actually attaching its receive workers to the host slots. In that
            // window a host-initiated Send has no reader and times out, and —
            // because this path is fire-and-forget — the command is silently
            // dropped. The user sees "nothing happens on the first click; it
            // works after clicking another button" (the second click lands
            // after the guest has connected). The mock host solves the same
            // race with an explicit first-request retry (internal/regtest
            // testdata/mock_host.cpp). We do the same here: bounded retry with
            // a short per-attempt timeout. This runs OFF the STA thread, so the
            // retry never blocks Excel. The guest, once connected, stays
            // connected, so steady-state clicks send on the first attempt.
            //
            // Each attempt re-acquires a fresh ZeroCopySlot and rebuilds the
            // request: ZeroCopySlot::Send disowns its slot (slotIdx = -1) on a
            // timeout, so a slot object cannot be reused across attempts.
            // Each failing attempt blocks kAttemptTimeoutMs inside Send waiting
            // for a reader, so the worst-case total wait is
            // kMaxAttempts * kAttemptTimeoutMs (~10s) before the command is
            // declared undeliverable — generous cover for a slow guest cold
            // start, while each attempt's short timeout keeps the unload path
            // responsive: the g_isUnloading re-check runs between attempts, so
            // the thread exits within ~one attempt of the flag being set
            // (<~350 ms worst case incl. shm's WaitEvent quantum).
            constexpr int kMaxAttempts = 50;
            constexpr unsigned int kAttemptTimeoutMs = 200;
            for (int attempt = 0; attempt < kMaxAttempts; ++attempt) {
                if (xll::g_isUnloading.load(std::memory_order_acquire)) return;
                if (!g_phost) return;

                auto slot = g_phost->GetZeroCopySlot();

                if (xll::g_isUnloading.load(std::memory_order_acquire)) return;
                if (!slot.IsValid()) {
                    // All host slots momentarily busy; yield and retry.
                    std::this_thread::sleep_for(std::chrono::milliseconds(kAttemptTimeoutMs));
                    continue;
                }

                SHMAllocator allocator(slot.GetReqBuffer(), slot.GetMaxReqSize());
                flatbuffers::FlatBufferBuilder builder(slot.GetMaxReqSize(), &allocator, false);
                auto nameOff = builder.CreateString(commandNameUtf8);
                auto ctrlOff = builder.CreateString(controlIdUtf8);
                auto req = protocol::CreateCommandInvokeRequest(builder, nameOff, ctrlOff);
                builder.Finish(req);

                if (xll::g_isUnloading.load(std::memory_order_acquire)) return;

                auto res = slot.Send(-((int)builder.GetSize()), (shm::MsgType)MSG_COMMAND_INVOKE, kAttemptTimeoutMs);

                // After Send returns we touch nothing on g_phost; mirrors
                // xll_rtd.cpp ConnectData. The ZeroCopySlot destructor only
                // touches its own slot header.
                if (!res.HasError()) {
                    return; // delivered (ack received)
                }
                // HasError() covers BOTH a transport timeout AND a delivered-
                // but-SYSTEM_ERROR response — both retry, so delivery is
                // at-least-once (AGENTS.md §18.11 "Delivery contract": command
                // handlers must tolerate duplicates). No extra sleep — the
                // Send already blocked kAttemptTimeoutMs waiting for a reader.
            }

            if (!xll::g_isUnloading.load(std::memory_order_acquire)) {
                xll::LogWarn("CommandInvoke dropped (server not reachable after retries): " + commandNameUtf8);
            }
        }).detach();
    }

}} // namespace xll::ribbon

#ifdef XLL_RIBBON_ENABLED
#include "com/ribbon_image.h"

namespace {
    // Some hosts late-bind _IDTExtensibility2 members through IDispatch
    // instead of the vtable. Our extensibility methods are all no-ops that
    // return S_OK, so resolving their names and succeeding in Invoke is
    // exactly equivalent to a faithful vtable forward.
    constexpr DISPID kDispIdExtBase = -1005; // OnConnection..OnBeginShutdown -> -1005..-1001
    const wchar_t* const kExtNames[] = {
        L"OnConnection", L"OnDisconnection", L"OnAddInsUpdate",
        L"OnStartupComplete", L"OnBeginShutdown",
    };

    // loadImage="LoadRibbonImage" callback. Commands start at kDispIdBase
    // (1000), extensibility ids are negative — 999 cannot collide.
    constexpr DISPID kDispIdLoadImage = 999;
}

// --- RibbonAddIn ---

RibbonAddIn::RibbonAddIn() : m_refCount(1) { rtd::GlobalModule::Lock(); }
RibbonAddIn::~RibbonAddIn() { rtd::GlobalModule::Unlock(); }

HRESULT __stdcall RibbonAddIn::QueryInterface(REFIID riid, void** ppv) {
    if (!ppv) return E_POINTER;
    *ppv = nullptr;
    if (IsEqualGUID(riid, IID_IUnknown) || IsEqualGUID(riid, IID_IDispatch) ||
        IsEqualGUID(riid, IID_IDTExtensibility2)) {
        *ppv = static_cast<IDTExtensibility2*>(this);
    } else if (IsEqualGUID(riid, IID_IRibbonExtensibility)) {
        *ppv = static_cast<IRibbonExtensibility*>(this);
    } else {
        return E_NOINTERFACE;
    }
    AddRef();
    return S_OK;
}

ULONG __stdcall RibbonAddIn::AddRef() { return InterlockedIncrement(&m_refCount); }
ULONG __stdcall RibbonAddIn::Release() {
    ULONG res = InterlockedDecrement(&m_refCount);
    if (res == 0) delete this;
    return res;
}

HRESULT __stdcall RibbonAddIn::GetTypeInfoCount(UINT* pctinfo) { if (pctinfo) *pctinfo = 0; return S_OK; }
HRESULT __stdcall RibbonAddIn::GetTypeInfo(UINT, LCID, ITypeInfo** ppTInfo) {
    if (ppTInfo) *ppTInfo = nullptr;
    return E_NOTIMPL;
}

HRESULT __stdcall RibbonAddIn::GetIDsOfNames(REFIID, LPOLESTR* rgszNames, UINT cNames, LCID, DISPID* rgDispId) {
    if (cNames != 1 || !rgszNames || !rgDispId) return E_INVALIDARG;
    for (size_t i = 0; i < (sizeof(kExtNames) / sizeof(kExtNames[0])); ++i) {
        if (_wcsicmp(rgszNames[0], kExtNames[i]) == 0) {
            rgDispId[0] = kDispIdExtBase + static_cast<DISPID>(i);
            return S_OK;
        }
    }
    if (_wcsicmp(rgszNames[0], L"LoadRibbonImage") == 0) {
        rgDispId[0] = kDispIdLoadImage;
        return S_OK;
    }
    for (size_t i = 0; i < g_commandNames.size(); ++i) {
        if (_wcsicmp(rgszNames[0], g_commandNames[i].c_str()) == 0) {
            rgDispId[0] = kDispIdBase + static_cast<DISPID>(i);
            return S_OK;
        }
    }
    rgDispId[0] = DISPID_UNKNOWN;
    return DISP_E_UNKNOWNNAME;
}

HRESULT __stdcall RibbonAddIn::Invoke(DISPID dispIdMember, REFIID, LCID, WORD, DISPPARAMS* pDispParams,
                                      VARIANT* pVarResult, EXCEPINFO*, UINT*) {
    // Late-bound extensibility members (see kExtNames): all no-ops returning S_OK.
    if (dispIdMember >= kDispIdExtBase && dispIdMember < kDispIdExtBase + 5) return S_OK;

    if (dispIdMember == kDispIdLoadImage) {
        // loadImage(imageId As String) As IPictureDisp — imageId arrives as
        // VT_BSTR (args reversed); the picture goes back via pVarResult and
        // Office takes ownership of the reference.
        if (!pDispParams || pDispParams->cArgs < 1 || !pVarResult) return E_INVALIDARG;
        VARIANT& v = pDispParams->rgvarg[pDispParams->cArgs - 1];
        BSTR name = nullptr;
        if (v.vt == VT_BSTR) {
            name = v.bstrVal;
        } else if (v.vt == (VT_BSTR | VT_BYREF) && v.pbstrVal) {
            name = *v.pbstrVal;
        }
        if (!name) return E_INVALIDARG;
        IPictureDisp* pic = xll::ribbon::CreateRibbonPicture(name);
        if (!pic) {
            // Office shows a blank icon on E_FAIL; never popup, never crash.
            xll::LogWarn("Ribbon: loadImage failed for " +
                         WideToUtf8(std::wstring(name, SysStringLen(name))));
            return E_FAIL;
        }
        VariantInit(pVarResult);
        pVarResult->vt = VT_DISPATCH;
        pVarResult->pdispVal = pic;
        return S_OK;
    }

    size_t idx = static_cast<size_t>(dispIdMember - kDispIdBase);
    if (dispIdMember < kDispIdBase || idx >= g_commandNames.size()) return DISP_E_MEMBERNOTFOUND;

    // onAction(IRibbonControl* control): the control arrives as VT_DISPATCH.
    std::string controlId;
    if (pDispParams && pDispParams->cArgs >= 1) {
        VARIANT& v = pDispParams->rgvarg[pDispParams->cArgs - 1]; // args reversed
        IDispatch* ctrl = nullptr;
        if (v.vt == VT_DISPATCH) {
            ctrl = v.pdispVal;
        } else if (v.vt == (VT_DISPATCH | VT_BYREF) && v.ppdispVal) {
            ctrl = *v.ppdispVal;
        }
        if (ctrl) {
            VARIANT idVar; VariantInit(&idVar);
            if (SUCCEEDED(xll::com::GetProperty(ctrl, L"Id", &idVar)) && idVar.vt == VT_BSTR && idVar.bstrVal) {
                controlId = WideToUtf8(std::wstring(idVar.bstrVal, SysStringLen(idVar.bstrVal)));
            }
            VariantClear(&idVar);
        }
    }

    xll::ribbon::SendCommandInvoke(WideToUtf8(g_commandNames[idx]), controlId);
    return S_OK; // returns immediately — never wait for the Go handler (STA deadlock)
}

HRESULT __stdcall RibbonAddIn::OnConnection(IDispatch*, ext_ConnectMode, IDispatch*, SAFEARRAY**) { return S_OK; }

HRESULT __stdcall RibbonAddIn::OnDisconnection(ext_DisconnectMode RemoveMode, SAFEARRAY**) {
    // CONFIRMED-shutdown signal. Both modes mean a real teardown that does NOT
    // happen on a cancelled quit (design §2 / §3): ext_dm_HostShutdown = Excel
    // is closing (the cancel decision is already resolved by the time this
    // fires), ext_dm_UserClosed = the add-in was disabled while the session
    // continues. Either way the graceful teardown must run; the CAS in
    // GracefulTeardownOnce() makes it idempotent with OnBeginShutdown and the
    // DETACH backstop. Decoupled via the exported lifecycle hook.
    // isHostShutdown drives the close-time ghost fix (AGENTS.md §23.6):
    // ext_dm_HostShutdown (Excel quitting) => skip the RTD class-object revoke so
    // Excel can start its RTD DisconnectData/ServerTerminate handshake.
    // ext_dm_UserClosed (add-in disabled, session continues) => normal revoke.
    const bool isHostShutdown = (RemoveMode == ext_dm_HostShutdown);
    xll::GracefulTeardownOnce(isHostShutdown);
    return S_OK;
}

HRESULT __stdcall RibbonAddIn::OnAddInsUpdate(SAFEARRAY**) { return S_OK; }
HRESULT __stdcall RibbonAddIn::OnStartupComplete(SAFEARRAY**) { return S_OK; }

HRESULT __stdcall RibbonAddIn::OnBeginShutdown(SAFEARRAY**) {
    // CONFIRMED-shutdown signal: fires only on a REAL quit, AFTER the Save/Cancel
    // decision is resolved, and NEVER on a cancelled quit (design §3.4). This is
    // the graceful pre-teardown moment — it runs on the STA thread (COM/C++-safe,
    // not the loader lock), so the §23.0 drains and the clean server shutdown
    // happen here rather than at DETACH. Idempotent via the GracefulTeardownOnce
    // CAS. Decoupled via the exported lifecycle hook.
    //
    // OnBeginShutdown fires ONLY on a real Excel quit (never an add-in disable),
    // so this is unambiguously a HOST SHUTDOWN — pass true to trigger the §23.6
    // RTD revoke-skip that lets Excel start its RTD teardown handshake.
    xll::GracefulTeardownOnce(/*isHostShutdown=*/true);
    return S_OK;
}

HRESULT __stdcall RibbonAddIn::GetCustomUI(BSTR RibbonID, BSTR* RibbonXml) {
    (void)RibbonID; // only the workbook ribbon is supported
    if (!RibbonXml) return E_POINTER;
    if (g_ribbonXml.empty()) return E_FAIL;
    *RibbonXml = SysAllocString(g_ribbonXml.c_str());
    return *RibbonXml ? S_OK : E_OUTOFMEMORY;
}

#endif // XLL_RIBBON_ENABLED
