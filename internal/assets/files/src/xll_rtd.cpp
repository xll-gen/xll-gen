#ifdef XLL_RTD_ENABLED

#include "xll_rtd.h"
#include "xll_log.h"
#include "xll_ipc.h"
#include "xll_lifecycle.h"
#include "types/utility.h"
#include "SHMAllocator.h"
#include "shm/DirectHost.h"
#include "shm/IPCUtils.h"
#include <thread>
#include <atomic>
#include <chrono>

// Defined in xll_ipc.cpp
extern shm::DirectHost g_host;

// Global RTD State
RtdServer* g_rtdServer = nullptr;

// In-flight counter for detached ConnectData lambdas. Each spawned thread
// increments on entry and decrements on exit via RtdConnectInFlightGuard.
// OnAutoClose may call WaitForRtdConnectDrain() to drain these before
// `delete g_phost` is performed (integration in xll_lifecycle.cpp is pending —
// the API is exposed here so the lifecycle owner can wire it in without
// touching this file).
static std::atomic<int> g_rtdConnectInFlight{0};

namespace {

struct RtdConnectInFlightGuard {
    RtdConnectInFlightGuard() noexcept {
        g_rtdConnectInFlight.fetch_add(1, std::memory_order_acq_rel);
    }
    ~RtdConnectInFlightGuard() noexcept {
        g_rtdConnectInFlight.fetch_sub(1, std::memory_order_acq_rel);
    }
    RtdConnectInFlightGuard(const RtdConnectInFlightGuard&) = delete;
    RtdConnectInFlightGuard& operator=(const RtdConnectInFlightGuard&) = delete;
};

} // anonymous namespace

// Drain helper. Returns true if the in-flight count reached 0 within timeoutMs,
// false on timeout. Polls at 1 ms granularity (RTD ConnectData calls are rare
// and short, so polling cost is negligible).
bool WaitForRtdConnectDrain(unsigned int timeoutMs) {
    using clock = std::chrono::steady_clock;
    auto deadline = clock::now() + std::chrono::milliseconds(timeoutMs);
    while (g_rtdConnectInFlight.load(std::memory_order_acquire) > 0) {
        if (clock::now() >= deadline) {
            return false;
        }
        std::this_thread::sleep_for(std::chrono::milliseconds(1));
    }
    return true;
}

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

    if (g_rtdServer) {
        // Update topic in RtdServerBase
        g_rtdServer->UpdateTopic(topicID, v);
        
        xll::LogDebug("RTD: Notifying Excel via g_rtdServer->NotifyUpdate()");
        g_rtdServer->NotifyUpdate();
    } else {
        xll::LogDebug("RTD: Update notification skipped, Server is NULL");
    }
    VariantClear(&v);
}

// RtdServer Implementation

// ServerStart, Heartbeat, ServerTerminate, RefreshData are handled by RtdServerBase

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

    // The detached thread accesses g_host (a global owned by xll_ipc / lifecycle).
    // On a graceful close, OnAutoClose deletes g_phost; on a forced unload
    // (DllMain DLL_PROCESS_DETACH per AGENTS.md §20), threads are leaked rather
    // than joined. To make this safe:
    //   1. Re-check xll::g_isUnloading at every yield point — top of lambda,
    //      before SHM access, after each blocking SHM call returns.
    //   2. Hold an RtdConnectInFlightGuard so OnAutoClose can call
    //      WaitForRtdConnectDrain(timeoutMs) before deleting g_phost.
    //
    // NOTE (2026-05-16): the call-site integration in OnAutoClose lives in
    // xll_lifecycle.cpp, which is owned by another in-flight agent. This file
    // exposes the drain API and the per-yield-point unloading checks; once the
    // lifecycle owner wires `WaitForRtdConnectDrain(N_ms)` into OnAutoClose
    // before `delete g_phost`, the UAF window closes entirely. Until then the
    // unloading-flag checks reduce — but do not fully eliminate — the race.
    std::thread([TopicID, strings, newVal]() {
        RtdConnectInFlightGuard inflight;

        if (xll::g_isUnloading.load(std::memory_order_acquire)) return;

        auto slot = g_host.GetZeroCopySlot();

        if (xll::g_isUnloading.load(std::memory_order_acquire)) return;

        SHMAllocator allocator(slot.GetReqBuffer(), slot.GetMaxReqSize());
        flatbuffers::FlatBufferBuilder builder(slot.GetMaxReqSize(), &allocator, false);

        std::vector<flatbuffers::Offset<flatbuffers::String>> strOffsets;
        for (const auto& s : strings) strOffsets.push_back(builder.CreateString(s));

        auto stringsVec = builder.CreateVector(strOffsets);
        auto req = protocol::CreateRtdConnectRequest(builder, TopicID, stringsVec, newVal);
        builder.Finish(req);

        if (xll::g_isUnloading.load(std::memory_order_acquire)) return;

        slot.Send(-((int)builder.GetSize()), (shm::MsgType)MSG_RTD_CONNECT, 5000);

        // After Send returns (success or timeout) we touch nothing on g_host;
        // the ZeroCopySlot destructor only touches its own slot header which
        // is invalidated through DirectHost::Shutdown()'s sharedState.reset().
    }).detach();

    if (pvarOut) {
        VariantInit(pvarOut);
        pvarOut->vt = VT_BSTR;
        pvarOut->bstrVal = SysAllocString(L"Connecting...");
    }
    
    xll::LogDebug("RTD: Returning TopicID " + std::to_string(TopicID));
    return S_OK;
}

HRESULT __stdcall RtdServer::DisconnectData(long TopicID) {
    // 1. Notify Go Backend
    auto slot = g_host.GetZeroCopySlot();
    SHMAllocator allocator(slot.GetReqBuffer(), slot.GetMaxReqSize());
    flatbuffers::FlatBufferBuilder builder(slot.GetMaxReqSize(), &allocator, false);
    auto req = protocol::CreateRtdDisconnectRequest(builder, TopicID);
    builder.Finish(req);
    slot.Send(-((int)builder.GetSize()), (shm::MsgType)MSG_RTD_DISCONNECT, 500);

    // 2. Clean up in Base Class
    return RtdServerBase::DisconnectData(TopicID);
}

#endif // XLL_RTD_ENABLED