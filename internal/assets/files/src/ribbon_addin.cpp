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

    // Depth counter, not a bool: Office may in principle nest its own
    // disconnect, and an RAII guard that stored/restored a bool could clear the
    // flag while an outer OnDisconnection is still on the stack. Defined
    // unconditionally (this TU is compiled in non-ribbon builds too, for
    // WaitForCommandDrain); only OnDisconnection ever increments it, so in a
    // non-ribbon build it is permanently 0 and the accessor is a constant false.
    // See the header for the crash this exists to prevent.
    //
    // NOT in ResetLifecycleStateForFreshLoad's reset set, deliberately (review LOW #3).
    // It has internal linkage in this TU, and the exposure is what matters: the only
    // way it could latch is an async SEH fault unwinding past DisconnectDepthGuard's
    // destructor, and OnDisconnection is OUTSIDE XLL_SAFE_BLOCK while a fault below it
    // is caught by GracefulTeardownOnce's own __except in ITS frame — so the guard's
    // destructor still runs. And the LEAK DIRECTION IS FAIL-SAFE: a latched depth means
    // "always skip the explicit disconnect", which is the correct answer on the add-in-
    // disable path and harmless on a host shutdown (the §20.2.1 PIN already prevents the
    // unmap the disconnect used to be credited with). Same disposition as the s_inHook
    // residual documented in AGENTS.md §20.3.
    static std::atomic<int> g_officeDisconnectDepth{0};

    bool OfficeDisconnectInProgress() {
        return g_officeDisconnectDepth.load(std::memory_order_acquire) > 0;
    }

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
        // ENTRY GATE, before the thread exists (review MED #2). CommandInFlightGuard is
        // acquired INSIDE the detached lambda, so a command dispatched after Phase 1's
        // drain completed would not be counted by it and could still be inside a Send
        // when Phase 2 deletes g_phost. A teardown means the session is going away, so
        // dropping a ribbon click here costs nothing. The in-lambda re-checks remain the
        // cover for a teardown that starts mid-flight.
        if (xll::TeardownStarted()) {
            // POST-TEARDOWN USE (AGENTS.md §20.3): a ribbon click after Phase 2
            // COMPLETED means the add-in was destroyed and the session went on
            // living — the commonest way in is the user unticking this add-in in the
            // COM Add-ins dialog, which runs the full destructive teardown while the
            // XLL stays loaded and the ribbon tab stays on screen. Gated on Phase 2's
            // own marks (g_isUnloading latched AND g_phost null), not on
            // TeardownStarted(), which is also true during a genuine quit's Phase 1.
            // Reported from HERE, the STA, never from the detached lambda below:
            // LogTeardownWarn must not be called from a detached thread (§20.2.1).
            if (xll::g_isUnloading.load(std::memory_order_acquire) && !g_phost) {
                xll::ReportPostTeardownUse("SendCommandInvoke");
            }
            return;
        }
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
                if (xll::TeardownStarted()) return; // quiesce OR unload — see xll_lifecycle.h
                if (!g_phost) return;

                auto slot = g_phost->GetZeroCopySlot();

                if (xll::TeardownStarted()) return; // quiesce OR unload — see xll_lifecycle.h
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

                // The request lives in the slot's fixed-size arena. An
                // over-capacity build is served from the heap and latched (see
                // SHMAllocator) — those bytes are not in shared memory, so
                // sending would ship garbage. Unreachable for a command name
                // in practice; the check is what keeps that true.
                if (allocator.Overflowed()) {
                    xll::LogWarn("CommandInvoke dropped (request exceeds the SHM slot): " + commandNameUtf8);
                    return;
                }

                if (xll::TeardownStarted()) return; // quiesce OR unload — see xll_lifecycle.h

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

            if (!xll::TeardownStarted()) {
                xll::LogWarn("CommandInvoke dropped (server not reachable after retries): " + commandNameUtf8);
            }
        }).detach();
    }

}} // namespace xll::ribbon

