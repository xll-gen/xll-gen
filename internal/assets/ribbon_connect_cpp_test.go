package assets

import (
	"strings"
	"testing"
)

// The ribbon COM add-in connect machinery moved out of internal/templates/
// xll_main.cpp.tmpl into include/com/ribbon_connect.h + src/ribbon_connect.cpp on
// 2026-08-02 (it had no template variables in it, so every project was getting a
// byte-identical copy re-emitted into a tree nothing but a golden-string grep
// could test). This file is where the BODY invariants that used to be greps over
// the rendered template now live.
//
// WHERE EACH OLD ASSERTION WENT — every one of these was in
// internal/generator/gen_ribbon_connect_test.go against renderCppMain():
//
//	TestXllMainRibbonDeferredConnect
//	  TryConnectRibbon signature + allowBounce/pOutcome  -> TestRibbonConnectHeaderContract
//	  enum class RibbonAttempt / kNotAttempted           -> TestRibbonConnectHeaderContract
//	  s_inConnect CAS re-entrancy guard                  -> TestRibbonConnectReentrancyGuard
//	  bounce-vs-direct Application acquisition           -> TestRibbonConnectAcquiresAppThroughTheContext
//	  noApp does not burn the give-up budget             -> TestRibbonConnectOutcomeClassification
//	  0/1/2 state gate                                   -> TestRibbonConnectStateGateAndGiveUpCap
//	  removed STA WM_TIMER machinery stays gone          -> TestRibbonConnectHasNoIdleTimerResidual
//	  old one-shot connect log line stays gone           -> TestRibbonConnectStateGateAndGiveUpCap
//	TestXllMainRibbonOnTimeConnectRetry
//	  kRibbonRetryMaxAttempts / kRibbonRetryIntervalSec  -> TestRibbonConnectRetryBudgetConstants
//	TestXllMainRibbonRetryNoAppBudget
//	  the separate noApp budget + its counters           -> TestRibbonConnectRetryBudgetConstants
//	  the pre-fix single s_retryAttempts counter is gone -> TestRibbonConnectRetryBudgetConstants
//	TestXllMainRibbonRetryUnattemptedNotCharged
//	  all four classification sites + their ORDER        -> TestRibbonConnectOutcomeClassification
//	TestXllMainRibbonRetryArmRcAndSingleChain
//	  file-scope (not function-local) chain counters     -> TestRibbonConnectRetryBudgetConstants
//
// What stayed in internal/generator is the WIRING: that a ribbon project includes
// the header, publishes a fully-populated ConnectContext before the teardown hook
// is registered, calls TryConnectRibbon from the three trigger sites, and charges
// the budgets from the exported OnTime macro — plus a guard that the body did not
// get re-inlined into the template. See
// internal/generator/gen_ribbon_connect_test.go.

// ribbonConnectSources returns the header and the implementation, with comments
// stripped from the implementation. Stripping is load-bearing for the ORDER
// asserts: the moved comments deliberately NAME the classification classes out of
// code order (the kNotAttempted rationale is written above the enum, the noApp
// rationale above the constants), so an index comparison over raw text would
// report false failures. The header keeps its comments because a few assertions
// are about the documented contract itself.
func ribbonConnectSources(t *testing.T) (hdr, code string) {
	t.Helper()
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	h, ok := m["include/com/ribbon_connect.h"]
	if !ok {
		t.Fatalf("embedded asset include/com/ribbon_connect.h not found")
	}
	c, ok := m["src/ribbon_connect.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/ribbon_connect.cpp not found")
	}
	return h, stripCppCommentsAsset(c)
}

