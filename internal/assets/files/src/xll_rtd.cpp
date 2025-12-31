#ifdef XLL_RTD_ENABLED

#include "xll_rtd.h"
#include "xll_log.h"
#include "xll_ipc.h"
#include "types/utility.h" // For WideToUtf8, StringToWString
#include "SHMAllocator.h"
#include "shm/DirectHost.h"
#include "shm/IPCUtils.h"

// Defined in xll_ipc.cpp
extern shm::DirectHost g_host;

// Global RTD State
std::mutex g_rtdMutex;
std::map<long, RtdValue> g_rtdValues;
rtd::IRTDUpdateEvent* g_rtdCallback = nullptr;

GUID StringToGuid(const std::wstring& str) {
    GUID guid;
    HRESULT hr = CLSIDFromString(str.c_str(), &guid);
    if (FAILED(hr)) {
        return { 0 };
    }
    return guid;
}

void ProcessRtdUpdate(const protocol::RtdUpdate* update) {
    if (!update) return;
    long topicID = update->topic_id();

    // Extract value from Any
    std::wstring valStr;
    auto anyVal = update->val();
    if (anyVal) {
        if (anyVal->val_type() == protocol::AnyValue::Str) {
             valStr = StringToWString(anyVal->val_as_Str()->val()->str());
        } else if (anyVal->val_type() == protocol::AnyValue::Num) {
             valStr = std::to_wstring(anyVal->val_as_Num()->val());
        } else if (anyVal->val_type() == protocol::AnyValue::Int) {
             valStr = std::to_wstring(anyVal->val_as_Int()->val());
        } else if (anyVal->val_type() == protocol::AnyValue::Bool) {
             valStr = anyVal->val_as_Bool()->val() ? L"TRUE" : L"FALSE";
        } else {
             valStr = L"#VALUE!";
        }
    }

    {
        std::lock_guard<std::mutex> lock(g_rtdMutex);
        g_rtdValues[topicID] = {topicID, valStr, true};

        if (g_rtdCallback) {
            // We should use AddRef if we stored it, but here we just use the raw pointer carefully.
            // Ideally, we should have AddRef'd it in ConnectData.
            // For now, assume simple notify.
            // Note: UpdateNotify is thread-safe.
            g_rtdCallback->UpdateNotify();
        }
    }
}

// RtdServer Implementation

HRESULT __stdcall RtdServer::ConnectData(long TopicID, SAFEARRAY** Strings, VARIANT_BOOL* GetNewValues, VARIANT* pvarOut) {
    // Send Connect Request to Go
    auto slot = g_host.GetZeroCopySlot();
    SHMAllocator allocator(slot.GetReqBuffer(), slot.GetMaxReqSize());
    flatbuffers::FlatBufferBuilder builder(slot.GetMaxReqSize(), &allocator, false);

    std::vector<flatbuffers::Offset<flatbuffers::String>> strOffsets;
    if (Strings && *Strings) {
        SAFEARRAY* psa = *Strings;
        long lb, ub;
        SafeArrayGetLBound(psa, 1, &lb);
        SafeArrayGetUBound(psa, 1, &ub);
        for (long i = lb; i <= ub; ++i) {
            VARIANT v;
            VariantInit(&v);
            SafeArrayGetElement(psa, &i, &v);
            if (v.vt == VT_BSTR) {
                std::wstring wstr(v.bstrVal, SysStringLen(v.bstrVal));
                strOffsets.push_back(builder.CreateString(WideToUtf8(wstr)));
            }
            VariantClear(&v);
        }
    }

    auto stringsVec = builder.CreateVector(strOffsets);
    auto req = protocol::CreateRtdConnectRequest(builder, TopicID, stringsVec, (GetNewValues && *GetNewValues));
    builder.Finish(req);

    auto res = slot.Send(-((int)builder.GetSize()), (shm::MsgType)MSG_RTD_CONNECT, 2000);
    if (res.HasError()) {
        xll::LogError("RTD Connect failed: " + SHMErrorToString(res.GetError()));
        // Return Error
        VariantInit(pvarOut);
        pvarOut->vt = VT_ERROR;
        pvarOut->scode = 2046; // xlErrConnect
        return S_OK;
    }

    {
            std::lock_guard<std::mutex> lock(g_rtdMutex);
            // Store reference to callback
            if (m_callback) {
                if (g_rtdCallback) g_rtdCallback->Release();
                g_rtdCallback = m_callback;
                g_rtdCallback->AddRef();
            }
    }

    // Return #GettingData to indicate async update
    VariantInit(pvarOut);
    pvarOut->vt = VT_ERROR;
    pvarOut->scode = 2043; // xlErrGettingData
    return S_OK;
}

HRESULT __stdcall RtdServer::DisconnectData(long TopicID) {
    auto slot = g_host.GetZeroCopySlot();
    SHMAllocator allocator(slot.GetReqBuffer(), slot.GetMaxReqSize());
    flatbuffers::FlatBufferBuilder builder(slot.GetMaxReqSize(), &allocator, false);

    auto req = protocol::CreateRtdDisconnectRequest(builder, TopicID);
    builder.Finish(req);

    slot.Send(-((int)builder.GetSize()), (shm::MsgType)MSG_RTD_DISCONNECT, 500);

    // Cleanup
    {
            std::lock_guard<std::mutex> lock(g_rtdMutex);
            g_rtdValues.erase(TopicID);
    }

    return S_OK;
}

HRESULT __stdcall RtdServer::RefreshData(long* TopicCount, SAFEARRAY** parrayOut) {
    std::lock_guard<std::mutex> lock(g_rtdMutex);

    std::vector<RtdValue> updates;
    for (auto& [id, val] : g_rtdValues) {
        if (val.dirty) {
            updates.push_back(val);
            val.dirty = false;
        }
    }

    *TopicCount = (long)updates.size();
    HRESULT hr = CreateRefreshDataArray(*TopicCount, parrayOut);
    if (FAILED(hr)) return hr;

    if (*TopicCount == 0) return S_OK;

    long indices[2];
    for (long i = 0; i < *TopicCount; ++i) {
            long topicID = updates[i].topicId;
            indices[0] = 0; indices[1] = i;
            VARIANT vID; VariantInit(&vID);
            vID.vt = VT_I4; vID.lVal = topicID;
            SafeArrayPutElement(*parrayOut, indices, &vID);

            indices[0] = 1; indices[1] = i;
            VARIANT vVal; VariantInit(&vVal);
            vVal.vt = VT_BSTR;
            vVal.bstrVal = SysAllocString(updates[i].value.c_str());
            SafeArrayPutElement(*parrayOut, indices, &vVal);
            SysFreeString(vVal.bstrVal);
    }

    return S_OK;
}

HRESULT __stdcall RtdServer::ServerTerminate() {
    {
            std::lock_guard<std::mutex> lock(g_rtdMutex);
            if (g_rtdCallback) {
                g_rtdCallback->Release();
                g_rtdCallback = nullptr;
            }
    }
    return RtdServerBase::ServerTerminate();
}

#endif // XLL_RTD_ENABLED
