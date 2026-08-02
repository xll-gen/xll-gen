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

// WHERE THE OLD ASSERTIONS WENT (2026-08-02). The ribbon COM connect machinery
// moved out of xll_main.cpp.tmpl into include/com/ribbon_connect.h +
// src/ribbon_connect.cpp (it had no template variables in it, so every project was
// getting a byte-identical copy re-emitted into its generated tree). The BODY
// invariants this file used to grep out of the rendered template are now pinned
// against the embedded asset, in internal/assets/ribbon_connect_cpp_test.go:
//
//	TryConnectRibbon signature / defaults        -> TestRibbonConnectHeaderContract
//	enum class RibbonAttempt + the four classes  -> TestRibbonConnectHeaderContract
//	the four *pOutcome sites and their ORDER     -> TestRibbonConnectOutcomeClassification
//	s_inConnect STA re-entrancy guard            -> TestRibbonConnectReentrancyGuard
//	bounce-vs-direct Application acquisition     -> TestRibbonConnectAcquiresAppThroughTheContext
//	the 0/1/2 state gate + the 60-attempt cap    -> TestRibbonConnectStateGateAndGiveUpCap
//	every connect log string                     -> TestRibbonConnectStateGateAndGiveUpCap
//	the five retry-budget constants + counters   -> TestRibbonConnectRetryBudgetConstants
//	the removed STA WM_TIMER residual            -> TestRibbonConnectHasNoIdleTimerResidual
//
// That is a strengthening, not a loss: those were greps over generated text that
// only ever proved the template still SAID the right thing, and they were
// duplicated identically across three bounce-mode renders. What CANNOT move is
// whether a given project is WIRED to the asset at all — the context it must
// publish, the three trigger sites, and the budget accounting inside the exported
// OnTime macro (which has to stay in the generated TU because Excel resolves the
// registered procedure by name against it). That is what remains here.
//
// The scratch-workbook halves (GetActiveWorkbookName, ScratchCloseEventSuppressor)
// moved earlier in the same pass; their body assertions are in
// internal/assets/scratch_book_cpp_test.go.

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
// This test pins the parts that are still GENERATED:
//   - the bounce helper GetExcelApplicationOrBounce exists and uses the verified
//     xlc* opcodes through xll::CallExcel;
//   - the close is issued only by IDENTITY (the captured active-workbook name);
//   - xlAutoOpen drives the connect through TryConnectRibbon with allowBounce=true;
//   - the calc-end handler retries WITHOUT bouncing (defensive fallback);
//   - the CalculationEnded event is still registered whenever the ribbon is enabled.
func TestXllMainRibbonDeferredConnect(t *testing.T) {
	t.Parallel()
	src := renderCppMain(t, ribbonConnectCfg())

	for _, want := range []string{
		// The relocated machinery is reachable from the generated TU.
		`#include "com/ribbon_connect.h"`,
		// The temp-workbook bounce helper STAYS generated (ribbon.bounce-branched).
		"static IDispatch* GetExcelApplicationOrBounce()",
		// It uses the verified xlc* command opcodes via xll::CallExcel.
		"xll::CallExcel(xlcNew, nullptr, 5)",
		"xll::CallExcel(xlcWorkbookInsert, nullptr, 6)",
		"xll::CallExcel(xlcFileClose, nullptr, false)",
		// HIGH (data-loss) hardening: the bounce captures the ACTIVE workbook name
		// (GET.DOCUMENT(88), now xll::ribbon::GetActiveWorkbookName in
		// src/scratch_book.cpp) and closes the scratch book BY IDENTITY — only
		// while it is still the active one.
		"std::wstring scratchName = xll::ribbon::GetActiveWorkbookName();",
		"std::wstring activeNow = xll::ribbon::GetActiveWorkbookName();",
		// The close is guarded by the identity comparison, never issued blindly.
		"if (activeNow.empty() || activeNow != scratchName) {",
		// xlAutoOpen drives the connect through the asset WITH the bounce enabled.
		`xll::ribbon::TryConnectRibbon("xlAutoOpen", /*allowBounce=*/true);`,
		// The bounce helper honors graceful degradation (warn, not crash).
		"SAFE_LOG_WARN(",
		// Calc-end retries the connect as a defensive fallback (no bounce).
		`xll::ribbon::TryConnectRibbon("calc end");`,
		// CalculationEnded is registered as the fallback retry hook for ribbon builds.
		"needRibbonRetry:",
		`xll::CallExcel(xlEventRegister, nullptr, L"CalculationEnded", xleventCalculationEnded);`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("xll_main.cpp (ribbon) missing %q\n---\n%s", want, src)
		}
	}

	// The relocated body must NOT be re-inlined into the template. Same discipline
	// as AGENTS.md §18.6.1's TestChunkSegmentLogicIsExtracted: without this, a
	// re-inlined copy would shadow the asset, the asset tests would keep passing,
	// and the shipped connect path would be untested code again.
	code := stripCppComments(src)
	for _, gone := range []string{
		"static bool TryConnectRibbon(",
		"static bool SetRibbonConnected(",
		"enum class RibbonAttempt",
		"static std::atomic<bool> s_inConnect",
		"static constexpr int    kRibbonRetryMaxAttempts",
		"static std::atomic<int> g_ribbonConnectState",
		"static std::atomic<bool> g_ribbonRegistered",
		"CoRegisterClassObject(GetRibbonClsid()",
		"rtd::RegisterOfficeAddinKey(g_szRibbonProgID,",
	} {
		if strings.Contains(code, gone) {
			t.Errorf("xll_main.cpp re-inlines the relocated connect machinery (%q); it must live "+
				"ONLY in include/com/ribbon_connect.h + src/ribbon_connect.cpp, or the shipped "+
				"code stops being the code the asset tests cover", gone)
		}
	}

	// The calc-end fallback must NOT bounce (a workbook already exists there).
	if strings.Contains(src, `TryConnectRibbon("calc end", true)`) ||
		strings.Contains(src, `TryConnectRibbon("calc end", /*allowBounce=*/true)`) {
		t.Errorf("calc-end retry must not enable the temp-workbook bounce (allowBounce must default to false there)")
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
	for _, gone := range []string{
		"TryConnectRibbon",
		"GetExcelApplicationOrBounce",
		"needRibbonRetry",
		"ribbon_connect.h",
		"SetConnectContext",
		// The scratch-book helpers are ribbon-only too; a non-ribbon project must
		// not even pull the header in (that regressed once — BounceMode() maps an
		// unset ribbon.bounce to "full" regardless of Ribbon.Enabled, so the
		// include had to be gated on BOTH).
		"scratch_book.h",
	} {
		if strings.Contains(noRibbonSrc, gone) {
			t.Errorf("ribbon-disabled render must not reference %q", gone)
		}
	}
}