// TestRibbonConnectHeaderContract pins the public surface the generated TU binds
// to. A silent signature change here does not break the C++ build in an obvious
// place — it breaks the OnTime retry's budget accounting, which is only visible as
// "the ribbon tab takes ten minutes to appear".
func TestRibbonConnectHeaderContract(t *testing.T) {
	t.Parallel()
	hdr, _ := ribbonConnectSources(t)

	for _, want := range []string{
		// The retryable connect helper, with allowBounce and the outcome-class
		// out-param both DEFAULTED here (the template's calc-end call sites pass
		// neither, so the defaults are part of the contract).
		"bool TryConnectRibbon(const char* phase, bool allowBounce = false,",
		"RibbonAttempt* pOutcome = nullptr);",
		// The disconnect half the graceful teardown hook calls, same defaults.
		"bool SetRibbonConnected(bool connected, bool* pNoApp = nullptr, bool allowBounce = false);",
		// The outcome classes are an enum class: a bool cannot express the third,
		// UNCHARGEABLE class, which is the whole point of the 2026-07-26 fix.
		"enum class RibbonAttempt {",
		"kNotAttempted = 0,",
		"kNoApp,",
		"kRejected,",
		"kConnected,",
		// The injected context and its publisher.
		"struct ConnectContext {",
		"void SetConnectContext(const ConnectContext& ctx);",
	} {
		if !strings.Contains(hdr, want) {
			t.Errorf("com/ribbon_connect.h missing %q", want)
		}
	}

	// Every field the generated TU has to fill. A field added here without a
	// matching assignment in xll_main.cpp.tmpl silently leaves a null pointer in
	// the COM registration path; the generator-side counterpart of this list is
	// TestXllMainPublishesRibbonConnectContext.
	for _, field := range []string{
		"hModule", "progId", "clsid", "comFriendlyName", "addinFriendlyName",
		"addinDescription", "ribbonXml", "getImages", "acquireApp",
		"acquireAppOrBounce", "pClassObjectCookie",
	} {
		if !strings.Contains(hdr, field) {
			t.Errorf("ConnectContext is missing the %q field", field)
		}
	}

	// getImages must stay a FUNCTION POINTER, not a materialized vector: today the
	// generated GetXllRibbonImages() runs lazily inside the one-time registration
	// block, at most once. Storing the vector in the context would build and copy
	// every embedded icon at xlAutoOpen even on a load that never registers.
	if !strings.Contains(hdr, "std::vector<RibbonImage> (*getImages)()") {
		t.Errorf("ConnectContext.getImages must be a function pointer so the embedded icons are " +
			"still built lazily, at most once, inside the registration block")
	}

	// The retry-chain state must be EXPORTED (extern), not private to the .cpp:
	// the exported __xllgen_RibbonConnectRetry macro and the xlAutoOpen arm site
	// both have to stay in the generated TU and both read/charge these.
	for _, want := range []string{
		"extern std::atomic<int>  g_ribbonConnectState;",
		"extern std::atomic<bool> g_ribbonRegistered;",
		"extern std::atomic<bool> g_ribbonRetryArmed;",
		"extern std::atomic<int>  g_ribbonRetryAttempts;",
		"extern std::atomic<int>  g_ribbonRetryNoAppAttempts;",
	} {
		if !strings.Contains(hdr, want) {
			t.Errorf("com/ribbon_connect.h must declare %q", want)
		}
	}
}

// TestRibbonConnectTUIsRibbonGated: file(GLOB src/*.cpp) sweeps this TU into
// EVERY generated project, including ones with no ribbon. Its body instantiates
// rtd::ClassFactory<RibbonAddIn>, and RibbonAddIn is itself declared only under
// XLL_RIBBON_ENABLED (com/ribbon_addin.h), so an ungated body fails to compile in
// every non-ribbon project. Same gate as src/ribbon_addin.cpp and
// src/scratch_book.cpp.
func TestRibbonConnectTUIsRibbonGated(t *testing.T) {
	t.Parallel()
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	raw := m["src/ribbon_connect.cpp"]
	gate := strings.Index(raw, "#ifdef XLL_RIBBON_ENABLED")
	if gate < 0 {
		t.Fatalf("src/ribbon_connect.cpp must gate its body on #ifdef XLL_RIBBON_ENABLED " +
			"(the CMake source glob compiles it into non-ribbon projects too)")
	}
	if body := strings.Index(raw, "namespace ribbon {"); body < 0 || body < gate {
		t.Errorf("the namespace body must sit INSIDE the XLL_RIBBON_ENABLED gate (gate@%d body@%d)", gate, body)
	}
	if !strings.Contains(raw, "#endif // XLL_RIBBON_ENABLED") {
		t.Errorf("src/ribbon_connect.cpp is missing the closing #endif // XLL_RIBBON_ENABLED")
	}
}

