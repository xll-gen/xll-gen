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
RtdServer* g_rtdServer = nullptr;

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
    xll::LogDebug("RTD: Received update for TopicID " + std::to_string(topicID));

    VARIANT v; VariantInit(&v);
    auto anyVal = update->val();
    if (anyVal) {
        if (anyVal->val_type() == protocol::AnyValue::Str) {
             v.vt = VT_BSTR;
             v.bstrVal = SysAllocString(StringToWString(anyVal->val_as_Str()->val()->str()).c_str());
        } else if (anyVal->val_type() == protocol::AnyValue::Num) {
             v.vt = VT_R8;
             v.dblVal = anyVal->val_as_Num()->val();
        } else if (anyVal->val_type() == protocol::AnyValue::Int) {
             v.vt = VT_I4;
             v.lVal = anyVal->val_as_Int()->val();
        } else if (anyVal->val_type() == protocol::AnyValue::Bool) {
             v.vt = VT_BOOL;
             v.boolVal = anyVal->val_as_Bool()->val() ? VARIANT_TRUE : VARIANT_FALSE;
        } else {
             v.vt = VT_ERROR;
             v.scode = 0x80040101; // xlerrValue equivalent
        }
    }

    {
        std::lock_guard<std::mutex> lock(g_rtdMutex);
        RtdValue& rv = g_rtdValues[topicID];
        rv.topicId = topicID;
        VariantCopy(&rv.value, &v);
        rv.dirty = true;
        VariantClear(&v);

        if (g_rtdServer) {
            xll::LogDebug("RTD: Notifying Excel via g_rtdServer->NotifyUpdate()");
            g_rtdServer->NotifyUpdate();
        } else {
            xll::LogDebug("RTD: Update notification skipped, Server is NULL");
        }
    }
}

// RtdServer Implementation

// ServerStart is handled by RtdServerBase

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
    xll::LogDebug("RTD RefreshData: " + std::to_string(*TopicCount) + " updates available");

    HRESULT hr = rtd::RtdServerBase::CreateRefreshDataArray(*TopicCount, parrayOut);
    if (FAILED(hr)) return hr;
    
    if (*TopicCount == 0) return S_OK;

    for (long i = 0; i < *TopicCount; ++i) {
            long indices[2];
            
            // Row 0, Col i: TopicID
            // indices[0] corresponds to bounds[0] (Rightmost/Col) -> i
            // indices[1] corresponds to bounds[1] (Leftmost/Row)  -> 0
            indices[0] = i; 
            indices[1] = 0; 
            
            VARIANT vID; VariantInit(&vID); vID.vt = VT_I4; vID.lVal = updates[i].topicId;
            HRESULT hr1 = SafeArrayPutElement(*parrayOut, indices, &vID);
            if (FAILED(hr1)) xll::LogError("RTD: SafeArrayPutElement(ID) failed: " + std::to_string(hr1));

            // Row 1, Col i: Value
            indices[1] = 1; 
            // indices[0] remains i
            
            HRESULT hr2 = SafeArrayPutElement(*parrayOut, indices, &updates[i].value);
            if (FAILED(hr2)) xll::LogError("RTD: SafeArrayPutElement(Val) failed: " + std::to_string(hr2));

            xll::LogDebug("RTD: Returning TopicID " + std::to_string(updates[i].topicId));
    }
    return S_OK;
}

// Heartbeat and ServerTerminate are handled by RtdServerBase

#endif // XLL_RTD_ENABLED
