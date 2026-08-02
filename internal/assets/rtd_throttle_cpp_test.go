package assets

import (
	"strings"
	"testing"
)

// The rtd.throttle_interval applier moved out of internal/templates/
// xll_main.cpp.tmpl into include/com/rtd_throttle.h + src/rtd_throttle.cpp on
// 2026-08-03. The ONLY template variable in the whole block was the millisecond
// number — used twice, as the call argument and inside the log string — so it
// became a PARAMETER and everything else is now compiled once as an asset
// instead of being re-emitted, byte-identical, into every RTD project.
//
// WHERE EACH OLD ASSERTION WENT — all of these were in
// internal/generator/gen_cpp_test.go::TestGenCpp_RtdThrottle against
// renderCppMain():
//
//	"static bool SetRtdThrottleInterval(long ms)" -> TestRtdThrottleAppliesThroughTheApplicationObject
//	"SetRtdThrottleInterval(250)"                 -> the generator test keeps the VALUE half:
//	                                                 xll::throttle::TryApplyRtdThrottle(250, "…")
//	"static IDispatch* GetExcelApplication()"     -> TestRtdThrottleAppliesThroughTheApplicationObject
//	                                                 (the asset acquires the Application itself now,
//	                                                 so a throttle-only project no longer emits the
//	                                                 template's wrapper at all — see the generator
//	                                                 test's ribbon-off expectations)
//	`TryApplyRtdThrottle("xlAutoOpen"/"calc end")` -> STAY in the generator test (call sites are wiring)
//
// New here, and untested before the move because it was template text: the 0/1/2
// state gate's exact transitions, the 10-attempt bound, both log strings, and the
// XLL_RTD_ENABLED gating of the TU.

func rtdThrottleSources(t *testing.T) (hdr, code string) {
	t.Helper()
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	h, ok := m["include/com/rtd_throttle.h"]
	if !ok {
		t.Fatalf("embedded asset include/com/rtd_throttle.h not found")
	}
	c, ok := m["src/rtd_throttle.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/rtd_throttle.cpp not found")
	}
	return h, stripCppCommentsAsset(c)
}

// TestRtdThrottleHeaderContract pins the surface the generated TU binds to. The
// millisecond value being a PARAMETER is the whole point of the relocation: if it
// ever went back to a compile-time constant the asset would stop being usable by
// a project whose xll.yaml says something other than that constant.
func TestRtdThrottleHeaderContract(t *testing.T) {
	t.Parallel()
	hdr, _ := rtdThrottleSources(t)

	if !strings.Contains(hdr, "void TryApplyRtdThrottle(long ms, const char* phase);") {
		t.Errorf("com/rtd_throttle.h must declare `void TryApplyRtdThrottle(long ms, const char* phase);` — " +
			"ms is the xll.yaml value and MUST stay a parameter")
	}
	// Namespaced xll::throttle, deliberately NOT xll::rtd: the generated TU does
	// `using namespace xll;` and then calls the TOP-LEVEL rtd:: namespace
	// (rtd::RegisterServer, rtd::ClassFactory, rtd::GlobalModule). An xll::rtd
	// would make every one of those lookups ambiguous in that TU.
	if !strings.Contains(hdr, "namespace throttle {") {
		t.Errorf("com/rtd_throttle.h must live in namespace xll::throttle")
	}
	if strings.Contains(hdr, "namespace rtd {") {
		t.Errorf("com/rtd_throttle.h must NOT open an xll::rtd namespace: the generated TU's " +
			"`using namespace xll;` would make every top-level rtd:: lookup ambiguous")
	}
	// The header refuses to be included from a non-RTD TU, so the failure is at
	// the include rather than an unresolved symbol at link time. Same shape as
	// com/ribbon_connect.h.
	if !strings.Contains(hdr, "#ifndef XLL_RTD_ENABLED") || !strings.Contains(hdr, "#error") {
		t.Errorf("com/rtd_throttle.h must #error when XLL_RTD_ENABLED is absent (its definition is " +
			"compiled only for RTD builds)")
	}
}

// TestRtdThrottleTUIsRtdGated: file(GLOB src/*.cpp) sweeps this TU into EVERY
// generated project, including ones with no RTD. Same gate as
// src/ribbon_connect.cpp / src/scratch_book.cpp.
func TestRtdThrottleTUIsRtdGated(t *testing.T) {
	t.Parallel()
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	raw := m["src/rtd_throttle.cpp"]
	gate := strings.Index(raw, "#ifdef XLL_RTD_ENABLED")
	if gate < 0 {
		t.Fatalf("src/rtd_throttle.cpp must gate its body on #ifdef XLL_RTD_ENABLED " +
			"(the CMake source glob compiles it into non-RTD projects too)")
	}
	// The gate must open BEFORE the header include, or a non-RTD build hits the
	// header's #error instead of compiling to nothing.
	inc := strings.Index(raw, `#include "com/rtd_throttle.h"`)
	if inc < 0 || inc < gate {
		t.Errorf("the com/rtd_throttle.h include must sit INSIDE the XLL_RTD_ENABLED gate "+
			"(gate@%d include@%d): the header #errors when the macro is absent", gate, inc)
	}
	if !strings.Contains(raw, "#endif // XLL_RTD_ENABLED") {
		t.Errorf("src/rtd_throttle.cpp is missing the closing #endif // XLL_RTD_ENABLED")
	}
}

