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
        // Detached fire-and-forget thread; mirrors xll_rtd.cpp::ConnectData.
        // Re-checks xll::g_isUnloading at every yield point so that on a graceful
        // close WaitForCommandDrain() can drain in-flight threads before g_phost
        // teardown, and on a forced unload the thread bails before touching SHM.
        std::thread([commandNameUtf8, controlIdUtf8]() {
            CommandInFlightGuard inflight;

            if (xll::g_isUnloading.load(std::memory_order_acquire)) return;
            if (!g_phost) return;

            auto slot = g_phost->GetZeroCopySlot();

            if (xll::g_isUnloading.load(std::memory_order_acquire)) return;

            SHMAllocator allocator(slot.GetReqBuffer(), slot.GetMaxReqSize());
            flatbuffers::FlatBufferBuilder builder(slot.GetMaxReqSize(), &allocator, false);
            auto nameOff = builder.CreateString(commandNameUtf8);
            auto ctrlOff = builder.CreateString(controlIdUtf8);
            auto req = protocol::CreateCommandInvokeRequest(builder, nameOff, ctrlOff);
            builder.Finish(req);

            if (xll::g_isUnloading.load(std::memory_order_acquire)) return;

            slot.Send(-((int)builder.GetSize()), (shm::MsgType)MSG_COMMAND_INVOKE, 5000);

            // After Send returns (success or timeout) we touch nothing on g_phost;
            // the ZeroCopySlot destructor only touches its own slot header (mirrors
            // xll_rtd.cpp).
        }).detach();
    }

}} // namespace xll::ribbon

#ifdef XLL_RIBBON_ENABLED

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
                                      VARIANT*, EXCEPINFO*, UINT*) {
    size_t idx = static_cast<size_t>(dispIdMember - kDispIdBase);
    if (dispIdMember < kDispIdBase || idx >= g_commandNames.size()) return DISP_E_MEMBERNOTFOUND;

    // onAction(IRibbonControl* control): the control arrives as VT_DISPATCH.
    std::string controlId;
    if (pDispParams && pDispParams->cArgs >= 1) {
        VARIANT& v = pDispParams->rgvarg[pDispParams->cArgs - 1]; // args reversed
        if (v.vt == VT_DISPATCH && v.pdispVal) {
            VARIANT idVar; VariantInit(&idVar);
            if (SUCCEEDED(xll::com::GetProperty(v.pdispVal, L"Id", &idVar)) && idVar.vt == VT_BSTR && idVar.bstrVal) {
                controlId = WideToUtf8(std::wstring(idVar.bstrVal, SysStringLen(idVar.bstrVal)));
            }
            VariantClear(&idVar);
        }
    }

    xll::ribbon::SendCommandInvoke(WideToUtf8(g_commandNames[idx]), controlId);
    return S_OK; // returns immediately — never wait for the Go handler (STA deadlock)
}

HRESULT __stdcall RibbonAddIn::OnConnection(IDispatch*, int, IDispatch*, SAFEARRAY**) { return S_OK; }
HRESULT __stdcall RibbonAddIn::OnDisconnection(int, SAFEARRAY**) { return S_OK; }
HRESULT __stdcall RibbonAddIn::OnAddInsUpdate(SAFEARRAY**) { return S_OK; }
HRESULT __stdcall RibbonAddIn::OnStartupComplete(SAFEARRAY**) { return S_OK; }
HRESULT __stdcall RibbonAddIn::OnBeginShutdown(SAFEARRAY**) { return S_OK; }

HRESULT __stdcall RibbonAddIn::GetCustomUI(BSTR RibbonID, BSTR* RibbonXml) {
    (void)RibbonID; // only the workbook ribbon is supported
    if (!RibbonXml) return E_POINTER;
    if (g_ribbonXml.empty()) return E_FAIL;
    *RibbonXml = SysAllocString(g_ribbonXml.c_str());
    return *RibbonXml ? S_OK : E_OUTOFMEMORY;
}

#endif // XLL_RIBBON_ENABLED