// TestXllMainPublishesRibbonConnectContext is the wiring gate for the relocation.
//
// src/ribbon_connect.cpp cannot see a single generated symbol: the COM identity,
// the project-named registration strings, the embedded ribbon XML/images and the
// ribbon.bounce-branched Application acquisition all live in the generated TU. It
// receives them through ONE xll::ribbon::SetConnectContext call. A field left
// unassigned is a null pointer inside the COM registration path — which the asset
// refuses (ContextReady) rather than crashing on, so the symptom would be a
// silently missing ribbon tab plus one warning. Assert every field is filled.
//
// ORDER is load-bearing twice over:
//   - BEFORE xll::SetGracefulTeardownHook, because the hook's step (0) calls
//     xll::ribbon::SetRibbonConnected(false), which acquires the Application
//     through the injected acquireApp;
//   - BEFORE the first TryConnectRibbon, obviously.
func TestXllMainPublishesRibbonConnectContext(t *testing.T) {
	t.Parallel()
	src := renderCppMain(t, ribbonConnectCfg())

	for _, want := range []string{
		"xll::ribbon::ConnectContext ribbonCtx;",
		"ribbonCtx.hModule            = g_hModule;",
		"ribbonCtx.progId             = g_szRibbonProgID;",
		"ribbonCtx.clsid              = GetRibbonClsid();",
		`ribbonCtx.comFriendlyName    = L"TestProj Ribbon";`,
		`ribbonCtx.addinFriendlyName  = L"TestProj";`,
		`ribbonCtx.addinDescription   = L"TestProj ribbon helper";`,
		"ribbonCtx.ribbonXml          = kXllRibbonXml;",
		"ribbonCtx.getImages          = &GetXllRibbonImages;",
		"ribbonCtx.acquireApp         = &GetExcelApplication;",
		"ribbonCtx.acquireAppOrBounce = &GetExcelApplicationOrBounce;",
		// The CoRegisterClassObject cookie stays a global HERE and is written
		// through the context — see "COOKIE OWNERSHIP" in com/ribbon_connect.h. It
		// is deliberate: the teardown hook revokes g_ribbonCookie directly, and
		// that hook's statement order is the fix for a 100%-reproducible mso.dll
		// NULL-vtable crash (gen_office_disconnect_guard_test.go), so the
		// relocation left the hook byte-identical.
		"ribbonCtx.pClassObjectCookie = &g_ribbonCookie;",
		"xll::ribbon::SetConnectContext(ribbonCtx);",
		// The cookie itself is still declared in the generated TU.
		"DWORD g_ribbonCookie = 0;",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("xll_main.cpp does not publish the ribbon connect context field %q", want)
		}
	}

	// Cross-check against the header so a NEW ConnectContext field cannot be added
	// without a matching assignment here. Deriving the field list from the shipped
	// header (rather than restating it) is what makes this a drift guard instead of
	// a second copy of the same list.
	for _, field := range connectContextFields(t) {
		if !strings.Contains(src, "ribbonCtx."+field+" ") && !strings.Contains(src, "ribbonCtx."+field+"=") {
			t.Errorf("ConnectContext declares the field %q but xll_main.cpp never assigns it; an "+
				"unpublished field is a null pointer in the COM registration path (the asset "+
				"refuses it, so the symptom is a silently missing ribbon tab)", field)
		}
	}

	publishIdx := strings.Index(src, "xll::ribbon::SetConnectContext(ribbonCtx);")
	hookIdx := strings.Index(src, "xll::SetGracefulTeardownHook(&GracefulComTeardownHook);")
	connectIdx := strings.Index(src, `xll::ribbon::TryConnectRibbon("xlAutoOpen", /*allowBounce=*/true);`)
	if publishIdx < 0 || hookIdx < 0 || connectIdx < 0 {
		t.Fatalf("missing markers (publish=%d hook=%d connect=%d)", publishIdx, hookIdx, connectIdx)
	}
	if publishIdx > hookIdx {
		t.Errorf("SetConnectContext must run BEFORE SetGracefulTeardownHook (publish@%d hook@%d): "+
			"the hook's explicit COMAddIns disconnect acquires the Application through the "+
			"injected acquireApp", publishIdx, hookIdx)
	}
	if publishIdx > connectIdx {
		t.Errorf("SetConnectContext must run BEFORE the first TryConnectRibbon (publish@%d connect@%d)",
			publishIdx, connectIdx)
	}

	// The teardown hook still calls the (now namespaced) disconnect.
	if !strings.Contains(src, "xll::ribbon::SetRibbonConnected(false)") {
		t.Errorf("GracefulComTeardownHook must still disconnect through xll::ribbon::SetRibbonConnected(false)")
	}
}