#ifdef XLL_RIBBON_ENABLED
#include "com/ribbon_image.h"
// ConnectContextPublished() — OnConnection's readiness refusal. Included INSIDE
// the gate: com/ribbon_connect.h #errors without XLL_RIBBON_ENABLED (its
// definitions are compiled only for ribbon builds), and everything above this
// point is compiled in non-ribbon builds too (WaitForCommandDrain et al.).
#include "com/ribbon_connect.h"

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
    // Late-bound extensibility members (see kExtNames). Four of the five are
    // no-ops returning S_OK — but OnConnection is NOT a no-op any more (it refuses
    // an activation xlAutoOpen never prepared), so it is routed to the real body.
    // A blanket S_OK here would let a host that late-binds _IDTExtensibility2
    // through IDispatch bypass that refusal entirely and get the half add-in the
    // guard exists to prevent.
    if (dispIdMember == kDispIdExtBase) {
        return OnConnection(nullptr, ext_cm_AfterStartup, nullptr, nullptr);
    }
    if (dispIdMember > kDispIdExtBase && dispIdMember < kDispIdExtBase + 5) return S_OK;

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

// REFUSES a COM activation that xlAutoOpen never prepared (backlog line 120,
// 2026-08-03).
//
// This used to be a bare `return S_OK`, which was fine while the graceful teardown
// deleted HKCU\…\Excel\Addins\<progId> and the ribbon's HKCU COM registration on
// every confirmed shutdown: nothing could reach us that xlAutoOpen had not just
// wired. That deletion was itself the defect — it removed the COM Add-ins dialog
// row on an add-in DISABLE, so a user who unticked the box could not tick it back
// — and it is gone. The row and a live InprocServer32 now survive the session,
// which means Office can COM-activate this class in a session where the XLL was
// NEVER LOADED: no ribbon XML, no images, no SHM host, no Go server, no
// ConnectContext.
//
// TWO ROUTES GET HERE, and the second is why this guard cannot be a "can't
// happen" assert. On a FRESH install LoadBehavior is 0 — RegisterOfficeAddinKey
// writes it that way and nothing autoloads at Excel startup — so the only route
// in is a manual tick in the COM Add-ins dialog. But a tick is recorded by Office
// as LoadBehavior=3, and RegisterOfficeAddinKey PRESERVES an existing value
// (2026-08-03: it used to overwrite it with 0 on every xlAutoOpen, silently
// undoing the user's tick). From that tick onwards Office may therefore
// COM-activate this class during its OWN startup, with no ordering guarantee
// against the XLL's xlAutoOpen — so the refusal below is a NORMAL, expected
// outcome on that path, not a corner case. Office's answer to it is its own
// (typically demoting LoadBehavior to 2); ours is to refuse cleanly and say why.
//
// A half-initialised add-in that reports success is worse than none: it shows a
// ribbon tab whose every button silently does nothing. E_FAIL is a refusal Office
// understands and surfaces.
//
// WHERE THE WARN LINE ACTUALLY GOES (corrected 2026-08-03). The original comment
// here claimed "the log line is what makes it diagnosable". That was FALSE for
// this guard: the native log file is opened by InitNativeLogging inside
// xlAutoOpen, and the whole premise of this branch is that xlAutoOpen never ran —
// so g_logPath was empty and WriteLogUnconditional dropped the line silently,
// precisely in the state the guard covers. The message survives now because that
// function falls back to OutputDebugStringW while there is no log file (see "THE
// NO-LOG-FILE SINK" in src/xll_log.cpp); read it with a debug-output viewer, NOT
// in <proj>_native.log, which in this session does not exist. Do not "fix" this
// by pointing a user at the log file, and do not remove that fallback.
//
// Still SAFE_LOG_WARN and not LogTeardownWarn: this is not a teardown path, and
// g_isUnloading is false in the case that gets here.
HRESULT __stdcall RibbonAddIn::OnConnection(IDispatch*, ext_ConnectMode, IDispatch*, SAFEARRAY**) {
    if (!xll::ribbon::ConnectContextPublished()) {
        SAFE_LOG_WARN("Ribbon: OnConnection REFUSED — xlAutoOpen never published the connect context in "
                      "this process, so there is no ribbon XML, host or server to serve. This happens when "
                      "the COM Add-ins dialog row is ticked in a session where the XLL itself was not "
                      "loaded; load the add-in (Excel: Add-ins ▸ Browse to the .xll) instead.");
        return E_FAIL;
    }
    return S_OK;
}

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

    // Mark "Office is inside its own add-in disconnect" for the whole duration of
    // the teardown this call drives. The generated teardown hook reads it and
    // SKIPS its explicit `COMAddIns.Item(progId).Connect = false`, which from here
    // would re-enter the put_Connect already on this stack and leave Office
    // Release()ing an interface pointer its own nested call had just cleared
    // (EXCEL.EXE 0xC0000005 at mso.dll+0xa1d19e; see
    // xll::ribbon::OfficeDisconnectInProgress in com/ribbon_addin.h).
    //
    // Scoped with RAII so it is cleared on normal AND exception unwind. The
    // counter is decremented even if GracefulTeardownOnce is a CAS no-op, which
    // is correct: the flag describes OFFICE's stack, not our teardown's progress.
    struct DisconnectDepthGuard {
        DisconnectDepthGuard() { xll::ribbon::g_officeDisconnectDepth.fetch_add(1, std::memory_order_acq_rel); }
        ~DisconnectDepthGuard() { xll::ribbon::g_officeDisconnectDepth.fetch_sub(1, std::memory_order_acq_rel); }
    } disconnectDepth;

    xll::GracefulTeardownOnce(isHostShutdown);
    return S_OK;
}

