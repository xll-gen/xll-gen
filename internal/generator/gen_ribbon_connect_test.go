package generator

import (
	"regexp"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/assets"
	"github.com/xll-gen/xll-gen/internal/config"
)

// ribbonConnectCfg builds a minimal ribbon-enabled config for the deferred
// connect regression tests. Ribbon requires at least one command (validated
// upstream — "ribbon without commands is an error", AGENTS.md §18.11), so a
// single RunReport command is declared.
func ribbonConnectCfg() *config.Config {
	return &config.Config{
		Project:  config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Commands: []config.Command{{Name: "RunReport", Handler: "RunReport"}},
		Ribbon: config.RibbonConfig{Tab: "T", Groups: []config.RibbonGroup{{
			Label: "G", Buttons: []config.RibbonButton{{Label: "B", Command: "RunReport"}},
		}}},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}
}

// TestXllMainRibbonDeferredConnect is the Bug 1 regression: the ribbon COMAddIns
// connect needs the in-process Application object, which is reachable only via
// the XLDESK -> EXCEL7 child window. When the XLL loads with NO workbook open,
// that window does not exist, GetExcelApplication() returns nullptr, and the
// connect cannot run — the ribbon tab never appears.
//
// The fix (2026-06-13) adopts Excel-DNA's synchronous temp-workbook bounce
// (Source/ExcelDna.Integration/Excel.cs, GetApplicationFromNewWorkbook): at
// xlAutoOpen, GetExcelApplicationOrBounce() creates a temporary workbook via the
// XLM command API (xlcNew/xlcWorkbookInsert) to materialize the EXCEL7 window,
// grabs the Application, then closes the scratch workbook (xlcFileClose). The
// connection binds to the Application (not the workbook) so it survives the
// temp workbook closing. This REPLACES the former STA WM_TIMER retry loop, which
// was an accepted forced-unload crash residual (AGENTS.md §20.2).
//
// This test pins:
//   - the bounce helper GetExcelApplicationOrBounce exists and uses the verified
//     xlc* opcodes through xll::CallExcel;
//   - xlAutoOpen drives the connect through TryConnectRibbon with allowBounce=true;
//   - the calc-end handler retries WITHOUT bouncing (defensive fallback);
//   - the CalculationEnded event is still registered whenever the ribbon is enabled;
//   - the no-workbook-yet ("noApp") case does NOT consume the give-up budget;
//   - the removed STA timer machinery (SetTimer/TimerProc/Arm/Stop) is GONE.
func TestXllMainRibbonDeferredConnect(t *testing.T) {
	t.Parallel()
	src := renderCppMain(t, ribbonConnectCfg())

	for _, want := range []string{
		// The retryable connect helper exists and threads allowBounce + the
		// outcome-class out-param (see TestXllMainRibbonRetryNoAppBudget and
		// TestXllMainRibbonRetryUnattemptedNotCharged).
		"static bool TryConnectRibbon(const char* phase, bool allowBounce = false,",
		"RibbonAttempt* pOutcome = nullptr) {",
		// …and the outcome classes are an enum class (a bool cannot express the
		// third, UNCHARGEABLE class).
		"enum class RibbonAttempt {",
		"kNotAttempted = 0,",
		// The temp-workbook bounce helper exists.
		"static IDispatch* GetExcelApplicationOrBounce()",
		// It uses the verified xlc* command opcodes via xll::CallExcel.
		"xll::CallExcel(xlcNew, nullptr, 5)",
		"xll::CallExcel(xlcWorkbookInsert, nullptr, 6)",
		"xll::CallExcel(xlcFileClose, nullptr, false)",
		// HIGH (data-loss) hardening: the bounce captures the ACTIVE workbook
		// name via GET.DOCUMENT(88) (xlfGetDocument, selector 88) and closes the
		// scratch book BY IDENTITY — only when it is still the active one.
		"static std::wstring GetActiveWorkbookName()",
		"xll::CallExcel(xlfGetDocument, xName, 88)",
		"PascalToWString(xName.get()->val.str)",
		"std::wstring scratchName = GetActiveWorkbookName();",
		"std::wstring activeNow = GetActiveWorkbookName();",
		// The close is guarded by the identity comparison, never issued blindly.
		"if (activeNow.empty() || activeNow != scratchName) {",
		// MED hardening: TryConnectRibbon is non-re-entrant during the bounce.
		"static std::atomic<bool> s_inConnect{false};",
		"if (!s_inConnect.compare_exchange_strong(expected, true)) return false;",
		// SetRibbonConnected routes through the bounce only when allowed.
		"GetExcelApplicationOrBounce() : GetExcelApplication();",
		// xlAutoOpen drives the connect through it WITH the bounce enabled.
		`TryConnectRibbon("xlAutoOpen", /*allowBounce=*/true);`,
		// The bounce helper honors graceful degradation (warn, not crash).
		"SAFE_LOG_WARN(",
		// The no-workbook-yet case is detected, does NOT burn the give-up budget,
		// and is REPORTED to the caller so the OnTime retry can honor the same rule.
		"bool noApp = false;",
		"if (noApp) {",
		"if (pOutcome) *pOutcome = RibbonAttempt::kNoApp;",
		// Calc-end retries the connect as a defensive fallback (no bounce).
		`TryConnectRibbon("calc end");`,
		// The connect state is a single atomic guard (pending/connected/gave-up).
		"g_ribbonConnectState",
		// CalculationEnded is registered as the fallback retry hook for ribbon builds.
		"needRibbonRetry:",
		`xll::CallExcel(xlEventRegister, nullptr, L"CalculationEnded", xleventCalculationEnded);`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("xll_main.cpp (ribbon) missing %q\n---\n%s", want, src)
		}
	}

	// The STA WM_TIMER retry machinery (the removed crash residual) must be
	// entirely absent from a ribbon-enabled render. A reintroduction of any of
	// these symbols brings back the forced-unload 0xC0000005 (AGENTS.md §20.2).
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
		if strings.Contains(src, gone) {
			t.Errorf("xll_main.cpp (ribbon) still contains removed STA timer symbol %q (the §20.2 unmap-crash residual must stay gone)", gone)
		}
	}

	// The calc-end fallback must NOT bounce (a workbook already exists there).
	if strings.Contains(src, `TryConnectRibbon("calc end", true)`) ||
		strings.Contains(src, `TryConnectRibbon("calc end", /*allowBounce=*/true)`) {
		t.Errorf("calc-end retry must not enable the temp-workbook bounce (allowBounce must default to false there)")
	}

	// The connect must NOT be wired as a single inline best-effort call in
	// xlAutoOpen without the retry path. The old code logged exactly this on a
	// failed connect; the new code never does (failures go through
	// TryConnectRibbon's bounded-attempt warning instead).
	if strings.Contains(src, `SAFE_LOG_WARN("Ribbon: COMAddIns connect failed; ribbon UI disabled.");`) {
		t.Errorf("xll_main.cpp still contains the old one-shot connect failure path (no retry)")
	}

	// Negative: ribbon-disabled render must not reference the connect helpers.
	noRibbon := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "Sum", Return: "int", Args: []config.Arg{{Name: "a", Type: "int"}}},
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}
	noRibbonSrc := renderCppMain(t, noRibbon)
	if strings.Contains(noRibbonSrc, "TryConnectRibbon") {
		t.Errorf("ribbon-disabled render must not reference TryConnectRibbon")
	}
	if strings.Contains(noRibbonSrc, "GetExcelApplicationOrBounce") {
		t.Errorf("ribbon-disabled render must not reference the temp-workbook bounce helper")
	}
	if strings.Contains(noRibbonSrc, "needRibbonRetry") {
		t.Errorf("ribbon-disabled render must not emit the needRibbonRetry hook")
	}
}