// TestRibbonConnectAcquiresAppThroughTheContext pins the allowBounce routing.
// GetExcelApplicationOrBounce STAYS in the template (its body is
// ribbon.bounce-branched: full / keep-open / off), so it arrives as a function
// pointer. If the two pointers were ever swapped, or if allowBounce stopped
// selecting between them, the calc-end and OnTime retries would start creating
// scratch workbooks from a worksheet/event context — where the xlc* command
// opcodes are not even legal (AGENTS.md §20.3).
func TestRibbonConnectAcquiresAppThroughTheContext(t *testing.T) {
	t.Parallel()
	_, code := ribbonConnectSources(t)

	if !strings.Contains(code, "allowBounce ? g_ctx.acquireAppOrBounce() : g_ctx.acquireApp();") {
		t.Errorf("SetRibbonConnected must select the bouncing acquisition ONLY when allowBounce is " +
			"true (want `allowBounce ? g_ctx.acquireAppOrBounce() : g_ctx.acquireApp();`)")
	}
	// A null Application is reported as the noApp class through pNoApp, never as an
	// ordinary rejection — that distinction is what keeps an empty Excel off the
	// 30-attempt give-up budget.
	if !strings.Contains(code, "if (!pApp) {") || !strings.Contains(code, "if (pNoApp) *pNoApp = true;") {
		t.Errorf("SetRibbonConnected must report an unreachable Application object via pNoApp:\n%s", code)
	}
	// The ProgID comes from the context, not from a hard-coded literal.
	if !strings.Contains(code, "vProg.bstrVal = SysAllocString(g_ctx.progId);") {
		t.Errorf("the COMAddIns Item lookup must use the injected ProgID")
	}
	// The BSTR is still freed by the caller per the dispatch_helpers contract.
	if !strings.Contains(code, "VariantClear(&vProg);") {
		t.Errorf("SetRibbonConnected leaks the ProgID BSTR (VariantClear(&vProg) is gone)")
	}
	// The class-object cookie is written THROUGH the context pointer. It stays a
	// global in the generated TU on purpose: the teardown hook revokes it, and that
	// hook's statement order is the fix for a 100%-reproducible mso.dll NULL-vtable
	// crash (see office_disconnect_guard_cpp_test.go) that a relocation must not
	// perturb. See "COOKIE OWNERSHIP" in com/ribbon_connect.h.
	if !strings.Contains(code, "REGCLS_MULTIPLEUSE, g_ctx.pClassObjectCookie)") {
		t.Errorf("CoRegisterClassObject must publish the cookie through ConnectContext.pClassObjectCookie")
	}
}

// TestRibbonConnectStateGateAndGiveUpCap pins the 0/1/2 state gate, the teardown
// gate and the 60-attempt cap.
func TestRibbonConnectStateGateAndGiveUpCap(t *testing.T) {
	t.Parallel()
	_, code := ribbonConnectSources(t)

	for _, want := range []string{
		// 0=pending / 1=connected / 2=gave up, checked first so a resolved connect
		// is a cheap no-op for the calc-end and OnTime retries.
		"if (g_ribbonConnectState.load(std::memory_order_acquire) != 0) return true;",
		"g_ribbonConnectState.store(1, std::memory_order_release);",
		"g_ribbonConnectState.store(2, std::memory_order_release);",
		// TeardownStarted(), NOT g_isUnloading alone: a confirmed teardown latches
		// g_isQuiescing in Phase 1 and deliberately keeps g_isUnloading FALSE across
		// Excel's whole RTD handshake (AGENTS.md §20.2.1 rule 2).
		"if (xll::TeardownStarted()) return false;",
		// The one-time registration is skipped on retries.
		"if (!g_ribbonRegistered.load(std::memory_order_acquire)) {",
		"g_ribbonRegistered.store(true, std::memory_order_release);",
		// Bounded give-up on REAL connect rejections.
		"if (s_attempts.fetch_add(1) + 1 >= 60) {",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("src/ribbon_connect.cpp missing %q", want)
		}
	}

	// The give-up gate must be checked BEFORE the teardown gate: a resolved
	// connect has nothing to do either way, and swapping them would make a
	// teardown-time call return false where it used to return true.
	stateIdx := strings.Index(code, "if (g_ribbonConnectState.load(std::memory_order_acquire) != 0) return true;")
	teardownIdx := strings.Index(code, "if (xll::TeardownStarted()) return false;")
	if stateIdx < 0 || teardownIdx < 0 || stateIdx > teardownIdx {
		t.Errorf("the state gate must precede the teardown gate (state@%d teardown@%d)", stateIdx, teardownIdx)
	}

	// Every documented log string, verbatim. These are the only externally
	// observable evidence of each failure class, and the relocation preserved them.
	for _, want := range []string{
		"Ribbon: HKCU COM registration failed (locked-down registry?); ribbon UI disabled.",
		"Ribbon: Office Addins key registration failed; ribbon UI disabled.",
		"Ribbon: CoRegisterClassObject failed: ",
		"Ribbon: COM add-in connected (",
		"Ribbon: COMAddIns connect failed after 60 attempts; ribbon UI disabled.",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("src/ribbon_connect.cpp lost the log string %q", want)
		}
	}
	// The PRE-retry one-shot failure line must stay gone: it was the shape that had
	// no retry path at all.
	if strings.Contains(code, "Ribbon: COMAddIns connect failed; ribbon UI disabled.") {
		t.Errorf("the old one-shot connect failure log line is back (that shape had no retry path)")
	}
}