HRESULT __stdcall RibbonAddIn::OnAddInsUpdate(SAFEARRAY**) { return S_OK; }
HRESULT __stdcall RibbonAddIn::OnStartupComplete(SAFEARRAY**) { return S_OK; }

HRESULT __stdcall RibbonAddIn::OnBeginShutdown(SAFEARRAY**) {
    // Shutdown signal: fires AFTER the Save/Cancel decision is resolved and NEVER
    // on a cancelled quit (design §3.4). This is the graceful pre-teardown moment
    // — it runs on the STA thread (COM/C++-safe, not the loader lock), so the
    // §23.0 drains and the clean server shutdown happen here rather than at
    // DETACH. Idempotent via the GracefulTeardownOnce CAS. Decoupled via the
    // exported lifecycle hook. Never an add-in disable, so it is unambiguously a
    // HOST shutdown as far as the §23.6 RTD revoke-skip is concerned — hence
    // isHostShutdown=true.
    //
    // IT DOES NOT GUARANTEE THE PROCESS IS GOING AWAY, and this comment used to
    // claim it did ("fires only on a REAL quit"). Measured 2026-08-03, 3 of 3:
    // an external COM client that holds an Application reference and calls
    // Application.Quit() gets this callback delivered, the destructive teardown
    // runs, the Go server is reaped and the add-in stops answering — and
    // EXCEL.EXE keeps running with the session alive. A UDF that returned 5 a
    // second earlier then fails.
    //
    // Left as-is deliberately (decision 2026-08-03). The blast radius is COM
    // automation clients, which is essentially our own harnesses: a user closing
    // Excel by the X button or File > Exit is unaffected, verified repeatedly by
    // ghost-check. Against that, every alternative — deferring the destructive
    // phase to DETACH, or detecting the survival afterwards and re-initialising —
    // rewrites the ordering that v0.8.41 validated at 0/40 in the code region
    // that produced TWO separate 100%-reproducible Excel crashes (v0.8.41
    // use-after-unload, v0.8.42 put_Connect re-entrancy). Not a trade worth
    // making for a scenario no end user reaches.
    //
    // If you are here because an automation client saw a dead add-in: release the
    // Application reference BEFORE calling Quit, or close the window instead.
    // AGENTS.md §20.3 owns the full signal-set caveat.
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
