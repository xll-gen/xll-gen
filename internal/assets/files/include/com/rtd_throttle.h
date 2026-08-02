#pragma once
// rtd_throttle.h — the rtd.throttle_interval applier.
//
// Excel throttles how often it asks an RTD server for fresh values
// (Application.RTD.ThrottleInterval, default 2000 ms). A project that wants a
// faster stream opts in with `rtd.throttle_interval` in xll.yaml and the XLL
// puts the value at load.
//
// WHY IT LIVES HERE. All of it was inline in xll_main.cpp.tmpl and the ONLY
// template variable in the whole block was the millisecond number itself — used
// twice, as the call argument and inside the log string. Everything else was
// byte-identical in every generated project, re-emitted into a tree where
// nothing but a golden-string grep could test it. The number is now a PARAMETER,
// so the logic is compiled once as an ordinary asset and the template supplies
// only the value it always supplied.
//
// This is a PURE RELOCATION (2026-08-03). The 0/1/2 state gate, the 10-attempt
// bound, the acquire/release ordering and both log strings are exactly what
// shipped; the log text is rebuilt with std::to_string(ms), which renders the
// same decimal the template used to inline (parseTimeout yields an int, and
// config.Validate bounds it to 0..MaxInt32).
//
// GATING: src/rtd_throttle.cpp compiles its body only under XLL_RTD_ENABLED
// (CMake defines it project-wide for RTD builds), because file(GLOB src/*.cpp)
// sweeps this TU into EVERY generated project. Same pattern as
// src/ribbon_connect.cpp / src/scratch_book.cpp. A project with RTD enabled but
// no rtd.throttle_interval compiles the TU and simply never calls it.
//
// THREADING: STA only. xlAutoOpen and the calc-end callbacks all run on Excel's
// main STA thread; the atomics are one-shot / idempotence guards for that ONE
// thread, not cross-thread synchronization.

// GATING. The definition lives in src/rtd_throttle.cpp, which is
// #ifdef XLL_RTD_ENABLED. Declaring it from a non-RTD TU would compile and then
// fail at LINK with an unresolved xll::throttle::TryApplyRtdThrottle — a
// diagnostic one step removed from the cause. Fail at the include instead, the
// way com/ribbon_connect.h does.
#ifndef XLL_RTD_ENABLED
#error "com/rtd_throttle.h requires XLL_RTD_ENABLED (its definition is compiled only for RTD builds)"
#endif

namespace xll {
namespace throttle {

// One-shot apply with bounded retries. At xlAutoOpen the native object model
// is often not reachable yet (no workbook -> no EXCEL7 child window: early
// startup loads AND automation hosts hit this), so the calc-end callbacks —
// which run on the main STA thread once a workbook exists — retry until it
// sticks. Bounded so a pathological host doesn't pay the window walk forever.
//
// ms is the rtd.throttle_interval value from xll.yaml, in milliseconds; phase
// names the call site for the log line ("xlAutoOpen", "calc end", "calc event").
void TryApplyRtdThrottle(long ms, const char* phase);

} // namespace throttle
} // namespace xll