// TestRibbonConnectReentrancyGuard pins the STA re-entrancy guard (MED hardening,
// AGENTS.md §20.3). The temp-workbook bounce (xlcNew/xlcWorkbookInsert) and the
// COMAddIns Connect both PUMP the STA message loop, so Excel can dispatch a
// CalculationEnded callback or a queued OnTime macro while a connect is mid-flight;
// that dispatch re-enters this function ON THE SAME THREAD and would otherwise
// reach a second concurrent COMAddIns…Connect.
func TestRibbonConnectReentrancyGuard(t *testing.T) {
	t.Parallel()
	_, code := ribbonConnectSources(t)

	for _, want := range []string{
		"static std::atomic<bool> s_inConnect{false};",
		"if (!s_inConnect.compare_exchange_strong(expected, true)) return false;",
		// RAII release, so every return path (including the registration bails)
		// clears the flag.
		"~ConnectGuard() { flag.store(false, std::memory_order_release); }",
		"} connectGuard{s_inConnect};",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("src/ribbon_connect.cpp missing re-entrancy guard piece %q", want)
		}
	}
}

// TestRibbonConnectOutcomeClassification is the pin for the LOW accounting fix of
// 2026-07-26 (§23.6 "§3 FOLLOW-UP #2"), plus the MED noApp split before it.
//
// TryConnectRibbon has exits that return BEFORE ever calling SetRibbonConnected —
// the teardown bail and, very reachably, the STA re-entrancy bail. Before the fix
// both reported themselves as an ordinary failure, so the OnTime runner charged
// one of its 30 productive attempts for a connect that never happened. The
// out-param is defaulted to kNotAttempted at the top, so an exit that forgets to
// classify itself is UNCHARGED rather than MIS-charged.
func TestRibbonConnectOutcomeClassification(t *testing.T) {
	t.Parallel()
	_, code := ribbonConnectSources(t)

	for _, want := range []string{
		"if (pOutcome) *pOutcome = RibbonAttempt::kNotAttempted;",
		"if (pOutcome) *pOutcome = RibbonAttempt::kNoApp;",
		"if (pOutcome) *pOutcome = RibbonAttempt::kRejected;",
		"if (pOutcome) *pOutcome = RibbonAttempt::kConnected;",
		// noApp is detected via the bool out-param of SetRibbonConnected and
		// returns WITHOUT touching the give-up budget.
		"bool noApp = false;",
		"if (noApp) {",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("src/ribbon_connect.cpp missing %q", want)
		}
	}

	// The default MUST be the very first statement of the function: any earlier
	// return would otherwise leave a caller's variable untouched.
	fnIdx := strings.Index(code, "bool TryConnectRibbon(const char* phase, bool allowBounce, RibbonAttempt* pOutcome) {")
	defaultIdx := strings.Index(code, "if (pOutcome) *pOutcome = RibbonAttempt::kNotAttempted;")
	if fnIdx < 0 || defaultIdx < 0 {
		t.Fatalf("missing markers (fn=%d default=%d)", fnIdx, defaultIdx)
	}
	if between := strings.TrimSpace(code[fnIdx+len("bool TryConnectRibbon(const char* phase, bool allowBounce, RibbonAttempt* pOutcome) {") : defaultIdx]); between != "" {
		t.Errorf("the kNotAttempted default must be the FIRST statement of TryConnectRibbon; found %q before it", between)
	}

	// kRejected must be classified at the site that actually RAN the Connect:
	// after SetRibbonConnected returned false and after the noApp early return.
	// Anything earlier re-introduces the mis-billing.
	connectIdx := strings.Index(code, "if (SetRibbonConnected(true, &noApp, allowBounce)) {")
	noAppIdx := strings.Index(code, "if (pOutcome) *pOutcome = RibbonAttempt::kNoApp;")
	rejectedIdx := strings.Index(code, "if (pOutcome) *pOutcome = RibbonAttempt::kRejected;")
	reentryIdx := strings.Index(code, "if (!s_inConnect.compare_exchange_strong(expected, true)) return false;")
	if connectIdx < 0 || noAppIdx < 0 || rejectedIdx < 0 || reentryIdx < 0 {
		t.Fatalf("missing markers (connect=%d noApp=%d rejected=%d reentry=%d)",
			connectIdx, noAppIdx, rejectedIdx, reentryIdx)
	}
	if !(reentryIdx < connectIdx && connectIdx < noAppIdx && noAppIdx < rejectedIdx) {
		t.Errorf("outcome classification is out of order (reentry=%d connect=%d noApp=%d rejected=%d): "+
			"kRejected must only be reported after an actual SetRibbonConnected attempt",
			reentryIdx, connectIdx, noAppIdx, rejectedIdx)
	}
	// The re-entrancy bail must NOT classify itself as anything but the default.
	between := code[reentryIdx:connectIdx]
	if strings.Contains(between, "*pOutcome = RibbonAttempt::kRejected") ||
		strings.Contains(between, "*pOutcome = RibbonAttempt::kNoApp") {
		t.Errorf("the STA re-entrancy bail must stay kNotAttempted (charge nothing)")
	}
	// Neither may the teardown bail.
	teardownIdx := strings.Index(code, "if (xll::TeardownStarted()) return false;")
	if teardownIdx < 0 || teardownIdx > reentryIdx {
		t.Fatalf("teardown bail not found before the re-entrancy bail (teardown=%d reentry=%d)", teardownIdx, reentryIdx)
	}
	if head := code[:reentryIdx]; strings.Contains(head, "*pOutcome = RibbonAttempt::kRejected") ||
		strings.Contains(head, "*pOutcome = RibbonAttempt::kNoApp") {
		t.Errorf("nothing before the re-entrancy bail may classify a chargeable outcome " +
			"(the teardown / unpublished-context bails attempted no connect)")
	}
}

