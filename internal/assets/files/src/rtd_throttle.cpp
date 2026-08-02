// rtd_throttle.cpp — see include/com/rtd_throttle.h for the contract and why
// the millisecond value is a parameter rather than a baked-in literal.
//
// The gate opens BEFORE com/rtd_throttle.h: that header #errors when the macro
// is absent, precisely so a non-RTD TU cannot declare a symbol whose definition
// this file does not compile. Including it above the gate would make every
// non-RTD build fail here instead.
#ifdef XLL_RTD_ENABLED

#include "com/rtd_throttle.h"

#include "com/dispatch_helpers.h" // xll::com::GetProperty / Invoke
#include "com/excel_app.h"        // xll::com::AcquireExcelApplication
#include "xll_log.h"              // SAFE_LOG_*

#include <atomic>
#include <string>

namespace xll {
namespace throttle {

namespace {

// Applies rtd.throttle_interval from xll.yaml: Application.RTD.ThrottleInterval
// = <ms>. NOTE: this is a per-user, registry-persisted Excel setting — it is
// only touched because the config explicitly opted in.
bool SetRtdThrottleInterval(long ms) {
    IDispatch* pApp = xll::com::AcquireExcelApplication();
    if (!pApp) return false;

    bool ok = false;
    VARIANT vRtd; VariantInit(&vRtd);
    if (SUCCEEDED(xll::com::GetProperty(pApp, L"RTD", &vRtd)) && vRtd.vt == VT_DISPATCH && vRtd.pdispVal) {
        VARIANT vMs; VariantInit(&vMs);
        vMs.vt = VT_I4;
        vMs.lVal = ms;
        ok = SUCCEEDED(xll::com::Invoke(vRtd.pdispVal, L"ThrottleInterval", DISPATCH_PROPERTYPUT, { vMs }, nullptr));
    }
    VariantClear(&vRtd);
    pApp->Release();
    return ok;
}

std::atomic<int> g_rtdThrottleState{0}; // 0=pending, 1=applied, 2=gave up

} // namespace

void TryApplyRtdThrottle(long ms, const char* phase) {
    if (g_rtdThrottleState.load(std::memory_order_acquire) != 0) return;
    static std::atomic<int> s_attempts{0};
    if (SetRtdThrottleInterval(ms)) {
        g_rtdThrottleState.store(1, std::memory_order_release);
        SAFE_LOG_INFO(std::string("RTD: ThrottleInterval set to ") + std::to_string(ms) + "ms (" + phase + ").");
    } else if (s_attempts.fetch_add(1) + 1 >= 10) {
        g_rtdThrottleState.store(2, std::memory_order_release);
        SAFE_LOG_WARN("RTD: could not set ThrottleInterval (Application object unreachable after 10 attempts); keeping the current Excel setting.");
    }
}

} // namespace throttle
} // namespace xll

#endif // XLL_RTD_ENABLED
