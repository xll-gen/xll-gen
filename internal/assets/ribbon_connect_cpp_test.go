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
// SECOND PASS, 2026-08-03 — the OnTime retry CHAIN followed. The exported symbol
// __xllgen_RibbonConnectRetry has to stay in the generated TU (Excel resolves the
// ON.TIME procedure by NAME against an exported entry point, AGENTS.md §21), but
// its BODY had no template variables either, and neither did the xlAutoOpen arm
// block. They are now xll::ribbon::RunConnectRetryTick() and
// xll::ribbon::ArmConnectRetry(); the template keeps a thin SEH-wrapped shim and a
// one-line call. A second batch of assertions moved with them:
//
//	TestXllMainRibbonOnTimeConnectRetry
//	  the tick's teardown self-abort + state gate        -> TestRibbonConnectRetryTickGates
//	  the re-arm through ScheduleOnTimeMacro             -> TestRibbonConnectRetryTickReArm
//	TestXllMainRibbonRetryNoAppBudget
//	  which class charges which counter, both hard stops -> TestRibbonConnectRetryTickBudgetAccounting
//	  the three branches and their ORDER                 -> TestRibbonConnectRetryTickBudgetAccounting
//	  the pre-fix `bool retryNoApp` shape stays gone     -> TestRibbonConnectRetryTickBudgetAccounting
//	TestXllMainRibbonRetryArmRcAndSingleChain
//	  the START-ONCE CAS at the arm site                 -> TestRibbonConnectArmIsStartOnceAndInspectsRc
//	  both arm sites inspecting the xlcOnTime rc         -> TestRibbonConnectArmIsStartOnceAndInspectsRc
//	                                                        (+ TestRibbonConnectRetryTickReArm)
//
// What stayed in internal/generator is the WIRING: that a ribbon project includes
// the header, publishes a fully-populated ConnectContext before the teardown hook
// is registered, calls TryConnectRibbon from the three trigger sites, exports the
// retry macro under the name the header literal dictates and routes it straight
// into the tick, and arms the chain from xlAutoOpen (the only VALID command
// context for that first xlcOnTime) — plus a guard that the body did not get
// re-inlined into the template. See
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