// TestRibbonConnectRetryBudgetConstants pins the two-budget split (MED fix,
// 2026-07-26) and the file-scope chain counters (MED #2(d)).
//
// The values are asserted, not just the names: the noApp window is
// 15*2 s + 60*10 s = 630 s by construction, and a silent edit to any of the five
// numbers changes how long an empty Excel keeps looking for a workbook — which is
// exactly the behavior the §3 FOLLOW-UP was written to fix and to bound.
func TestRibbonConnectRetryBudgetConstants(t *testing.T) {
	t.Parallel()
	hdr, code := ribbonConnectSources(t)

	for _, want := range []string{
		"inline constexpr int    kRibbonRetryMaxAttempts       = 30;",
		"inline constexpr double kRibbonRetryIntervalSec       = 2.0;",
		"inline constexpr int    kRibbonRetryNoAppFastAttempts = 15;",
		"inline constexpr double kRibbonRetryNoAppIdleSec      = 10.0;",
		"inline constexpr int    kRibbonRetryNoAppMaxAttempts  = 75;",
	} {
		if !strings.Contains(hdr, want) {
			t.Errorf("com/ribbon_connect.h missing retry-budget constant %q", want)
		}
	}

	// File-scope definitions, NOT function-local statics: two chains armed in one
	// process generation (probe-unload-reuse, or add-in disable->enable without a
	// DLL unload) used to share a function-local counter — double the dispatch
	// rate, half the effective budget each.
	for _, want := range []string{
		"std::atomic<bool> g_ribbonRetryArmed{false};",
		"std::atomic<int>  g_ribbonRetryAttempts{0};",
		"std::atomic<int>  g_ribbonRetryNoAppAttempts{0};",
		"std::atomic<int>  g_ribbonConnectState{0};",
		"std::atomic<bool> g_ribbonRegistered{false};",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("src/ribbon_connect.cpp must define %q at namespace scope", want)
		}
	}

	// The pre-fix shape: ONE counter charged for every outcome, with no noApp
	// class. Its return makes an empty Excel burn all 30 attempts in 60 s again.
	if strings.Contains(code, "s_retryAttempts") || strings.Contains(hdr, "s_retryAttempts") {
		t.Errorf("the pre-fix single-budget retry counter s_retryAttempts is back; a noApp attempt " +
			"must not consume the connect-failure budget")
	}
	// Neither budget may become unbounded.
	if strings.Contains(code, "for (;;)") || strings.Contains(code, "while (true)") {
		t.Errorf("the ribbon connect path must never spin unbounded")
	}
}