// TestRtdThrottleAppliesThroughTheApplicationObject pins the COM put itself.
// Application.RTD.ThrottleInterval is a per-user, REGISTRY-PERSISTED Excel
// setting: getting the property path wrong does not fail loudly, it just silently
// leaves the user's Excel throttling at 2000 ms (or, worse, writes the value onto
// some other property).
func TestRtdThrottleAppliesThroughTheApplicationObject(t *testing.T) {
	t.Parallel()
	_, code := rtdThrottleSources(t)

	for _, want := range []string{
		// The Application comes from the shared header-only acquisition, not from
		// a generated wrapper — that is what let the template drop its
		// GetExcelApplication / oleacc / dispatch_helpers includes for
		// throttle-only (ribbon-off) projects.
		"IDispatch* pApp = xll::com::AcquireExcelApplication();",
		"if (!pApp) return false;",
		// Application.RTD -> .ThrottleInterval = (VT_I4)ms
		`xll::com::GetProperty(pApp, L"RTD", &vRtd)`,
		"vRtd.vt == VT_DISPATCH && vRtd.pdispVal",
		"vMs.vt = VT_I4;",
		"vMs.lVal = ms;",
		`xll::com::Invoke(vRtd.pdispVal, L"ThrottleInterval", DISPATCH_PROPERTYPUT, { vMs }, nullptr)`,
		// The acquisition contract is "AddRef'd; caller releases".
		"VariantClear(&vRtd);",
		"pApp->Release();",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("src/rtd_throttle.cpp missing %q", want)
		}
	}

	// SetRtdThrottleInterval was `static` in the template; it must stay private to
	// this TU (anonymous namespace) rather than becoming public API — the public
	// surface is the one-shot applier, which owns the state gate.
	if !strings.Contains(code, "namespace {") || !strings.Contains(code, "bool SetRtdThrottleInterval(long ms) {") {
		t.Errorf("SetRtdThrottleInterval must stay TU-private (anonymous namespace), mirroring the " +
			"`static` it had in the template")
	}
	hdr, _ := rtdThrottleSources(t)
	if strings.Contains(hdr, "SetRtdThrottleInterval") {
		t.Errorf("com/rtd_throttle.h must not export SetRtdThrottleInterval; the public entry point is " +
			"the state-gated TryApplyRtdThrottle")
	}
}

// TestRtdThrottleOneShotStateGate pins the retry accounting. xlAutoOpen usually
// runs before any workbook exists (no EXCEL7 child window -> no Application), so
// the put normally FAILS there and the calc-end callbacks are what make it stick.
// Two properties have to hold together: it keeps trying until it succeeds, and it
// stops trying — a window walk on every single calc-end for the life of the
// session is exactly what the bound exists to prevent.
func TestRtdThrottleOneShotStateGate(t *testing.T) {
	t.Parallel()
	_, code := rtdThrottleSources(t)

	for _, want := range []string{
		// 0=pending / 1=applied / 2=gave up, read first so an applied throttle is a
		// cheap no-op on every later calc-end.
		"std::atomic<int> g_rtdThrottleState{0};",
		"if (g_rtdThrottleState.load(std::memory_order_acquire) != 0) return;",
		"g_rtdThrottleState.store(1, std::memory_order_release);",
		"g_rtdThrottleState.store(2, std::memory_order_release);",
		// Bounded at 10 failed attempts.
		"static std::atomic<int> s_attempts{0};",
		"} else if (s_attempts.fetch_add(1) + 1 >= 10) {",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("src/rtd_throttle.cpp missing %q", want)
		}
	}

	// The gate must be the FIRST statement: an applied/gave-up throttle must not
	// pay for the window walk inside SetRtdThrottleInterval.
	const sig = "void TryApplyRtdThrottle(long ms, const char* phase) {"
	fnIdx := strings.Index(code, sig)
	gateIdx := strings.Index(code, "if (g_rtdThrottleState.load(std::memory_order_acquire) != 0) return;")
	if fnIdx < 0 || gateIdx < 0 {
		t.Fatalf("missing markers (fn=%d gate=%d)", fnIdx, gateIdx)
	}
	if between := strings.TrimSpace(code[fnIdx+len(sig) : gateIdx]); between != "" {
		t.Errorf("the 0/1/2 state gate must be the FIRST statement of TryApplyRtdThrottle; found %q "+
			"before it", between)
	}
	// The attempt counter may only be charged on the FAILURE branch: charging a
	// success would be harmless today (the state gate short-circuits) but would
	// make the bound describe something other than "failed attempts".
	successIdx := strings.Index(code, "g_rtdThrottleState.store(1, std::memory_order_release);")
	chargeIdx := strings.Index(code, "s_attempts.fetch_add(1)")
	if successIdx < 0 || chargeIdx < 0 || successIdx > chargeIdx {
		t.Errorf("the 10-attempt bound must be charged in the else (failure) branch only "+
			"(success@%d charge@%d)", successIdx, chargeIdx)
	}

	// Both log strings, verbatim, including the ms interpolation shape. The INFO
	// line used to be built by the template with the number baked into the
	// literal; std::to_string(ms) renders the same decimal (parseTimeout yields an
	// int and config.Validate bounds it to 0..MaxInt32), so the emitted text is
	// unchanged.
	for _, want := range []string{
		`std::string("RTD: ThrottleInterval set to ") + std::to_string(ms) + "ms (" + phase + ")."`,
		`"RTD: could not set ThrottleInterval (Application object unreachable after 10 attempts); keeping the current Excel setting."`,
	} {
		if !strings.Contains(code, want) {
			t.Errorf("src/rtd_throttle.cpp lost the log string %q", want)
		}
	}
}