// TestXllMainRibbonOnTimeConnectRetry pins the bounded xlcOnTime connect-retry
// (AGENTS.md §3). Before this, when the ribbon did not connect at load
// (ribbon.bounce: off, or a bounce that failed) the ONLY remaining trigger was
// TryConnectRibbon("calc end") — which never fires for a workbook that is open
// but never recalculates (manual calc mode / no-formula book), so the ribbon
// tab was delayed indefinitely. The fix arms a bounded, state-gated
// xlcOnTime retry macro (__xllgen_RibbonConnectRetry) from xlAutoOpen (a valid
// command context) that re-arms from its OWN Excel-dispatched macro context and
// self-aborts (no C-API cancel) on connect/give-up/budget/unload — so it adds no
// new teardown-cancellation surface (§20/§23; the schedule/cancel-from-event
// wall is §23.6 HIGH #2).
//
// Runs the marker set against BOTH the default (full) render and the off render:
// off is the mode most in need of the retry (it never bounces at all), and the
// cpp_compile_gate_bounce_test.go off case proves the emitted retry compiles.
func TestXllMainRibbonOnTimeConnectRetry(t *testing.T) {
	t.Parallel()

	offCfg := ribbonConnectCfg()
	offCfg.Ribbon.Bounce = "off"

	for _, tc := range []struct {
		name string
		cfg  *config.Config
	}{
		{"default", ribbonConnectCfg()},
		{"off", offCfg},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := renderCppMain(t, tc.cfg)
			for _, want := range []string{
				// The bounded budget constants exist (name + count/spacing).
				"kRibbonRetryMaxAttempts       = 30",
				"kRibbonRetryIntervalSec       = 2.0",
				// The retry macro is exported with the shared single-source name.
				`extern "C" __declspec(dllexport) short __stdcall __xllgen_RibbonConnectRetry()`,
				"xll::RibbonConnectRetryMacroName()",
				// The runner retries the connect (no bounce on retries).
				`TryConnectRibbon("ontime retry", /*allowBounce=*/false, &retryOutcome);`,
				// Self-abort on unload BEFORE touching Excel (no re-arm on unload).
				"if (g_isUnloading.load(std::memory_order_acquire)) return 1;",
				// Stops once the connect resolves (connected/gave-up).
				"if (g_ribbonConnectState.load(std::memory_order_acquire) != 0) return 1;",
				// Bounded re-arm through the reused asset scheduler.
				"g_ribbonRetryAttempts.fetch_add(1) + 1;",
				"if (n >= kRibbonRetryMaxAttempts) {",
				"xll::ScheduleOnTimeMacro(xll::RibbonConnectRetryMacroName(), nextDelaySec);",
				// The macro is registered (macroType=2) so xlcOnTime can target it.
				"Failed to register ribbon-connect OnTime retry macro.",
				// xlAutoOpen arms the first retry ONLY when not yet connected.
				"g_ribbonConnectState.load(std::memory_order_acquire) == 0 &&",
			} {
				if !strings.Contains(src, want) {
					t.Errorf("[%s] xll_main.cpp (ribbon) missing %q", tc.name, want)
				}
			}

			// Termination must be state-gated self-abort, NOT an xlcOnTime cancel
			// from the retry path (a cancel from a COM-event context is rejected
			// with xlretInvXlfn — §23.6 HIGH #2 — and adds teardown surface §20).
			if strings.Contains(src, "CancelDeferredRunner") {
				t.Errorf("[%s] ribbon retry must not wire an xlcOnTime cancel; termination is by state-gate self-abort (§3/§23.6)", tc.name)
			}
		})
	}

	// Negative: ribbon-disabled render must not emit the retry macro/name.
	noRibbon := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "Sum", Return: "int", Args: []config.Arg{{Name: "a", Type: "int"}}},
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}
	noRibbonSrc := renderCppMain(t, noRibbon)
	for _, gone := range []string{
		"__xllgen_RibbonConnectRetry",
		"RibbonConnectRetryMacroName",
		"kRibbonRetryMaxAttempts",
		"kRibbonRetryNoAppMaxAttempts",
		"g_ribbonRetryArmed",
	} {
		if strings.Contains(noRibbonSrc, gone) {
			t.Errorf("ribbon-disabled render must not emit %q", gone)
		}
	}
}