// rawRibbonConnectImpl returns src/ribbon_connect.cpp WITH its comments, for the
// handful of assertions that are about a comment (an argument-name comment on a
// bare bool literal is documentation the compiler cannot check).
func rawRibbonConnectImpl(t *testing.T) string {
	t.Helper()
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	return m["src/ribbon_connect.cpp"]
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
		// The disconnect half the graceful teardown hook calls, same defaults. The
		// pFault out-param (2026-08-03, backlog line 121) is defaulted for the same
		// reason: the teardown hook's SetRibbonConnected(false) passes neither it
		// nor pNoApp.
		"bool SetRibbonConnected(bool connected, bool* pNoApp = nullptr, bool allowBounce = false,",
		"RibbonConnectFault* pFault = nullptr);",
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
	connectIdx := strings.Index(code, "if (SetRibbonConnected(true, &noApp, allowBounce, &fault)) {")
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

// TestRibbonConnectRetryTickGates pins the two gates that make a leaked OnTime
// dispatch harmless and stop the chain once the connect resolves.
//
// The teardown gate is the load-bearing one. A schedule placed before teardown can
// still be dispatched by Excel afterwards; if the tick then touched Excel or
// re-armed, it would be doing COM work on a half-torn-down add-in and extending
// the chain past the point where anything can consume it. Note it is
// TeardownStarted(), not g_isUnloading: Phase 1 latches g_isQuiescing and
// deliberately keeps g_isUnloading FALSE across Excel's whole RTD handshake
// (AGENTS.md §20.2.1 rule 2), so the unload flag alone is not a teardown test.
func TestRibbonConnectRetryTickGates(t *testing.T) {
	t.Parallel()
	_, code := ribbonConnectSources(t)

	tick := retryTickBody(t, code)

	for _, want := range []string{
		"if (xll::TeardownStarted()) return;",
		// The /*allowBounce=*/ argument comment is stripped along with every other
		// comment, so the expectation is the bare argument.
		`TryConnectRibbon("ontime retry", false, &retryOutcome);`,
		"RibbonAttempt retryOutcome = RibbonAttempt::kNotAttempted;",
		"if (g_ribbonConnectState.load(std::memory_order_acquire) != 0) return;",
	} {
		if !strings.Contains(tick, want) {
			t.Errorf("RunConnectRetryTick missing %q:\n%s", want, tick)
		}
	}

	// ORDER: the teardown self-abort must come BEFORE the connect attempt (it must
	// not touch Excel at all during teardown), and the state gate must come after
	// the attempt (that attempt is what can resolve the state).
	teardownIdx := strings.Index(tick, "if (xll::TeardownStarted()) return;")
	connectIdx := strings.Index(tick, `TryConnectRibbon("ontime retry"`)
	stateIdx := strings.Index(tick, "if (g_ribbonConnectState.load(std::memory_order_acquire) != 0) return;")
	if !(teardownIdx >= 0 && teardownIdx < connectIdx && connectIdx < stateIdx) {
		t.Errorf("RunConnectRetryTick gates are out of order (teardown=%d connect=%d state=%d)",
			teardownIdx, connectIdx, stateIdx)
	}

	// The retry must never bounce: the temp-workbook bounce issues xlcNew /
	// xlcFileClose, and this dispatch is not the xlAutoOpen first-attempt path the
	// bounce is scoped to (AGENTS.md §20.3).
	if strings.Contains(tick, `TryConnectRibbon("ontime retry", true`) {
		t.Errorf("the OnTime retry must not enable the temp-workbook bounce")
	}
	// The RAW source must still carry the /*allowBounce=*/ argument comment: a
	// bare `false` at a three-argument call site is exactly the kind of literal
	// that gets flipped by accident.
	if raw := rawRibbonConnectImpl(t); !strings.Contains(raw, `TryConnectRibbon("ontime retry", /*allowBounce=*/false, &retryOutcome);`) {
		t.Errorf("the retry's TryConnectRibbon call must keep its /*allowBounce=*/ argument comment")
	}
}

// TestRibbonConnectRetryTickBudgetAccounting is the pin for the MED noApp budget
// split (2026-07-26) and the LOW kNotAttempted hole (§23.6 "§3 FOLLOW-UP #2"),
// both of which are now asset code.
//
// The defect both fixed is the same shape: charging a budget for an attempt that
// was not a connect FAILURE. Charging noApp made an empty Excel burn all 30
// productive attempts in 60 s and miss the workbook opened at t≈90 s; charging the
// STA re-entrancy bail did the same, one branch further out, for a dispatch that
// never touched COM.
func TestRibbonConnectRetryTickBudgetAccounting(t *testing.T) {
	t.Parallel()
	_, code := ribbonConnectSources(t)
	tick := retryTickBody(t, code)

	for _, want := range []string{
		// noApp -> the separate noApp counter and its own hard stop…
		"if (retryOutcome == RibbonAttempt::kNoApp) {",
		"int n = g_ribbonRetryNoAppAttempts.fetch_add(1) + 1;",
		"if (n >= kRibbonRetryNoAppMaxAttempts) {",
		// …and the poll relaxes once the fast window is spent (still bounded).
		"if (n >= kRibbonRetryNoAppFastAttempts) nextDelaySec = kRibbonRetryNoAppIdleSec;",
		// rejected -> the productive counter and its hard stop.
		"} else if (retryOutcome == RibbonAttempt::kRejected) {",
		"int n = g_ribbonRetryAttempts.fetch_add(1) + 1;",
		"if (n >= kRibbonRetryMaxAttempts) {",
		// The default spacing is the productive one.
		"double nextDelaySec = kRibbonRetryIntervalSec;",
	} {
		if !strings.Contains(tick, want) {
			t.Errorf("RunConnectRetryTick missing budget-accounting piece %q:\n%s", want, tick)
		}
	}

	// The three branches must be exhaustive and in the documented order. A missing
	// else would make kNotAttempted fall into a chargeable branch again.
	noAppIdx := strings.Index(tick, "if (retryOutcome == RibbonAttempt::kNoApp) {")
	rejIdx := strings.Index(tick, "} else if (retryOutcome == RibbonAttempt::kRejected) {")
	elseIdx := strings.Index(tick, "Ribbon: OnTime connect retry re-entered while a connect was in flight; ")
	if noAppIdx < 0 || rejIdx < 0 || elseIdx < 0 || !(noAppIdx < rejIdx && rejIdx < elseIdx) {
		t.Errorf("the tick's outcome branches are missing or out of order (noApp=%d rejected=%d uncharged=%d)",
			noAppIdx, rejIdx, elseIdx)
	}

	// The uncharged branch must charge NOTHING. Slice it out and prove no counter
	// is touched inside it — the substring assertions above would still pass if a
	// fetch_add were added here.
	unchargedIdx := strings.LastIndex(tick[:elseIdx], "} else {")
	if unchargedIdx < 0 {
		t.Fatalf("could not locate the kNotAttempted else branch")
	}
	uncharged := tick[unchargedIdx:elseIdx]
	if strings.Contains(uncharged, "fetch_add") {
		t.Errorf("the kNotAttempted branch must charge no budget at all:\n%s", uncharged)
	}

	// Every log string that reports a terminal state, verbatim: these are the only
	// externally observable evidence that the chain stopped and WHY.
	for _, want := range []string{
		"Ribbon: OnTime connect retry gave up waiting for a workbook to be opened ",
		"Ribbon: OnTime connect retry exhausted its bounded budget; the calc-end fallback remains the only trigger.",
		"Ribbon: OnTime connect retry re-entered while a connect was in flight; ",
	} {
		if !strings.Contains(tick, want) {
			t.Errorf("RunConnectRetryTick lost the log string %q", want)
		}
	}

	// The pre-fix shapes: a single unconditional charge, and the bool out-param
	// that could not express "nothing was attempted".
	for _, gone := range []string{"s_retryAttempts", "bool retryNoApp", "if (retryNoApp)"} {
		if strings.Contains(code, gone) {
			t.Errorf("src/ribbon_connect.cpp still contains the pre-fix retry accounting shape %q", gone)
		}
	}
}

// TestRibbonConnectRetryTickReArm pins the self-re-arm and, critically, that its
// rc is INSPECTED. A rejected xlcOnTime silently ENDS the chain: without the log
// line it is indistinguishable from "armed and still retrying", which is how a
// missing ribbon tab stays invisible until a user reports it.
func TestRibbonConnectRetryTickReArm(t *testing.T) {
	t.Parallel()
	_, code := ribbonConnectSources(t)
	tick := retryTickBody(t, code)

	for _, want := range []string{
		"int reArmRc = xll::ScheduleOnTimeMacro(xll::RibbonConnectRetryMacroName(), nextDelaySec);",
		"if (reArmRc != xlretSuccess) {",
		"g_ribbonRetryArmed.store(false, std::memory_order_release);",
		"Ribbon: OnTime connect retry could not re-arm (xlcOnTime rc=",
	} {
		if !strings.Contains(tick, want) {
			t.Errorf("RunConnectRetryTick missing re-arm piece %q:\n%s", want, tick)
		}
	}
	// The macro name comes from the single-source accessor, never a re-typed
	// literal (item 2e, 2026-07-26: a one-sided rename compiles and only fails at
	// runtime as an unresolvable ON.TIME macro).
	if strings.Contains(code, `L"__xllgen_RibbonConnectRetry"`) {
		t.Errorf("src/ribbon_connect.cpp must schedule via xll::RibbonConnectRetryMacroName(), not a " +
			"re-typed macro-name literal")
	}
	// Termination is by state gate / self-abort, never a C-API cancel: a cancel
	// from a COM-event context is rejected with xlretInvXlfn and would add
	// teardown surface (§20/§23.6 HIGH #2).
	if strings.Contains(tick, "CancelDeferredRunner") || strings.Contains(tick, "schedule=*/FALSE") {
		t.Errorf("the retry chain must terminate by self-abort, not by an xlcOnTime cancel")
	}
}

// TestRibbonConnectArmIsStartOnceAndInspectsRc pins the arm half of the chain.
//
// The START-ONCE CAS is the fix for MED #2(d): a second xlAutoOpen in the same
// process generation (probe-unload-reuse, or add-in disable→enable without a DLL
// unload) while still unconnected used to arm a SECOND self-re-arming chain
// sharing the same counters — double dispatch rate, half the budget each. The
// latch is released ONLY when the xlcOnTime itself was rejected (nothing is in
// flight, so a later xlAutoOpen may legitimately try again); the terminal states
// leave it latched so nothing restarts an already-decided chain.
func TestRibbonConnectArmIsStartOnceAndInspectsRc(t *testing.T) {
	t.Parallel()
	_, code := ribbonConnectSources(t)

	const sig = "void ArmConnectRetry() {"
	start := strings.Index(code, sig)
	if start < 0 {
		t.Fatalf("ArmConnectRetry not found in src/ribbon_connect.cpp")
	}
	arm := code[start:]
	if e := strings.Index(arm, "\nvoid RunConnectRetryTick()"); e > 0 {
		arm = arm[:e]
	}

	for _, want := range []string{
		// All three preconditions, in one expression: not unloading, still
		// unconnected, and the CAS won.
		"bool retryExpected = false;",
		"if (!xll::g_isUnloading.load(std::memory_order_acquire) &&",
		"g_ribbonConnectState.load(std::memory_order_acquire) == 0 &&",
		"g_ribbonRetryArmed.compare_exchange_strong(retryExpected, true)) {",
		// The first link is scheduled at the productive spacing…
		"int armRc = xll::ScheduleOnTimeMacro(xll::RibbonConnectRetryMacroName(), kRibbonRetryIntervalSec);",
		// …and the rc is inspected, un-latching so a later xlAutoOpen may retry.
		"if (armRc != xlretSuccess) {",
		"g_ribbonRetryArmed.store(false, std::memory_order_release);",
		"Ribbon: OnTime connect retry could not be armed (xlcOnTime rc=",
	} {
		if !strings.Contains(arm, want) {
			t.Errorf("ArmConnectRetry missing %q:\n%s", want, arm)
		}
	}

	// The un-latch must live INSIDE the rejection branch. Clearing it
	// unconditionally would defeat the start-once property entirely.
	rejIdx := strings.Index(arm, "if (armRc != xlretSuccess) {")
	unlatchIdx := strings.Index(arm, "g_ribbonRetryArmed.store(false, std::memory_order_release);")
	if rejIdx < 0 || unlatchIdx < 0 || unlatchIdx < rejIdx {
		t.Errorf("the g_ribbonRetryArmed un-latch must sit inside the rejected-arm branch "+
			"(reject@%d unlatch@%d): clearing it unconditionally would let a second chain start",
			rejIdx, unlatchIdx)
	}
	// The rc must not be discarded — the pre-fix shape was a bare fire-and-forget
	// call.
	if strings.Contains(arm, "\n    xll::ScheduleOnTimeMacro(xll::RibbonConnectRetryMacroName()") {
		t.Errorf("ArmConnectRetry discards the schedule rc; a rejected arm would kill the chain silently")
	}
}

// retryTickBody slices RunConnectRetryTick out of the comment-stripped
// implementation so the assertions above cannot be satisfied by ArmConnectRetry
// or TryConnectRibbon, which legitimately contain similar-looking statements
// (both touch g_ribbonRetryArmed and g_ribbonConnectState).
func retryTickBody(t *testing.T, code string) string {
	t.Helper()
	const sig = "void RunConnectRetryTick() {"
	start := strings.Index(code, sig)
	if start < 0 {
		t.Fatalf("RunConnectRetryTick not found in src/ribbon_connect.cpp — the OnTime retry body must " +
			"live in the asset, with only the exported shim left in the template")
	}
	body := code[start:]
	if e := strings.Index(body, "\n} // namespace ribbon"); e > 0 {
		body = body[:e]
	}
	return body
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

// TestRibbonConnectFaultClassification pins STAGE A of backlog line 121 — "ribbon
// COM connect fails sporadically (~3/20 starts)".
//
// The item asks whether that is fixable in code. It was UNKNOWABLE FROM THE REPO,
// and that was itself the defect: SetRibbonConnected collapsed THREE distinct
// failures into one bare `false` and discarded every HRESULT.
//
//	(i)   Application.COMAddIns unreadable / not VT_DISPATCH
//	(ii)  COMAddIns.Item(progId) failed — Excel does not list our ProgID at all.
//	      Not racy: persistent for the session.
//	(iii) the Connect PROPERTYPUT was rejected — the only genuine Office refusal
//	      (Trust Center "Disable all Application Add-ins", or the ProgID sitting in
//	      Excel's Resiliency\DisabledItems after an earlier crash).
//
// Three faults with completely different causes and completely different fixes,
// reported identically. Until they are separable there is nothing to decide.
//
// BRANCH-STRUCTURE asserts, not behavioural ones: this is COM, so there is no
// unit-testable surface (same discipline, and same reason, as
// office_disconnect_guard_cpp_test.go — a substring assert stayed GREEN there with
// a whole guard removed).
func TestRibbonConnectFaultClassification(t *testing.T) {
	t.Parallel()
	hdr, code := ribbonConnectSources(t)

	// --- The fault class is a NAMED type in the header, beside RibbonAttempt. ---
	for _, want := range []string{
		"enum class RibbonConnectFault {",
		"kNoComAddInsProperty",
		"kProgIdNotInCollection",
		"kConnectPutRejected",
		"RibbonConnectFault* pFault = nullptr);",
	} {
		if !strings.Contains(hdr, want) {
			t.Errorf("include/com/ribbon_connect.h missing %q — the three connect failures must be "+
				"separable by NAME, not by reading a bool", want)
		}
	}

	// --- SetRibbonConnected: each COM step's HRESULT captured, each fault named. ---
	scIdx := strings.Index(code, "bool SetRibbonConnected(bool connected")
	if scIdx < 0 {
		t.Fatalf("SetRibbonConnected definition not found")
	}
	sc := code[scIdx:]
	if e := strings.Index(sc, "\nbool TryConnectRibbon("); e > 0 {
		sc = sc[:e]
	}

	for _, want := range []string{
		// The three HRESULTs, captured into locals rather than swallowed by
		// SUCCEEDED(...) at the call site.
		"hrAddins = xll::com::GetProperty(pApp, L\"COMAddIns\", &vAddins)",
		"hrItem = xll::com::Invoke(vAddins.pdispVal, L\"Item\"",
		"hrPut = xll::com::Invoke(vItem.pdispVal, L\"Connect\"",
		// …and the three fault assignments.
		"fault = RibbonConnectFault::kNoComAddInsProperty",
		"fault = RibbonConnectFault::kProgIdNotInCollection",
		"fault = RibbonConnectFault::kConnectPutRejected",
	} {
		if !strings.Contains(sc, want) {
			t.Errorf("SetRibbonConnected missing %q\n---\n%s", want, sc)
		}
	}

	// The MAPPING, not just the presence. The classification is a progressive
	// narrowing -- `fault` starts at kNoComAddInsProperty and is downgraded to the next
	// class only after the preceding COM call SUCCEEDED -- so which assignment sits
	// between which calls IS the classification. Presence alone does not pin it:
	// swapping kProgIdNotInCollection and kConnectPutRejected onto each other's branch
	// left the earlier version of this test GREEN (verified), and that swap makes a
	// ProgID missing from the collection report as "Office REJECTED the Connect
	// property put" AND fire the registry sweep -- exactly the misdiagnosis this whole
	// item exists to prevent.
	order := []struct {
		what  string
		token string
	}{
		{"initial class (before any COM call)", "fault = RibbonConnectFault::kNoComAddInsProperty"},
		{"the COMAddIns read", "hrAddins = xll::com::GetProperty"},
		{"class after COMAddIns succeeded", "fault = RibbonConnectFault::kProgIdNotInCollection"},
		{"the Item lookup", "hrItem = xll::com::Invoke"},
		{"class after Item succeeded", "fault = RibbonConnectFault::kConnectPutRejected"},
		{"the Connect put", "hrPut = xll::com::Invoke"},
	}
	prev := -1
	prevWhat := ""
	for _, step := range order {
		at := strings.Index(sc, step.token)
		if at < 0 {
			continue // the presence loop above already reported it
		}
		if at < prev {
			t.Errorf("classification out of order: %s (@%d) must come AFTER %s (@%d). The class is "+
				"assigned by POSITION -- each one means \"everything before this point worked\" -- so "+
				"a reordering silently mislabels a real failure\n---\n%s",
				step.what, at, prevWhat, prev, sc)
		}
		prev, prevWhat = at, step.what
	}

	// The three HRESULTs must all reach the log line: naming the class without the
	// HRESULT still leaves "why" unanswerable.
	logIdx := strings.Index(sc, "SAFE_LOG_WARN")
	if logIdx < 0 {
		t.Fatalf("SetRibbonConnected must log the classified failure\n---\n%s", sc)
	}
	for _, want := range []string{"hrAddins", "hrItem", "hrPut"} {
		if !strings.Contains(sc[logIdx:], want) {
			t.Errorf("the diagnostic log must carry %s — a fault class with no HRESULT is still "+
				"undiagnosable\n---\n%s", want, sc[logIdx:])
		}
	}

	// --- THE GATE: the diagnostic must be inside `connected` ---
	// SetRibbonConnected is ALSO the teardown-time DISCONNECT
	// (GracefulComTeardownHook, inside Phase 1). That call runs on the STA in the
	// ~80-100 ms window before Excel's FreeLibrary; adding registry reads and COM
	// property gets there would be new work in exactly the window §20.2.1 exists to
	// keep empty. Gating on the connect direction removes it entirely.
	gateIdx := strings.Index(sc, "if (!ok && connected) {")
	if gateIdx < 0 {
		t.Fatalf("the whole diagnostic block must be gated on `if (!ok && connected)`: "+
			"SetRibbonConnected(false) is the TEARDOWN disconnect, called from Phase 1, and must gain "+
			"ZERO extra work\n---\n%s", sc)
	}
	if gateIdx > logIdx {
		t.Errorf("the `connected` gate must PRECEDE the diagnostic log (gate@%d log@%d), or the "+
			"teardown disconnect pays for it\n---\n%s", gateIdx, logIdx, sc)
	}
	// The environment dump is the most expensive part; it must be inside the same gate.
	dumpIdx := strings.Index(sc, "DumpConnectEnvironmentOnce(")
	if dumpIdx < 0 || dumpIdx < gateIdx {
		t.Errorf("the one-shot environment dump must sit INSIDE the `connected` gate (gate@%d dump@%d)"+
			"\n---\n%s", gateIdx, dumpIdx, sc)
	}
	// …and only for the one class that is a genuine Office refusal.
	if !strings.Contains(sc[gateIdx:], "fault == RibbonConnectFault::kConnectPutRejected") {
		t.Errorf("the environment dump must be reserved for kConnectPutRejected — the other two "+
			"classes are not Office refusals and the dump would not explain them\n---\n%s", sc[gateIdx:])
	}

	// --- The dump is ONE-SHOT (atomic CAS) and READ-ONLY. ---
	dIdx := strings.Index(code, "void DumpConnectEnvironmentOnce(")
	if dIdx < 0 {
		t.Fatalf("DumpConnectEnvironmentOnce definition not found")
	}
	dump := code[dIdx:]
	if e := strings.Index(dump, "\n} // namespace"); e > 0 {
		dump = dump[:e]
	}
	if !strings.Contains(dump, "compare_exchange_strong") {
		t.Errorf("the environment dump must be latched by an atomic CAS: the connect retries up to 60 "+
			"times, and 60 registry sweeps in the log is not a diagnostic\n---\n%s", dump)
	}
	// READ-ONLY is asserted over the WHOLE anonymous-namespace diagnostics block, not
	// just over DumpConnectEnvironmentOnce's own body.
	//
	// The first version of this check scanned only from `void DumpConnectEnvironmentOnce(`
	// onwards — but DescribeResiliencyDisabledItems, which does essentially ALL of the
	// registry work (open, enumerate Office versions, enumerate values), is defined
	// ABOVE it and therefore sat outside the scanned region. Verified hole: inserting
	// RegSetValueExW into that helper left this test GREEN. The guard was scoped to the
	// one part of the diagnostic that does not touch the registry.
	// Delimited by BRACE MATCHING, not by a "} // namespace" marker: `code` has had its
	// comments stripped, so that marker does not survive to be found. The first draft
	// searched for it, silently failed to delimit, and had to be caught by re-running
	// the mutation -- a reminder that a delimiter which can quietly not match turns an
	// assertion into a no-op just as effectively as a wrong scope does.
	diagStart := strings.Index(code, "namespace {")
	if diagStart < 0 {
		t.Fatalf("could not find the anonymous namespace opening the diagnostics block")
	}
	diagBlock := ""
	{
		depth := 0
		for i := diagStart + len("namespace"); i < len(code); i++ {
			if code[i] == '{' {
				depth++
			} else if code[i] == '}' {
				depth--
				if depth == 0 {
					diagBlock = code[diagStart : i+1]
					break
				}
			}
		}
	}
	if diagBlock == "" {
		t.Fatalf("unbalanced braces in the anonymous diagnostics namespace")
	}
	if !strings.Contains(diagBlock, "DescribeResiliencyDisabledItems") ||
		!strings.Contains(diagBlock, "DumpConnectEnvironmentOnce") {
		t.Fatalf("the scanned region must contain BOTH diagnostic helpers; if one moved out of the " +
			"anonymous namespace this check silently stopped covering it")
	}
	for _, forbidden := range []string{"RegSetValue", "RegCreateKey", "RegDeleteKey", "RegDeleteTree"} {
		if strings.Contains(diagBlock, forbidden) {
			t.Errorf("the environment dump must be strictly READ-ONLY; found %q in the diagnostics "+
				"block. A diagnostic that mutates the state it is diagnosing is worse than no "+
				"diagnostic\n---\n%s", forbidden, diagBlock)
		}
	}
	// The Office version segment must be ENUMERATED, not hard-coded: the Addins key
	// we write is version-independent and this file must not acquire a second,
	// silently-wrong Office-version assumption.
	if strings.Contains(code, `Office\\16.0\\Excel`) {
		t.Errorf("do not hard-code the Office version in the Resiliency probe — enumerate the " +
			`subkeys of HKCU\Software\Microsoft\Office instead`)
	}
	if !strings.Contains(code, "RegEnumKeyExW") {
		t.Errorf("the Resiliency probe must enumerate Office version subkeys (RegEnumKeyExW)")
	}
	if !strings.Contains(code, "DisabledItems") {
		t.Errorf(`the environment dump must report Excel's Resiliency\DisabledItems state — an ` +
			"earlier crash parking our ProgID there is the leading suspect for a rejected Connect")
	}

	// --- A2: the 60-attempt give-up line must name the DOMINANT class + its HRESULT. ---
	capIdx := strings.Index(code, "Ribbon: COMAddIns connect failed after 60 attempts")
	if capIdx < 0 {
		t.Fatalf("the 60-attempt give-up log line is gone")
	}
	tail := code[capIdx:]
	if e := strings.Index(tail, "\n    }"); e > 0 {
		tail = tail[:e]
	}
	if !strings.Contains(tail, "Dominant fault") || !strings.Contains(tail, "FaultName(") {
		t.Errorf("the give-up line must name the dominant fault class — an opaque \"failed after 60 "+
			"attempts\" is the exact line that made this item unanswerable\n---\n%s", tail)
	}
	if !strings.Contains(tail, "HrToString(") {
		t.Errorf("the give-up line must carry the dominant class's HRESULT\n---\n%s", tail)
	}
}
