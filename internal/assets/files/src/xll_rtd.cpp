#ifdef XLL_RTD_ENABLED

#include "xll_rtd.h"
#include "xll_rtd_once.h"
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

// SCODE placed in the VT_ERROR VARIANT when an RTD update carries a value of
// an AnyValue kind this client does not know how to render (the else branch in
// ProcessRtdUpdate). 0x80040101 is the classic Automation "value not available"
// error scode (the historical inline comment called it the "xlerrValue
// equivalent"). It is distinct from the refresh-time placeholder at
// rtd/server.h (scode 2043, xlErrGettingData), which marks a cell that has not
// yet received its first value; this constant marks a value we received but
// cannot convert. Named per IMPROVEMENT_BACKLOG.md §3.
static constexpr SCODE kRtdUnsupportedValueScode = 0x80040101;

// Global RTD State
RtdServer* g_rtdServer = nullptr;

// In-flight counter for detached ConnectData lambdas. Each spawned thread
// increments on entry and decrements on exit via RtdConnectInFlightGuard.
// OnAutoClose calls WaitForRtdConnectDrain() to drain these before
// `delete g_phost` is performed. The wiring lives in
// xll_lifecycle.cpp::OnAutoClose (guarded by XLL_RTD_ENABLED).
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
             v.scode = kRtdUnsupportedValueScode;
        }
    }

    // rtd-once: this update is, by construction, the single final value for a
    // one-shot topic. Cache it under the topic's key so the next time Excel
    // recalcs the cell, the XLL wrapper returns the value directly (without
    // re-issuing xlfRtd), which drops the RTD reference and lets Excel tear
    // the topic down. Lookups/inserts are no-ops for plain rtd topics (the
    // topicID was never registered in the rtd-once registry). See AGENTS.md
    // §19.3.
    {
        std::wstring onceKey;
        if (xll::RtdOnceRegistry::Instance().KeyForTopic(topicID, onceKey)) {
            xll::RtdOnceRegistry::Instance().StoreResult(onceKey, v);
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

    // rtd-once detection: if the first topic string names an rtd-once
    // function, record this topicID -> key mapping so ProcessRtdUpdate can
    // cache the eventual one-shot result under the same key the wrapper uses,
    // and return the #GETTING_DATA placeholder (VT_ERROR 2043) below instead
    // of the plain-RTD "Connecting..." BSTR. Plain rtd topics skip all of
    // this — their initial value is unchanged. See AGENTS.md §19.3.
    bool isRtdOnce = false;
    if (!strings.empty()) {
        std::vector<std::wstring> wTopics;
        wTopics.reserve(strings.size());
        for (const auto& s : strings) wTopics.push_back(StringToWString(s));
        if (xll::RtdOnceRegistry::Instance().IsOnceFunction(wTopics[0])) {
            isRtdOnce = true;
            xll::RtdOnceRegistry::Instance().RegisterTopic(TopicID, xll::MakeRtdOnceKey(wTopics));
        }
    }

    // The detached thread accesses g_host (== *g_phost; see xll_ipc.h), a global
    // owned by xll_ipc / lifecycle. On a graceful close, OnAutoClose calls
    // WaitForRtdConnectDrain (in xll_lifecycle.cpp, guarded by XLL_RTD_ENABLED)
    // BEFORE deleting g_phost; on a forced unload (DllMain DLL_PROCESS_DETACH per
    // AGENTS.md §20), threads are leaked rather than joined. To make this safe:
    //   1. Re-check xll::g_isUnloading at every yield point — top of lambda,
    //      before SHM access, before every slot acquire, before every Send.
    //   2. Hold an RtdConnectInFlightGuard so OnAutoClose can wait for
    //      in-flight Connects to drain before tearing down g_phost.
    //
    // Drain-cap alignment (AGENTS.md §23.0): the old code issued a SINGLE Send
    // with a 5000 ms blocking timeout. WaitForRtdConnectDrain's cap is 2000 ms
    // (xll_lifecycle.cpp). A Connect that blocked >2 s inside that single Send
    // outlived the drain, so OnAutoClose proceeded to `delete g_phost` while the
    // Send was still touching the slot — a narrow use-after-free. The fix mirrors
    // ribbon_addin.cpp::SendCommandInvoke (same structural problem): a bounded
    // retry loop of SHORT per-attempt timeouts that re-checks g_isUnloading
    // between attempts and re-acquires a FRESH ZeroCopySlot each attempt
    // (ZeroCopySlot::Send disowns its slot — slotIdx = -1 — on timeout, so a slot
    // object cannot be reused across attempts; verified against shm
    // DirectHost.h:266). With a <=250 ms per-attempt timeout and the unload
    // re-checks, an in-flight Connect thread observes g_isUnloading and returns
    // within ~350 ms worst case of it being set (the re-check is between
    // attempts, not mid-Send: one in-flight attempt = <=250 ms timeout + up to
    // one ~100 ms WaitEvent quantum + spin), so the existing 2000 ms drain cap
    // is sufficient with wide margin — no UAF window. The total retry budget
    // (kMaxAttempts * kAttemptTimeoutMs == 5000 ms) keeps behavior under a
    // slow-but-alive host unchanged from the previous single 5000 ms Send.
    //
    // Duplication-on-retry (verified, benign — AGENTS.md §23.0): a timed-out
    // Send does NOT mean the guest never saw the request. DirectHost::WaitResponse
    // (shm DirectHost.h:131) publishes SLOT_REQ_READY *first*, then waits for the
    // guest to flip the slot to SLOT_RESP_READY; a timeout means the guest did not
    // *respond* in budget, not that it did not *consume*. The guest may already
    // have dispatched MSG_RTD_CONNECT to HandleRtdConnect. A retry can therefore
    // deliver MSG_RTD_CONNECT twice. The real double-exposure is that the
    // USER-FACING handler runs twice: HandleRtdConnect dispatches the user's
    // OnRtdConnect / generated rtd-once dispatch in a panic-recovered goroutine
    // (Subscribe is only reached if that handler calls it — and IS idempotent on
    // (topicID,key), pkg/rtd manager.go:60, so topic identity stays consistent).
    // For rtd-once, a duplicate connect runs the one-shot handler twice and
    // pushes twice — benign: RtdOnceRegistry::StoreResult overwrites under the
    // same key and UpdateTopic/NotifyUpdate are value-idempotent. Side-effecting
    // connect handlers must tolerate the duplicate (delivery is at-least-once;
    // see AGENTS.md §18.11 "Delivery contract"). No dedup is added.
    std::thread([TopicID, strings, newVal]() {
        // Declared INSIDE the lambda: these are odr-used (passed by value to
        // std::chrono::milliseconds and slot.Send), so a constexpr local in the
        // enclosing scope would have to be captured. MinGW/GCC rejects an
        // uncaptured odr-use (MSVC silently accepts) — keeping them lambda-local
        // sidesteps the capture requirement entirely. kMaxAttempts *
        // kAttemptTimeoutMs == 5000 ms total budget (see drain-cap note above).
        constexpr int kMaxAttempts = 20;
        constexpr unsigned int kAttemptTimeoutMs = 250;
        RtdConnectInFlightGuard inflight;

        for (int attempt = 0; attempt < kMaxAttempts; ++attempt) {
            if (xll::g_isUnloading.load(std::memory_order_acquire)) return;
            if (!g_phost) return;

            auto slot = g_phost->GetZeroCopySlot();

            if (xll::g_isUnloading.load(std::memory_order_acquire)) return;
            if (!slot.IsValid()) {
                // All host slots momentarily busy; yield and retry.
                std::this_thread::sleep_for(std::chrono::milliseconds(kAttemptTimeoutMs));
                continue;
            }

            SHMAllocator allocator(slot.GetReqBuffer(), slot.GetMaxReqSize());
            flatbuffers::FlatBufferBuilder builder(slot.GetMaxReqSize(), &allocator, false);

            std::vector<flatbuffers::Offset<flatbuffers::String>> strOffsets;
            for (const auto& s : strings) strOffsets.push_back(builder.CreateString(s));

            auto stringsVec = builder.CreateVector(strOffsets);
            auto req = protocol::CreateRtdConnectRequest(builder, TopicID, stringsVec, newVal);
            builder.Finish(req);

            if (xll::g_isUnloading.load(std::memory_order_acquire)) return;

            auto res = slot.Send(-((int)builder.GetSize()), (shm::MsgType)MSG_RTD_CONNECT, kAttemptTimeoutMs);

            // After Send returns (success or timeout) we touch nothing on
            // g_phost; the ZeroCopySlot destructor only touches its own slot
            // header which is invalidated through DirectHost::Shutdown()'s
            // sharedState.reset().
            if (!res.HasError()) {
                return; // delivered (ack received)
            }
            // HasError() covers BOTH a transport timeout AND a delivered-but-
            // SYSTEM_ERROR response (shm maps the latter to Error::InternalError)
            // — both retry. A SYSTEM_ERROR resend is benign per the duplication
            // analysis above. No extra sleep — the Send already blocked
            // kAttemptTimeoutMs waiting for a reader.
        }

        if (!xll::g_isUnloading.load(std::memory_order_acquire)) {
            xll::LogWarn("RTD Connect dropped (server not reachable after retries): TopicID " +
                         std::to_string(TopicID));
        }
    }).detach();

    if (pvarOut) {
        VariantInit(pvarOut);
        if (isRtdOnce) {
            // rtd-once: surface #GETTING_DATA (VT_ERROR 2043) as the initial
            // value so the cell reads as "pending" rather than the textual
            // "Connecting..." used by plain rtd topics. Plain rtd's initial
            // value is intentionally left unchanged.
            *pvarOut = xll::MakeGettingDataVariant();
        } else {
            pvarOut->vt = VT_BSTR;
            pvarOut->bstrVal = SysAllocString(L"Connecting...");
        }
    }

    xll::LogDebug("RTD: Returning TopicID " + std::to_string(TopicID));
    return S_OK;
}

HRESULT __stdcall RtdServer::DisconnectData(long TopicID) {
    // rtd-once: drop the topicID->key mapping. The stored result (if any)
    // survives under its key — its lifetime is governed by the once/memoize
    // policy (CalculationEnded clears once-mode results; memoize keeps them),
    // not by disconnect. A no-op for plain rtd topics.
    xll::RtdOnceRegistry::Instance().UnregisterTopic(TopicID);

    // 1. Notify Go Backend.
    //
    // Unlike ConnectData this runs synchronously on the STA thread (no detached
    // thread, so it is NOT covered by WaitForRtdConnectDrain) and its single
    // Send already uses a 500 ms timeout — well under the 2000 ms graceful drain
    // cap (xll_lifecycle.cpp), so it cannot overrun teardown the way the old
    // 5000 ms Connect could. The only residual hazard is a forced unload (DllMain
    // DLL_PROCESS_DETACH per AGENTS.md §20) racing this call after g_phost has
    // been torn down, so re-check the unload flag and null-check g_phost before
    // touching SHM. No retry loop is needed: a slow-but-alive guest answers
    // within the 500 ms budget, and on disconnect there is no first-click cold
    // start to absorb.
    if (!xll::g_isUnloading.load(std::memory_order_acquire) && g_phost) {
        auto slot = g_phost->GetZeroCopySlot();
        if (slot.IsValid() && !xll::g_isUnloading.load(std::memory_order_acquire)) {
            SHMAllocator allocator(slot.GetReqBuffer(), slot.GetMaxReqSize());
            flatbuffers::FlatBufferBuilder builder(slot.GetMaxReqSize(), &allocator, false);
            auto req = protocol::CreateRtdDisconnectRequest(builder, TopicID);
            builder.Finish(req);
            slot.Send(-((int)builder.GetSize()), (shm::MsgType)MSG_RTD_DISCONNECT, 500);
        }
    }

    // 2. Clean up in Base Class
    return RtdServerBase::DisconnectData(TopicID);
}

#endif // XLL_RTD_ENABLED