// TestXllMainRibbonRetryNoAppBudget is the MED regression for the retry's
// budget-accounting bug (2026-07-26).
//
// TryConnectRibbon has ALWAYS declined to charge a "no Application object yet"
// (noApp) attempt against its own give-up budget — that state is not a failure,
// it just means the user has not opened a workbook. The xlcOnTime retry runner
// did not make that distinction: it charged every attempt to the single
// kRibbonRetryMaxAttempts=30 budget. Consequence, for the EXACT scenario the
// retry was added to fix: Excel started empty with `ribbon.bounce: off`, the
// retry polled 30 times over 60 s against no workbook, exhausted the budget, and
// stopped. The user then opened a manual-calc / no-formula workbook at t=90 s —
// no budget left, and calc-end never fires for such a book — so the ribbon tab
// was STILL delayed indefinitely. The feature did not fix its own target case.
//
// The fix threads a `bool* pNoApp` out-param through TryConnectRibbon and gives
// the noApp class its own, much longer, time-shaped budget with a relaxed poll
// interval, while the productive (Application reachable, Connect rejected) class
// keeps the original tight 30-attempt budget. Both budgets stay FINITE.
//
// This test pins the separation itself; a regression that folds the two classes
// back into one counter fails here.
func TestXllMainRibbonRetryNoAppBudget(t *testing.T) {
	t.Parallel()

	offCfg := ribbonConnectCfg()
	offCfg.Ribbon.Bounce = "off"

	for _, tc := range []struct {
		name string
		cfg  *config.Config
	}{
		{"default", ribbonConnectCfg()},
		{"off", offCfg},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := renderCppMain(t, tc.cfg)

			for _, want := range []string{
				// A SEPARATE noApp budget exists, with its own spacing and hard stop.
				"kRibbonRetryNoAppFastAttempts",
				"kRibbonRetryNoAppIdleSec",
				"kRibbonRetryNoAppMaxAttempts",
				// …and its own counter, distinct from the productive one.
				"g_ribbonRetryNoAppAttempts",
				"g_ribbonRetryAttempts",
				// The runner asks TryConnectRibbon which class the failure was.
				"RibbonAttempt retryOutcome = RibbonAttempt::kNotAttempted;",
				`TryConnectRibbon("ontime retry", /*allowBounce=*/false, &retryOutcome);`,
				// …and branches on it before charging anything.
				"if (retryOutcome == RibbonAttempt::kNoApp) {",
				"g_ribbonRetryNoAppAttempts.fetch_add(1) + 1;",
				"if (n >= kRibbonRetryNoAppMaxAttempts) {",
				// The poll relaxes once the fast window is spent (still bounded).
				"if (n >= kRibbonRetryNoAppFastAttempts) nextDelaySec = kRibbonRetryNoAppIdleSec;",
				// The re-arm honors the per-class spacing.
				"double nextDelaySec = kRibbonRetryIntervalSec;",
				"xll::ScheduleOnTimeMacro(xll::RibbonConnectRetryMacroName(), nextDelaySec);",
			} {
				if !strings.Contains(src, want) {
					t.Errorf("[%s] xll_main.cpp ribbon OnTime retry missing %q", tc.name, want)
				}
			}

			// The pre-fix shape: a single unconditional charge to the productive
			// budget, with no noApp branch. If this reappears, the empty-Excel
			// scenario burns its 30 attempts in 60 s again.
			for _, gone := range []string{
				"s_retryAttempts",
				"s_retryAttempts.fetch_add(1) + 1 >= kRibbonRetryMaxAttempts",
			} {
				if strings.Contains(src, gone) {
					t.Errorf("[%s] xll_main.cpp still contains the pre-fix single-budget retry counter %q; "+
						"a noApp attempt must not consume the connect-failure budget", tc.name, gone)
				}
			}

			// Neither budget may become unbounded: an add-in that polls Excel
			// forever is its own defect. Both hard stops must be present.
			if !strings.Contains(src, "if (n >= kRibbonRetryMaxAttempts) {") {
				t.Errorf("[%s] the productive retry budget lost its hard stop", tc.name)
			}
			if strings.Contains(src, "for (;;)") || strings.Contains(src, "while (true)") {
				t.Errorf("[%s] the ribbon retry must never spin unbounded", tc.name)
			}
		})
	}
}

