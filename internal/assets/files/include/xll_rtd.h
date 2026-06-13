#pragma once

#ifdef XLL_RTD_ENABLED

#include <windows.h>
#include <string>
#include <vector>
#include <map>
#include <mutex>
#include "types/protocol_generated.h"
#include "rtd/server.h"
#include "xll_log.h"
#include "types/utility.h"

// Helper function to create CLSID from string
GUID StringToGuid(const std::wstring& str);

// Global RTD State
extern class RtdServer* g_rtdServer;

void ProcessRtdUpdate(const protocol::RtdUpdate* update);

// Caches a guest->host one-shot grid result (MSG_RTD_ONCE_GRID) into
// RtdOnceGridRegistry. `buf`/`len` is the full serialized
// protocol::RtdOnceGridResult buffer; see xll_rtd.cpp for the byte contract
// Task 6's wrapper must read it back with.
void ProcessRtdOnceGrid(const uint8_t* buf, size_t len);

/**
 * @brief Wait for in-flight RTD ConnectData detached threads to drain.
 *
 * Each RtdServer::ConnectData call spawns a detached thread that publishes
 * the connect request to g_host. To avoid a use-after-free on g_phost during
 * OnAutoClose, the lifecycle owner must call this drain BEFORE `delete g_phost`.
 *
 * The drain cap (2000 ms at the call site in xll_lifecycle.cpp) is now strictly
 * larger than the in-flight Send's worst-case responsiveness: the ConnectData
 * thread sends via a bounded retry loop of <=250 ms per-attempt timeouts and
 * re-checks g_isUnloading between attempts, so it returns within ~350 ms worst
 * case of the unload flag being set (one in-flight attempt: timeout + one
 * ~100 ms WaitEvent quantum + spin) — well inside the 2000 ms cap. The single-5000 ms-Send
 * UAF window documented in AGENTS.md §23.0 is therefore closed; a drain timeout
 * is now a should-never-happen warning rather than an accepted residual race.
 *
 * @param timeoutMs Maximum time to wait (in milliseconds).
 * @return true if drained within the timeout, false on timeout (logged by the
 *         caller; with the bounded-retry ConnectData this should not occur).
 */
bool WaitForRtdConnectDrain(unsigned int timeoutMs);

// RTD Server Implementation
class RtdServer : public rtd::RtdServerBase {
public:
    RtdServer() {
        xll::LogDebug("RtdServer instance created");
        g_rtdServer = this;
    }

    virtual ~RtdServer() {
        g_rtdServer = nullptr;
    }

    // --- IUnknown ---
    virtual HRESULT __stdcall QueryInterface(REFIID riid, void** ppv) override {
        if (!ppv) return E_POINTER;
        *ppv = nullptr;

        LPOLESTR pszIID;
        StringFromIID(riid, &pszIID);
        std::string iidStr = WideToUtf8(pszIID);
        CoTaskMemFree(pszIID);

        if (IsEqualGUID(riid, rtd::IID_IRtdServer) || IsEqualGUID(riid, IID_IDispatch) || IsEqualGUID(riid, IID_IUnknown)) {
            *ppv = static_cast<rtd::IRtdServer*>(this);
            AddRef();
            xll::LogDebug("RtdServer::QueryInterface: Success for " + iidStr);
            return S_OK;
        }

        xll::LogDebug("RtdServer::QueryInterface: NoInterface for " + iidStr);
        return E_NOINTERFACE;
    }

    // --- IRtdServer Implementation ---
    // ServerStart, Heartbeat, ServerTerminate, RefreshData are handled by RtdServerBase
    
    virtual HRESULT __stdcall ConnectData(long TopicID, SAFEARRAY** Strings, VARIANT_BOOL* GetNewValues, VARIANT* pvarOut) override;
    virtual HRESULT __stdcall DisconnectData(long TopicID) override;

