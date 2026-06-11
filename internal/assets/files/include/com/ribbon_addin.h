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

    // Drains in-flight SendCommandInvoke threads before g_phost teardown;
    // mirrors WaitForRtdConnectDrain. Returns false on timeout.
    bool WaitForCommandDrain(unsigned int timeoutMs);
}} // namespace xll::ribbon

#ifdef XLL_RIBBON_ENABLED
#include "com/extensibility.h"

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
    HRESULT __stdcall OnConnection(IDispatch* Application, int ConnectMode, IDispatch* AddInInst, SAFEARRAY** custom) override;
    HRESULT __stdcall OnDisconnection(int RemoveMode, SAFEARRAY** custom) override;
    HRESULT __stdcall OnAddInsUpdate(SAFEARRAY** custom) override;
    HRESULT __stdcall OnStartupComplete(SAFEARRAY** custom) override;
    HRESULT __stdcall OnBeginShutdown(SAFEARRAY** custom) override;

    // IRibbonExtensibility
    HRESULT __stdcall GetCustomUI(BSTR RibbonID, BSTR* RibbonXml) override;
};
#endif // XLL_RIBBON_ENABLED