// TestXllMainRibbonRetryUnattemptedNotCharged is the LOW regression for the
// LAST unbilled-attempt accounting hole (2026-07-26, same defect class as the
// noApp split above, one branch further out).
//
// TryConnectRibbon has two exits that return BEFORE ever calling
// SetRibbonConnected — i.e. before touching COM at all:
//
//	1. the g_isUnloading bail (unreachable from the OnTime runner, which gates on
//	   the same flag first);
//	2. the STA RE-ENTRANCY bail (s_inConnect CAS failure) — very much reachable:
//	   the COMAddIns Connect and the temp-workbook bounce both PUMP the STA
//	   message loop, so Excel can dispatch a queued OnTime macro while a connect
//	   is mid-flight; that dispatch re-enters TryConnectRibbon on the same thread
//	   and is turned away.
//
// Before the fix both exits reported themselves as an ordinary failure (pNoApp
// left false), so the runner charged one of its 30 productive attempts for a
// connect that never happened. The fix replaces the bool out-param with an
// explicit outcome CLASS; only kRejected is chargeable, kNoApp goes to the noApp
// budget, and kNotAttempted charges NOTHING.
func TestXllMainRibbonRetryUnattemptedNotCharged(t *testing.T) {
	t.Parallel()

	offCfg := ribbonConnectCfg()
	offCfg.Ribbon.Bounce = "off"

	for _, tc := range []struct {
		name string
		cfg  *config.Config
	}{
		{"default", ribbonConnectCfg()},
		{"off", offCfg},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := renderCppMain(t, tc.cfg)

			for _, want := range []string{
				// The three-way (four-way with kConnected) outcome class exists…
				"enum class RibbonAttempt {",
				"kNotAttempted = 0,",
				"kNoApp,",
				"kRejected,",
				"kConnected,",
				// …TryConnectRibbon defaults the out-param to kNotAttempted, so an
				// exit that forgets to classify itself is UNCHARGED, never
				// mis-charged.
				"if (pOutcome) *pOutcome = RibbonAttempt::kNotAttempted;",
				"if (pOutcome) *pOutcome = RibbonAttempt::kNoApp;",
				"if (pOutcome) *pOutcome = RibbonAttempt::kRejected;",
				"if (pOutcome) *pOutcome = RibbonAttempt::kConnected;",
				// …and the runner charges the productive budget ONLY for kRejected.
				"} else if (retryOutcome == RibbonAttempt::kRejected) {",
				"g_ribbonRetryAttempts.fetch_add(1) + 1;",
			} {
				if !strings.Contains(src, want) {
					t.Errorf("[%s] xll_main.cpp ribbon retry missing %q", tc.name, want)
				}
			}

			// The pre-fix shape: an `else` that charges the productive budget for
			// EVERY non-noApp outcome, including the never-attempted ones.
			if strings.Contains(src, "if (retryNoApp) {") || strings.Contains(src, "bool retryNoApp = false;") {
				t.Errorf("[%s] the runner still uses the bool noApp out-param; a re-entrancy bail "+
					"(nothing attempted) is then billed to the 30-attempt productive budget", tc.name)
			}

			// kRejected must be classified at the site that actually RAN the
			// Connect: after SetRibbonConnected returned false and after the noApp
			// early-return. Anything earlier would re-introduce the mis-billing.
			connectIdx := strings.Index(src, "if (SetRibbonConnected(true, &noApp, allowBounce)) {")
			noAppIdx := strings.Index(src, "if (pOutcome) *pOutcome = RibbonAttempt::kNoApp;")
			rejectedIdx := strings.Index(src, "if (pOutcome) *pOutcome = RibbonAttempt::kRejected;")
			reentryIdx := strings.Index(src, "if (!s_inConnect.compare_exchange_strong(expected, true)) return false;")
			if connectIdx < 0 || noAppIdx < 0 || rejectedIdx < 0 || reentryIdx < 0 {
				t.Fatalf("[%s] missing markers (connect=%d noApp=%d rejected=%d reentry=%d)",
					tc.name, connectIdx, noAppIdx, rejectedIdx, reentryIdx)
			}
			if !(reentryIdx < connectIdx && connectIdx < noAppIdx && noAppIdx < rejectedIdx) {
				t.Errorf("[%s] outcome classification is out of order (reentry=%d connect=%d noApp=%d rejected=%d): "+
					"kRejected must only be reported after an actual SetRibbonConnected attempt",
					tc.name, reentryIdx, connectIdx, noAppIdx, rejectedIdx)
			}
			// The re-entrancy bail must NOT classify itself as anything but the
			// default: no assignment may sit between the CAS and the guard object.
			between := src[reentryIdx:connectIdx]
			if strings.Contains(between, "*pOutcome = RibbonAttempt::kRejected") ||
				strings.Contains(between, "*pOutcome = RibbonAttempt::kNoApp") {
				t.Errorf("[%s] the STA re-entrancy bail must stay kNotAttempted (charge nothing)", tc.name)
			}
		})
	}
}