    // --- IDispatch ---
    virtual HRESULT __stdcall GetTypeInfoCount(UINT* pctinfo) override {
        if (!pctinfo) return E_POINTER;
        *pctinfo = 0;
        return S_OK;
    }
    virtual HRESULT __stdcall GetTypeInfo(UINT, LCID, ITypeInfo**) override { return E_NOTIMPL; }
    virtual HRESULT __stdcall GetIDsOfNames(REFIID riid, LPOLESTR* rgszNames, UINT cNames, LCID lcid, DISPID* rgDispId) override {
        if (cNames > 0 && rgszNames && rgszNames[0]) {
            std::wstring name = rgszNames[0];
            if (name == L"ServerStart") { *rgDispId = 10; return S_OK; }
            if (name == L"ConnectData") { *rgDispId = 11; return S_OK; }
            if (name == L"RefreshData") { *rgDispId = 12; return S_OK; }
            if (name == L"DisconnectData") { *rgDispId = 13; return S_OK; }
            if (name == L"Heartbeat") { *rgDispId = 14; return S_OK; }
            if (name == L"ServerTerminate") { *rgDispId = 15; return S_OK; }
        }
        return DISP_E_UNKNOWNNAME;
    }
    virtual HRESULT __stdcall Invoke(DISPID dispIdMember, REFIID riid, LCID lcid, WORD wFlags, DISPPARAMS* pDispParams, VARIANT* pVarResult, EXCEPINFO* pExcepInfo, UINT* puArgErr) override {
        xll::LogDebug("RtdServer::Invoke DISPID: " + std::to_string(dispIdMember) + " Args: " + std::to_string(pDispParams->cArgs));
        for (UINT i = 0; i < pDispParams->cArgs; ++i) {
            xll::LogDebug("  Arg[" + std::to_string(i) + "] VT: " + std::to_string(pDispParams->rgvarg[i].vt));
        }
        
        switch (dispIdMember) {
            case 10: { // ServerStart(Callback, pfRes)
                if (pDispParams->cArgs < 1) return DISP_E_BADPARAMCOUNT;
                IUnknown* pUnk = (pDispParams->rgvarg[0].vt == VT_DISPATCH) ? (IUnknown*)pDispParams->rgvarg[0].pdispVal : (pDispParams->rgvarg[0].vt == VT_UNKNOWN ? pDispParams->rgvarg[0].punkVal : nullptr);
                if (!pUnk) return DISP_E_TYPEMISMATCH;
                
                rtd::IRTDUpdateEvent* pCallback = nullptr;
                if (FAILED(pUnk->QueryInterface(rtd::IID_IRTDUpdateEvent, (void**)&pCallback))) return E_NOINTERFACE;
                
                long res = 0;
                HRESULT hr = ServerStart(pCallback, &res);
                pCallback->Release();
                if (pVarResult) { VariantInit(pVarResult); pVarResult->vt = VT_I4; pVarResult->lVal = res; }
                return hr;
            }
            case 11: { // ConnectData(TopicID, Strings, GetNewValues, pvarOut)
                if (pDispParams->cArgs < 3) return DISP_E_BADPARAMCOUNT;
                long topicId = (pDispParams->rgvarg[2].vt == VT_I4) ? pDispParams->rgvarg[2].lVal : 0;
                SAFEARRAY** ppSA = (pDispParams->rgvarg[1].vt == (VT_ARRAY|VT_BYREF|VT_VARIANT)) ? pDispParams->rgvarg[1].pparray : (pDispParams->rgvarg[1].vt == (VT_ARRAY|VT_VARIANT) ? &pDispParams->rgvarg[1].parray : nullptr);
                return ConnectData(topicId, ppSA, pDispParams->rgvarg[0].pboolVal, pVarResult);
            }
            case 12: { // RefreshData(TopicCount, parrayOut)
                if (pDispParams->cArgs < 1) return DISP_E_BADPARAMCOUNT;
                
                long* pTopicCount = nullptr;
                if (pDispParams->rgvarg[0].vt == (VT_I4 | VT_BYREF)) {
                    pTopicCount = pDispParams->rgvarg[0].plVal;
                } else if (pDispParams->rgvarg[0].vt == VT_I4) {
                    pTopicCount = &(pDispParams->rgvarg[0].lVal);
                }

                if (!pTopicCount) {
                    xll::LogDebug("RefreshData: Arg[0] is not VT_I4|VT_BYREF (it is " + std::to_string(pDispParams->rgvarg[0].vt) + ")");
                    return DISP_E_TYPEMISMATCH;
                }
                
                SAFEARRAY* psa = nullptr;
                HRESULT hr = RefreshData(pTopicCount, &psa);
                if (SUCCEEDED(hr) && pVarResult) {
                    VariantInit(pVarResult);
                    pVarResult->vt = VT_ARRAY | VT_VARIANT;
                    pVarResult->parray = psa;
                }
                return hr;
            }
            case 13: { // DisconnectData(TopicID)
                if (pDispParams->cArgs < 1) return DISP_E_BADPARAMCOUNT;
                // TopicID is the first parameter (index 0)
                long topicId = (pDispParams->rgvarg[0].vt == VT_I4) ? pDispParams->rgvarg[0].lVal : 0;
                return DisconnectData(topicId);
            }
            case 14: { // Heartbeat(pfRes)
                if (pDispParams->cArgs < 1) return DISP_E_BADPARAMCOUNT;
                // pfRes is an [out] long* (index 0)
                long* pfRes = (pDispParams->rgvarg[0].vt == (VT_I4 | VT_BYREF)) ? pDispParams->rgvarg[0].plVal : nullptr;
                if (pfRes) return Heartbeat(pfRes);
                
                long d = 0;
                HRESULT hr = Heartbeat(&d);
                if (pVarResult) { VariantInit(pVarResult); pVarResult->vt = VT_I4; pVarResult->lVal = d; }
                return hr;
            }
            case 15: // ServerTerminate
                return ServerTerminate();
            default: return DISP_E_MEMBERNOTFOUND;
        }
    }
};

#endif // XLL_RTD_ENABLED
