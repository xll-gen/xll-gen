#pragma once
#include <windows.h>
#include <ole2.h>
#include <string>
#include <vector>

namespace xll { namespace ribbon {
    // Both set once from xlAutoOpen (generated code) before the COM add-in
    // connects / before any command proc can run.
    void SetRibbonXml(const wchar_t* xml);
    void SetCommands(std::vector<std::wstring> commandNames);

    // Fire-and-forget dispatch to the Go server (MSG_COMMAND_INVOKE).
    // Returns immediately; never blocks Excel's STA thread on the handler.
    void SendCommandInvoke(const std::string& commandNameUtf8, const std::string& controlIdUtf8);

    // Drains in-flight SendCommandInvoke threads on a graceful close (AGENTS.md
    // §18.11 / §20.3 teardown contract). Called once, from
    // xll::GracefulTeardownOnce() (xll_lifecycle.cpp) AFTER it sets
    // g_isUnloading=true and BEFORE `delete g_phost`. That ordering is what
    // closes the UAF window: each command thread re-checks g_isUnloading between
    // its <=200 ms per-attempt Sends, so it exits within ~one attempt (<~350 ms
    // incl. the WaitEvent quantum), well inside the 2000 ms cap. (Pre-2026-06-13
    // this was also called eagerly from the generated xlAutoClose; that early
    // drain was removed with the cancel-quit fix — xlAutoClose is now
    // non-destructive, so there is no drain there to abort a mid-retry thread.)
    // Returns false on timeout (logged, non-fatal).
    bool WaitForCommandDrain(unsigned int timeoutMs);

    // True while OFFICE is inside its OWN add-in disconnect for this add-in —
    // i.e. while RibbonAddIn::OnDisconnection is on this thread's stack, called
    // from mso.dll's COMAddIn::put_Connect(false) / host-shutdown add-in teardown.
    //
    // WHY IT EXISTS (measured 2026-07-30, present in v0.8.40 AND v0.8.41 alike):
    // the generated teardown hook's first step is an explicit
    // `Application.COMAddIns.Item(progId).Connect = false`. Reached from
    // OnDisconnection, that is a RE-ENTRANT call into the very put_Connect that
    // is already running: the nested call completes Office's disconnect and
    // CLEARS the interface pointers Office caches on its COMAddIn object, and
    // when the OUTER put_Connect resumes it Release()s one of them
    // UNCONDITIONALLY — reading a NULL vtable. Result: EXCEL.EXE 0xC0000005 at
    // mso.dll+0xa1d19e, 100% of the runs (3/3) in which the nested disconnect
    // actually executed, 0% of the runs in which it did not. Office is ALREADY
    // disconnecting us on this path, so the explicit disconnect is redundant
    // there; the hook must skip it. It is still required when the teardown is
    // entered from OnBeginShutdown: Excel has not started the add-in disconnect yet,
    // and the explicit disconnect is what makes Excel release its RibbonAddIn
    // reference EARLY (the original reason the call exists).
    //
    // WHAT IT IS *NOT* (corrected 2026-07-30): it is NOT what keeps Excel from holding
    // a vtable pointer into this DLL past FreeLibrary. On a CONFIRMED host shutdown
    // Phase 1 PINS the image (AGENTS.md §20.2.1 rule 1) BEFORE it invokes this hook, so
    // there is no hole for a stale pointer to fall into. This call is hygiene — release
    // early, do not leave Office holding a reference longer than necessary — and must
    // never be cited as a reason to weaken or remove the pin.
    bool OfficeDisconnectInProgress();
}} // namespace xll::ribbon

#ifdef XLL_RIBBON_ENABLED
#include "com/extensibility.h"

// NOTE (COM identity): both bases derive from IDispatch, so this object has
// two IDispatch vtables; QI(IID_IDispatch) always returns the
// IDTExtensibility2 one, and both route to the same final overrides below.
// Do not add further IDispatch-derived bases without revisiting QI.
//
// RibbonAddIn is the COM add-in helper class hosted by the XLL itself.
// Excel loads it through DllGetClassObject (in-memory class object first via
// CoRegisterClassObject), QIs IRibbonExtensibility for GetCustomUI, and
// delivers onAction callbacks through IDispatch::Invoke.
class RibbonAddIn : public IDTExtensibility2, public IRibbonExtensibility {
    long m_refCount;
public:
    RibbonAddIn();
    virtual ~RibbonAddIn();

    // IUnknown
    HRESULT __stdcall QueryInterface(REFIID riid, void** ppv) override;
    ULONG __stdcall AddRef() override;
    ULONG __stdcall Release() override;

    // IDispatch — only ribbon callbacks are late-bound; extensibility methods
    // are reached via vtable.
    HRESULT __stdcall GetTypeInfoCount(UINT* pctinfo) override;
    HRESULT __stdcall GetTypeInfo(UINT, LCID, ITypeInfo**) override;
    HRESULT __stdcall GetIDsOfNames(REFIID, LPOLESTR* rgszNames, UINT cNames, LCID, DISPID* rgDispId) override;
    HRESULT __stdcall Invoke(DISPID dispIdMember, REFIID, LCID, WORD, DISPPARAMS* pDispParams,
                             VARIANT*, EXCEPINFO*, UINT*) override;

    // IDTExtensibility2
    HRESULT __stdcall OnConnection(IDispatch* Application, ext_ConnectMode ConnectMode, IDispatch* AddInInst, SAFEARRAY** custom) override;
    HRESULT __stdcall OnDisconnection(ext_DisconnectMode RemoveMode, SAFEARRAY** custom) override;
    HRESULT __stdcall OnAddInsUpdate(SAFEARRAY** custom) override;
    HRESULT __stdcall OnStartupComplete(SAFEARRAY** custom) override;
    HRESULT __stdcall OnBeginShutdown(SAFEARRAY** custom) override;

    // IRibbonExtensibility
    HRESULT __stdcall GetCustomUI(BSTR RibbonID, BSTR* RibbonXml) override;
};
#endif // XLL_RIBBON_ENABLED