// TestXllMainRibbonRetryArmRcAndSingleChain pins two MED fixes on the arm path
// (2026-07-26):
//
//  1. The arm site discarded ScheduleOnTimeMacro's return value. If Excel
//     rejected the xlcOnTime (e.g. a context/host state that refuses command-class
//     C-API calls) the whole self-re-arming chain never started and NOTHING said
//     so — indistinguishable in the log from "armed and still retrying". Both the
//     initial arm and the runner's re-arm must inspect the rc and warn.
//
//  2. The attempt counter was a FUNCTION-LOCAL static inside the SEH __try block
//     of __xllgen_RibbonConnectRetry. A second xlAutoOpen in the same process
//     generation (probe-unload-reuse, or add-in disable→enable without a DLL
//     unload) while still unconnected armed a SECOND chain sharing that counter:
//     double the dispatch rate, half the effective budget each. The counters move
//     to file scope and a start-once CAS latch (g_ribbonRetryArmed) makes the arm
//     idempotent.
func TestXllMainRibbonRetryArmRcAndSingleChain(t *testing.T) {
	t.Parallel()
	src := renderCppMain(t, ribbonConnectCfg())

	for _, want := range []string{
		// (2) File-scope state, not function-local statics in an SEH __try.
		"static std::atomic<bool> g_ribbonRetryArmed{false};",
		"static std::atomic<int>  g_ribbonRetryAttempts{0};",
		"static std::atomic<int>  g_ribbonRetryNoAppAttempts{0};",
		// (2) Start-once CAS at the arm site: at most one chain per process.
		"bool retryExpected = false;",
		"g_ribbonRetryArmed.compare_exchange_strong(retryExpected, true)) {",
		// (1) The initial arm inspects the rc and un-latches so a later
		//     xlAutoOpen may legitimately try again (nothing is in flight).
		"int armRc = xll::ScheduleOnTimeMacro(xll::RibbonConnectRetryMacroName(), kRibbonRetryIntervalSec);",
		"if (armRc != xlretSuccess) {",
		"g_ribbonRetryArmed.store(false, std::memory_order_release);",
		"Ribbon: OnTime connect retry could not be armed (xlcOnTime rc=",
		// (1) The re-arm inside the runner does the same.
		"int reArmRc = xll::ScheduleOnTimeMacro(xll::RibbonConnectRetryMacroName(), nextDelaySec);",
		"if (reArmRc != xlretSuccess) {",
		"Ribbon: OnTime connect retry could not re-arm (xlcOnTime rc=",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("xll_main.cpp ribbon OnTime retry missing %q", want)
		}
	}

	// The pre-fix arm site: a bare fire-and-forget call whose rc was dropped.
	if strings.Contains(src, "        xll::ScheduleOnTimeMacro(xll::RibbonConnectRetryMacroName(), kRibbonRetryIntervalSec);\n") {
		t.Errorf("xll_main.cpp still discards the arm rc from ScheduleOnTimeMacro; " +
			"a rejected arm would kill the retry chain silently")
	}
	// The counter must not live inside the macro body as a local static.
	retryIdx := strings.Index(src, `__stdcall __xllgen_RibbonConnectRetry()`)
	if retryIdx < 0 {
		t.Fatalf("__xllgen_RibbonConnectRetry not found in the ribbon render")
	}
	if strings.Contains(src[retryIdx:], "static std::atomic<int>") {
		t.Errorf("__xllgen_RibbonConnectRetry still declares a function-local static counter; " +
			"two xlAutoOpen-armed chains would share it (double rate, half budget each)")
	}
}