// connectContextFields extracts the member names of xll::ribbon::ConnectContext
// from the embedded header, so TestXllMainPublishesRibbonConnectContext fails when
// a field is added to the struct without being wired in the template.
func connectContextFields(t *testing.T) []string {
	t.Helper()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	hdr, ok := m["include/com/ribbon_connect.h"]
	if !ok {
		t.Fatalf("embedded include/com/ribbon_connect.h not found in assets")
	}
	start := strings.Index(hdr, "struct ConnectContext {")
	if start < 0 {
		t.Fatalf("com/ribbon_connect.h: struct ConnectContext not found")
	}
	// "\n};" and not "};": a `CLSID clsid{};` member contains "};" mid-line.
	end := strings.Index(hdr[start:], "\n};")
	if end < 0 {
		t.Fatalf("com/ribbon_connect.h: unterminated struct ConnectContext")
	}
	body := hdr[start+len("struct ConnectContext {") : start+end]
	// One member per line: `Type name = init;`, `Type name{};` or the
	// function-pointer shape `Ret (*name)() = init;`.
	reFnPtr := regexp.MustCompile(`\(\*(\w+)\)\(\)`)
	var out []string
	for _, line := range strings.Split(body, "\n") {
		decl := strings.TrimSpace(line)
		if decl == "" || strings.HasPrefix(decl, "//") || !strings.HasSuffix(decl, ";") {
			continue
		}
		decl = strings.TrimSuffix(decl, ";")
		if i := strings.Index(decl, "="); i >= 0 {
			decl = decl[:i]
		}
		decl = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(decl), "{}"))
		if mm := reFnPtr.FindStringSubmatch(decl); mm != nil {
			out = append(out, mm[1])
			continue
		}
		fields := strings.Fields(decl)
		if len(fields) < 2 {
			continue
		}
		out = append(out, strings.TrimLeft(fields[len(fields)-1], "*&"))
	}
	if len(out) < 11 {
		t.Fatalf("only parsed %d ConnectContext fields (%v) out of the struct body; the field-drift "+
			"guard needs all of them:\n%s", len(out), out, body)
	}
	return out
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
// The runner and its registration STAY in the generated TU: Excel resolves the
// registered ON.TIME procedure by name against the exported symbol, so the export
// cannot move into an asset. Its budget CONSTANTS and COUNTERS did move (see
// internal/assets/ribbon_connect_cpp_test.go::TestRibbonConnectRetryBudgetConstants);
// what is asserted here is that the runner reads and charges THOSE.
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
				// The retry macro is exported with the shared single-source name.
				`extern "C" __declspec(dllexport) short __stdcall __xllgen_RibbonConnectRetry()`,
				"xll::RibbonConnectRetryMacroName()",
				// The runner retries the connect through the asset (no bounce on retries).
				`xll::ribbon::TryConnectRibbon("ontime retry", /*allowBounce=*/false, &retryOutcome);`,
				// Self-abort on ANY teardown, not just g_isUnloading (AGENTS.md §20.2.1 rule 2:
				// Phase 1 latches g_isQuiescing and keeps g_isUnloading FALSE across Excel's RTD
				// handshake, so the unload flag alone is not a teardown test) — BEFORE touching
				// Excel, so a leaked schedule never re-arms.
				"if (xll::TeardownStarted()) return 1;",
				// Stops once the connect resolves (connected/gave-up), reading the asset's gate.
				"if (xll::ribbon::g_ribbonConnectState.load(std::memory_order_acquire) != 0) return 1;",
				// Bounded re-arm, charging the asset's counter against the asset's budget.
				"xll::ribbon::g_ribbonRetryAttempts.fetch_add(1) + 1;",
				"if (n >= xll::ribbon::kRibbonRetryMaxAttempts) {",
				"xll::ScheduleOnTimeMacro(xll::RibbonConnectRetryMacroName(), nextDelaySec);",
				// The macro is registered (macroType=2) so xlcOnTime can target it.
				"Failed to register ribbon-connect OnTime retry macro.",
				// xlAutoOpen arms the first retry ONLY when not yet connected.
				"xll::ribbon::g_ribbonConnectState.load(std::memory_order_acquire) == 0 &&",
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
// The fix threads an outcome CLASS out of TryConnectRibbon and gives the noApp
// class its own, much longer, time-shaped budget with a relaxed poll interval,
// while the productive (Application reachable, Connect rejected) class keeps the
// original tight 30-attempt budget. Both budgets stay FINITE.
//
// The budgets and counters now live in the asset; what this test pins is the
// RUNNER's use of them — which class charges which counter, and that both hard
// stops are honored. (The values themselves:
// internal/assets/ribbon_connect_cpp_test.go::TestRibbonConnectRetryBudgetConstants.)
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
				// The runner asks TryConnectRibbon which class the failure was.
				"xll::ribbon::RibbonAttempt retryOutcome = xll::ribbon::RibbonAttempt::kNotAttempted;",
				`xll::ribbon::TryConnectRibbon("ontime retry", /*allowBounce=*/false, &retryOutcome);`,
				// …and branches on it before charging anything.
				"if (retryOutcome == xll::ribbon::RibbonAttempt::kNoApp) {",
				// The noApp class charges the SEPARATE noApp counter against the
				// SEPARATE noApp hard stop…
				"xll::ribbon::g_ribbonRetryNoAppAttempts.fetch_add(1) + 1;",
				"if (n >= xll::ribbon::kRibbonRetryNoAppMaxAttempts) {",
				// …and the poll relaxes once the fast window is spent (still bounded).
				"if (n >= xll::ribbon::kRibbonRetryNoAppFastAttempts) nextDelaySec = xll::ribbon::kRibbonRetryNoAppIdleSec;",
				// The productive class charges the productive counter/budget.
				"} else if (retryOutcome == xll::ribbon::RibbonAttempt::kRejected) {",
				"xll::ribbon::g_ribbonRetryAttempts.fetch_add(1) + 1;",
				"if (n >= xll::ribbon::kRibbonRetryMaxAttempts) {",
				// The re-arm honors the per-class spacing.
				"double nextDelaySec = xll::ribbon::kRibbonRetryIntervalSec;",
				"xll::ScheduleOnTimeMacro(xll::RibbonConnectRetryMacroName(), nextDelaySec);",
				// kNotAttempted charges NOTHING and only logs at debug (§3 FOLLOW-UP #2).
				"Ribbon: OnTime connect retry re-entered while a connect was in flight; ",
			} {
				if !strings.Contains(src, want) {
					t.Errorf("[%s] xll_main.cpp ribbon OnTime retry missing %q", tc.name, want)
				}
			}

			// The pre-fix shape: a single unconditional charge to the productive
			// budget, with no noApp branch. If this reappears, the empty-Excel
			// scenario burns its 30 attempts in 60 s again. Also the pre-fix bool
			// out-param, which billed an unattempted connect (§3 FOLLOW-UP #2).
			for _, gone := range []string{
				"s_retryAttempts",
				"if (retryNoApp) {",
				"bool retryNoApp = false;",
			} {
				if strings.Contains(src, gone) {
					t.Errorf("[%s] xll_main.cpp still contains the pre-fix retry accounting shape %q; "+
						"an attempt that never touched COM must not consume the connect-failure budget",
						tc.name, gone)
				}
			}

			// Neither budget may become unbounded: an add-in that polls Excel
			// forever is its own defect. Both hard stops must be present.
			if !strings.Contains(src, "if (n >= xll::ribbon::kRibbonRetryMaxAttempts) {") {
				t.Errorf("[%s] the productive retry budget lost its hard stop", tc.name)
			}
			if strings.Contains(src, "for (;;)") || strings.Contains(src, "while (true)") {
				t.Errorf("[%s] the ribbon retry must never spin unbounded", tc.name)
			}

			// The three outcome branches must be exhaustive and in the documented
			// order: noApp, then rejected, then the uncharged else. A missing else
			// would silently make kNotAttempted stop the chain.
			noAppIdx := strings.Index(src, "if (retryOutcome == xll::ribbon::RibbonAttempt::kNoApp) {")
			rejIdx := strings.Index(src, "} else if (retryOutcome == xll::ribbon::RibbonAttempt::kRejected) {")
			elseIdx := strings.Index(src, "Ribbon: OnTime connect retry re-entered while a connect was in flight; ")
			if noAppIdx < 0 || rejIdx < 0 || elseIdx < 0 || !(noAppIdx < rejIdx && rejIdx < elseIdx) {
				t.Errorf("[%s] the runner's outcome branches are missing or out of order "+
					"(noApp=%d rejected=%d uncharged=%d)", tc.name, noAppIdx, rejIdx, elseIdx)
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
//     double the dispatch rate, half the effective budget each. The counters moved
//     to file scope — and, since 2026-08-02, into src/ribbon_connect.cpp, which is
//     strictly stronger for the same reason (one definition per PROCESS, not per
//     rendered template) — and a start-once CAS latch (g_ribbonRetryArmed) makes
//     the arm idempotent.
func TestXllMainRibbonRetryArmRcAndSingleChain(t *testing.T) {
	t.Parallel()
	src := renderCppMain(t, ribbonConnectCfg())

	for _, want := range []string{
		// (2) Start-once CAS at the arm site, over the asset's latch: at most one
		//     chain per process.
		"bool retryExpected = false;",
		"xll::ribbon::g_ribbonRetryArmed.compare_exchange_strong(retryExpected, true)) {",
		// (1) The initial arm inspects the rc and un-latches so a later
		//     xlAutoOpen may legitimately try again (nothing is in flight).
		"int armRc = xll::ScheduleOnTimeMacro(xll::RibbonConnectRetryMacroName(), xll::ribbon::kRibbonRetryIntervalSec);",
		"if (armRc != xlretSuccess) {",
		"xll::ribbon::g_ribbonRetryArmed.store(false, std::memory_order_release);",
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
	if strings.Contains(src, "        xll::ScheduleOnTimeMacro(xll::RibbonConnectRetryMacroName(), xll::ribbon::kRibbonRetryIntervalSec);\n") {
		t.Errorf("xll_main.cpp still discards the arm rc from ScheduleOnTimeMacro; " +
			"a rejected arm would kill the retry chain silently")
	}
	// (2) No counter may live inside the macro body as a local static. Now that the
	//     counters are asset globals this is a "did not regress back" guard.
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
		// The retry honors the teardown self-abort contract at the yield points.
		// TeardownStarted() == g_isUnloading || g_isQuiescing (2026-07-29 fix):
		// Phase 1 latches the quiesce flag and then drains this counter.
		"xll::TeardownStarted()",
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
