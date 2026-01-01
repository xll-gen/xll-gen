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

        if (g_rtdCallback) {
            xll::LogDebug("RTD: Notifying Excel via Callback->UpdateNotify()");
            g_rtdCallback->UpdateNotify();
        } else {
            xll::LogDebug("RTD: Update notification skipped, Callback is NULL");
        }
    }
}

// RtdServer Implementation

HRESULT __stdcall RtdServer::ServerStart(rtd::IRTDUpdateEvent* Callback, long* pfRes) {
    xll::LogDebug("RTD ServerStart called");
    HRESULT hr = rtd::RtdServerBase::ServerStart(Callback, pfRes);
    if (SUCCEEDED(hr)) {
        std::lock_guard<std::mutex> lock(g_rtdMutex);
        g_rtdCallback = Callback;
        xll::LogDebug("RTD ServerStart succeeded, Callback stored.");
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
    xll::LogDebug("RTD RefreshData: " + std::to_string(*TopicCount) + " updates available");

    if (*TopicCount == 0) {
        *parrayOut = nullptr;
        return S_OK;
    }

    // 2D Array [2][TopicCount]: Row 0 = TopicID, Row 1 = Value
    // Based on AGENTS.md Section 22:
    // bounds[0] (Rightmost) = TopicCount (Columns)
    // bounds[1] (Leftmost)  = 2 (Rows)
    SAFEARRAYBOUND bounds[2];
    bounds[0].cElements = *TopicCount; // Columns
    bounds[0].lLbound = 0;
    bounds[1].cElements = 2;           // Rows
    bounds[1].lLbound = 0;

    *parrayOut = SafeArrayCreate(VT_VARIANT, 2, bounds);
    if (!*parrayOut) return E_OUTOFMEMORY;

    for (long i = 0; i < *TopicCount; ++i) {
            long indices[2];
            
            // Row 0, Col i: TopicID
            // indices[0] (Leftmost/Row) = 0
            // indices[1] (Rightmost/Col) = i
            indices[0] = 0; 
            indices[1] = i; 
            
            VARIANT vID; VariantInit(&vID); vID.vt = VT_I4; vID.lVal = updates[i].topicId;
            HRESULT hr1 = SafeArrayPutElement(*parrayOut, indices, &vID);
            if (FAILED(hr1)) xll::LogError("RTD: SafeArrayPutElement(ID) failed: " + std::to_string(hr1));

            // Row 1, Col i: Value
            indices[0] = 1; 
            // indices[1] remains i
            
            HRESULT hr2 = SafeArrayPutElement(*parrayOut, indices, &updates[i].value);
            if (FAILED(hr2)) xll::LogError("RTD: SafeArrayPutElement(Val) failed: " + std::to_string(hr2));

            xll::LogDebug("RTD: Returning TopicID " + std::to_string(updates[i].topicId));
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
            g_rtdCallback = nullptr;
    }
    return RtdServerBase::ServerTerminate();
}

#endif // XLL_RTD_ENABLED