// TestOnTimeMacroNameExportsMatchHeaderLiterals is the drift guard for the
// OnTime macro NAMES (item 2e, 2026-07-26).
//
// Each OnTime-scheduled macro name exists in TWO places that must agree exactly:
//
//	include/xll_deferred_commands.h : the L"…" literal returned by the
//	                                  *MacroName() accessor, which is what
//	                                  xlcOnTime and xlfRegister are handed;
//	templates/xll_main.cpp.tmpl     : the exported C symbol Excel resolves the
//	                                  registered procedure against.
//
// Nothing structurally couples them. If either side is renamed alone, the C++
// still compiles (the accessor is just a string), every generator test that
// greps for its own hard-coded name still passes, and the ONLY symptom is a
// runtime "cannot resolve ON.TIME macro" — i.e. the ribbon tab (or the deferred
// calc-end command drain) silently never runs. This test derives the expected
// export from the header literal instead of restating it, so a rename on either
// side fails here.
func TestOnTimeMacroNameExportsMatchHeaderLiterals(t *testing.T) {
	t.Parallel()
	src := renderCppMain(t, ribbonConnectCfg())

	for _, tc := range []struct {
		accessor string
		// whether the export is emitted only for ribbon-enabled renders
		ribbonOnly bool
	}{
		{"RibbonConnectRetryMacroName", true},
		{"DeferredRunnerMacroName", false},
	} {
		t.Run(tc.accessor, func(t *testing.T) {
			lit := onTimeMacroNameLiteral(t, tc.accessor)
			wantExport := `extern "C" __declspec(dllexport) short __stdcall ` + lit + `()`
			if !strings.Contains(src, wantExport) {
				t.Errorf("%s() returns L%q but xll_main.cpp exports no matching symbol.\n"+
					"expected to find: %s\n"+
					"The exported symbol and the header literal have DRIFTED: the C++ still "+
					"compiles and the generator tests still pass, but Excel cannot resolve the "+
					"ON.TIME macro at runtime.", tc.accessor, lit, wantExport)
			}
			// The registration must go through the accessor, never a re-typed literal.
			if !strings.Contains(src, "xll::"+tc.accessor+"()") {
				t.Errorf("xll_main.cpp must reference xll::%s() (single source of truth), "+
					"not a re-typed macro-name literal", tc.accessor)
			}
			if strings.Contains(src, `L"`+lit+`"`) {
				t.Errorf("xll_main.cpp re-types the macro-name literal L%q instead of calling "+
					"xll::%s(); that is the drift this test exists to prevent", lit, tc.accessor)
			}
		})
	}
}

