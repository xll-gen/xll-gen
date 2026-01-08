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

        // --- IDispatch (Stub) ---
        HRESULT __stdcall GetTypeInfoCount(UINT* pctinfo) override {
            if (!pctinfo) return E_POINTER;
            *pctinfo = 0;
            return S_OK;
        }
        HRESULT __stdcall GetTypeInfo(UINT, LCID, ITypeInfo**) override { return E_NOTIMPL; }
        HRESULT __stdcall GetIDsOfNames(REFIID, LPOLESTR*, UINT, LCID, DISPID*) override { return E_NOTIMPL; }
        HRESULT __stdcall Invoke(DISPID, REFIID, LCID, WORD, DISPPARAMS*, VARIANT*, EXCEPINFO*, UINT*) override { return E_NOTIMPL; }

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
         * The array is [2][topicCount].
         * Row 0: Topic IDs.
         * Row 1: Values.
         */
        static HRESULT CreateRefreshDataArray(long topicCount, SAFEARRAY** ppArray) {
            if (!ppArray) return E_POINTER;
            if (topicCount < 0) return E_INVALIDARG;

            if (topicCount == 0) {
                 *ppArray = nullptr;
                 return S_OK;
            }

            SAFEARRAYBOUND bounds[2];
            // To achieve a 2D array of [2][topicCount] (2 Rows, N Columns):
            // The first element in the bounds array (bounds[0]) defines the right-most dimension (Columns).
            // The last element (bounds[1]) defines the left-most dimension (Rows).

            // Dimension 1 (Right-most): Columns (Number of topics)
            bounds[0].cElements = topicCount;
            bounds[0].lLbound = 0;

            // Dimension 2 (Left-most): Rows (0=TopicID, 1=Value)
            bounds[1].cElements = 2;
            bounds[1].lLbound = 0;

            // Note on SafeArrayPutElement indices:
            // indices[0] corresponds to the first dimension in 'bounds' (the right-most one, i.e., the column index).
            // indices[1] corresponds to the second dimension (the left-most one, i.e., the row index).
            // So:
            // indices[0] = topicIndex (Column)
            // indices[1] = 0 (for TopicID row) or 1 (for Value row)
            // This is the standard and expected mapping.

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
                indices[0] = i;

                indices[1] = 0;
                VARIANT vTopicId;
                vTopicId.vt = VT_I4;
                vTopicId.lVal = dirtyTopics[i];
                SafeArrayPutElement(psa, indices, &vTopicId);

                indices[1] = 1;
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
