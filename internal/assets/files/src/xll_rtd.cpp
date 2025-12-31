#ifdef XLL_RTD_ENABLED

#include "xll_rtd.h"
#include "xll_log.h"
#include "xll_ipc.h"
#include "types/utility.h" 
#include "SHMAllocator.h"
#include "shm/DirectHost.h"
#include "shm/IPCUtils.h"
#include <thread>

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
            g_rtdCallback->UpdateNotify();
        }
    }
}

// RtdServer Implementation

HRESULT __stdcall RtdServer::ServerStart(rtd::IRTDUpdateEvent* Callback, long* pfRes) {
    xll::LogDebug("RTD ServerStart called");
    HRESULT hr = rtd::RtdServerBase::ServerStart(Callback, pfRes);
    if (SUCCEEDED(hr)) {
        xll::LogDebug("RTD ServerStart succeeded, pfRes=" + std::to_string(*pfRes));
    }
    return hr;
}

HRESULT __stdcall RtdServer::ConnectData(long TopicID, SAFEARRAY** Strings, VARIANT_BOOL* GetNewValues, VARIANT* pvarOut) {
    xll::LogDebug("RTD ConnectData: TopicID=" + std::to_string(TopicID));
    
    std::vector<std::string> strings;
    if (Strings && *Strings) {
        SAFEARRAY* psa = *Strings;
        long lb, ub;
        SafeArrayGetLBound(psa, 1, &lb);
        SafeArrayGetUBound(psa, 1, &ub);
        for (long i = lb; i <= ub; ++i) {
            VARIANT v; VariantInit(&v);
            if (SUCCEEDED(SafeArrayGetElement(psa, &i, &v))) {
                VARIANT vStr; VariantInit(&vStr);
                if (SUCCEEDED(VariantChangeType(&vStr, &v, 0, VT_BSTR))) {
                    strings.push_back(WideToUtf8(std::wstring(vStr.bstrVal, SysStringLen(vStr.bstrVal))));
                    VariantClear(&vStr);
                }
            }
            VariantClear(&v);
        }
    }
    bool newVal = (GetNewValues && *GetNewValues);

    std::thread([TopicID, strings, newVal]() {
        auto slot = g_host.GetZeroCopySlot();
        SHMAllocator allocator(slot.GetReqBuffer(), slot.GetMaxReqSize());
        flatbuffers::FlatBufferBuilder builder(slot.GetMaxReqSize(), &allocator, false);

        std::vector<flatbuffers::Offset<flatbuffers::String>> strOffsets;
        for (const auto& s : strings) strOffsets.push_back(builder.CreateString(s));

        auto stringsVec = builder.CreateVector(strOffsets);
        auto req = protocol::CreateRtdConnectRequest(builder, TopicID, stringsVec, newVal);
        builder.Finish(req);

        slot.Send(-((int)builder.GetSize()), (shm::MsgType)MSG_RTD_CONNECT, 5000);
    }).detach();

    if (pvarOut) {
        VariantInit(pvarOut);
        pvarOut->vt = VT_BSTR;
        pvarOut->bstrVal = SysAllocString(L"Connecting...");
    }
    
    return S_OK;
}

HRESULT __stdcall RtdServer::DisconnectData(long TopicID) {
    auto slot = g_host.GetZeroCopySlot();
    SHMAllocator allocator(slot.GetReqBuffer(), slot.GetMaxReqSize());
    flatbuffers::FlatBufferBuilder builder(slot.GetMaxReqSize(), &allocator, false);
    auto req = protocol::CreateRtdDisconnectRequest(builder, TopicID);
    builder.Finish(req);
    slot.Send(-((int)builder.GetSize()), (shm::MsgType)MSG_RTD_DISCONNECT, 500);

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
    if (*TopicCount == 0) {
        *parrayOut = nullptr;
        return S_OK;
    }

    HRESULT hr = CreateRefreshDataArray(*TopicCount, parrayOut);
    if (FAILED(hr)) return hr;

    long indices[2];
    for (long i = 0; i < *TopicCount; ++i) {
            // Row 0: TopicID
            indices[0] = 0; // First dimension (Row)
            indices[1] = i; // Second dimension (Column)
            VARIANT vID; VariantInit(&vID); vID.vt = VT_I4; vID.lVal = updates[i].topicId;
            SafeArrayPutElement(*parrayOut, indices, &vID);

            // Row 1: Value
            indices[0] = 1; // First dimension (Row)
            // indices[1] remains i
            VARIANT vVal; VariantInit(&vVal); vVal.vt = VT_BSTR; vVal.bstrVal = SysAllocString(updates[i].value.c_str());
            SafeArrayPutElement(*parrayOut, indices, &vVal);
            SysFreeString(vVal.bstrVal);
    }
    return S_OK;
}

HRESULT __stdcall RtdServer::Heartbeat(long* pfRes) {
    if (pfRes) *pfRes = 1;
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