// onTimeMacroNameLiteral extracts the L"…" literal returned by an inline
// *MacroName() accessor in the embedded include/xll_deferred_commands.h.
func onTimeMacroNameLiteral(t *testing.T, accessor string) string {
	t.Helper()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	hdr, ok := m["include/xll_deferred_commands.h"]
	if !ok {
		t.Fatalf("embedded include/xll_deferred_commands.h not found in assets")
	}
	re := regexp.MustCompile(`inline const wchar_t\* ` + regexp.QuoteMeta(accessor) +
		`\(\)\s*\{\s*return\s+L"([^"]+)";`)
	mm := re.FindStringSubmatch(hdr)
	if mm == nil {
		t.Fatalf("xll_deferred_commands.h: could not find the inline %s() accessor and its L\"…\" literal; "+
			"the single-source-of-truth macro name must stay in that exact shape (the template's "+
			"exported symbol is cross-checked against it)", accessor)
	}
	return mm[1]
}

// TestRibbonAddinFirstClickRetry is the Bug 2 regression: the ribbon onAction
// dispatch (SendCommandInvoke) is fire-and-forget. A click can land in the
// window between the server process being launched (xlAutoOpen) and the Go
// guest attaching its receive workers to the host slots. In that window a
// host-initiated Send has no reader, blocks the full timeout, and — because the
// result was discarded — the command was silently dropped. The user saw
// "nothing happens on the first click; it works after clicking another button"
// (the second click lands after the guest connected).
//
// The fix inspects the Send result and retries on failure with a bounded
// attempt budget and a short per-attempt timeout, off the STA thread (mirrors
// the mock host's first-request retry). This test pins the embedded
// src/ribbon_addin.cpp asset (the file that ships inside the XLL) so a refactor
// cannot silently drop the retry and reintroduce the dropped-first-click bug.
func TestRibbonAddinFirstClickRetry(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	src, ok := m["src/ribbon_addin.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/ribbon_addin.cpp not found")
	}

	// Isolate the SendCommandInvoke function body so the assertions cannot be
	// satisfied by unrelated code elsewhere in the file.
	const marker = "void SendCommandInvoke("
	idx := strings.Index(src, marker)
	if idx < 0 {
		t.Fatalf("SendCommandInvoke not found in ribbon_addin.cpp")
	}
	body := src[idx:]

	for _, want := range []string{
		// The Send result is inspected, not discarded.
		"slot.Send(",
		"res.HasError()",
		// A bounded retry loop exists (the dropped-first-click fix).
		"kMaxAttempts",
		// The retry honors the unload self-abort contract at the yield points.
		"g_isUnloading.load(std::memory_order_acquire)",
		// Each attempt re-acquires a fresh slot (Send disowns its slot on timeout).
		"g_phost->GetZeroCopySlot();",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("SendCommandInvoke missing %q\n---\n%s", want, body)
		}
	}

	// The pre-fix code sent exactly once with a 5000ms blocking timeout and
	// discarded the result. The retry path uses a short per-attempt timeout so
	// it does not stall teardown; assert the old single-shot 5000 literal is
	// gone from the function body.
	if strings.Contains(body, "MSG_COMMAND_INVOKE, 5000)") {
		t.Errorf("SendCommandInvoke still uses the old single-shot 5000ms blocking Send (no retry)")
	}
}