// TestRibbonConnectHasNoIdleTimerResidual: the original no-workbook fix retried on
// a Win32 thread timer (SetTimer(NULL, …) + RibbonConnectTimerProc). "Leak, don't
// crash" (AGENTS.md §20.2) does NOT transfer to a timer: a leaked TimerProc is a
// raw code pointer INTO the DLL that the OS dispatches on the next WM_TIMER, so
// after a forced FreeLibrary it is a guaranteed 0xC0000005 — and the
// g_isUnloading guard inside the proc cannot help, because the guard itself is
// unmapped code. KillTimer could only run from the owning STA thread, so a
// DllMain disarm was impossible. It was replaced by the synchronous
// temp-workbook bounce plus the xlcOnTime macro chain (an Excel-dispatched macro,
// which Excel un-registers on unload). None of it may come back.
func TestRibbonConnectHasNoIdleTimerResidual(t *testing.T) {
	t.Parallel()
	hdr, code := ribbonConnectSources(t)
	for _, gone := range []string{
		"ArmRibbonConnectTimer",
		"StopRibbonConnectTimer",
		"RibbonConnectTimerProc",
		"g_ribbonConnectTimer",
		"kRibbonConnectTimerId",
		"kRibbonConnectTimerMs",
		"SetTimer(",
		"KillTimer(",
	} {
		if strings.Contains(code, gone) || strings.Contains(hdr, gone) {
			t.Errorf("ribbon_connect still contains removed STA timer symbol %q "+
				"(the §20.2 unmap-crash residual must stay gone)", gone)
		}
	}
}

// TestRibbonConnectContextReadyIsTerminal pins the two xll-cpp-reviewer MED
// findings on the ContextReady bail (2026-08-02). Nothing covered this branch
// when it was introduced — the outcome-classification test above would pass with
// ContextReady deleted entirely.
func TestRibbonConnectContextReadyIsTerminal(t *testing.T) {
	t.Parallel()
	_, code := ribbonConnectSources(t)

	// MED-1. Every OTHER kNotAttempted exit is self-limiting: the unload bail
	// resolves as teardown proceeds, and the STA re-entrancy bail requires an
	// outer attempt in flight that will itself produce a real outcome. An
	// unpublished context is permanent, so if the bail does not latch the state
	// gate, __xllgen_RibbonConnectRetry charges nothing, sees state stay 0, and
	// re-arms itself every kRibbonRetryIntervalSec for the whole Excel session.
	idx := strings.Index(code, `if (!ContextReady("TryConnectRibbon"))`)
	if idx < 0 {
		t.Fatal("TryConnectRibbon no longer guards on ContextReady")
	}
	tail := code[idx:]
	end := strings.Index(tail, "\n    }")
	if end < 0 {
		t.Fatal("could not delimit the ContextReady bail")
	}
	if !strings.Contains(tail[:end], "g_ribbonConnectState.store(2, std::memory_order_release)") {
		t.Errorf("the unpublished-context bail does not latch the give-up state, so the "+
			"OnTime retry chain would re-arm forever against a permanent condition:\n%s", tail[:end])
	}

	// MED-2. hModule is the one context field whose absence does not fail loudly:
	// it flows into rtd::RegisterServer, where a null HMODULE makes
	// GetModuleFileNameW resolve to the HOST path, so the HKCU InprocServer32 for
	// our CLSID gets written pointing at EXCEL.EXE — a persistent, user-scope
	// registry entry that outlives the session.
	ready := code[strings.Index(code, "bool ContextReady(const char* site) {"):]
	ready = ready[:strings.Index(ready, "return true;")]
	if !strings.Contains(ready, "g_ctx.hModule") {
		t.Errorf("ContextReady does not check hModule; an unwired module handle would "+
			"silently register EXCEL.EXE as the ribbon InprocServer32:\n%s", ready)
	}
}
