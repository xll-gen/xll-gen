#ifndef RTD_SERVER_H
#define RTD_SERVER_H

#include <mutex>
#include <vector>
#include <map>
#include <algorithm>
#include <string>
#include "defs.h"
#include "module.h"
#include "types/utility.h"

// Forward declaration of logging
namespace xll { void LogDebug(const std::string& msg); }

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
            std::lock_guard<std::mutex> lock(m_callbackMutex);
            if (m_callback) {
                m_callback->Release();
                m_callback = nullptr;
            }
            return S_OK;
        }

        /**
         * @brief Thread-safe helper to notify Excel of updates.
         */
        void NotifyUpdate() {
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