// TestRibbonAddinDispatchMapping pins the ribbon-CLICK dispatch contract: the
// path a real ribbon button click takes — Excel calls the COM add-in's
// IDispatch::GetIDsOfNames(<onAction name>) then Invoke(DISPID). This layer was
// NEVER exercised by the Application.Run / Cmd_* macro E2E (which calls
// SendCommandInvoke directly), so a break here is invisible to those tests —
// exactly the blind spot that let "ribbon tab appears but clicks do nothing"
// ship. The contract is a symmetric base+index mapping over the SAME
// g_commandNames slice that SetCommands fills (in cfg.Commands order):
//
//	GetIDsOfNames(name) : g_commandNames[i] == name  ->  DISPID = kDispIdBase + i
//	Invoke(dispId)      : idx = dispId - kDispIdBase  ->  g_commandNames[idx]
//
// If either side drifts (different base, different slice, wrong direction) the
// onAction name resolves to the wrong command or none, and clicks silently
// no-op. We pin the embedded asset so a refactor cannot break the round-trip.
func TestRibbonAddinDispatchMapping(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	src, ok := m["src/ribbon_addin.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/ribbon_addin.cpp not found")
	}

	for _, want := range []string{
		// GetIDsOfNames matches the onAction name (case-insensitively) against
		// g_commandNames and returns kDispIdBase + index.
		"_wcsicmp(rgszNames[0], g_commandNames[i].c_str()) == 0",
		"rgDispId[0] = kDispIdBase + static_cast<DISPID>(i);",
		// Invoke recovers the SAME index by subtracting the SAME base and
		// dispatches g_commandNames[idx].
		"size_t idx = static_cast<size_t>(dispIdMember - kDispIdBase);",
		"SendCommandInvoke(WideToUtf8(g_commandNames[idx]), controlId);",
		// Out-of-range / below-base DISPIDs are rejected, not misrouted.
		"if (dispIdMember < kDispIdBase || idx >= g_commandNames.size()) return DISP_E_MEMBERNOTFOUND;",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("ribbon_addin.cpp dispatch mapping missing %q\n---\n%s", want, src)
		}
	}

	// The command DISPID base must be distinct from (above) the extensibility
	// and loadImage DISPIDs, or onAction names would collide with the
	// IDTExtensibility2 members and clicks would hit a no-op S_OK stub.
	if !strings.Contains(src, "kDispIdBase") || !strings.Contains(src, "kDispIdExtBase") {
		t.Fatalf("ribbon_addin.cpp missing the kDispIdBase / kDispIdExtBase DISPID partition")
	}
}
