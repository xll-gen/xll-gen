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
// SECOND PASS (2026-08-03) — the OnTime retry CHAIN followed the connect
// machinery into the asset. The exported symbol __xllgen_RibbonConnectRetry HAS
// to stay here (Excel resolves the registered ON.TIME procedure BY NAME against
// an exported entry point, AGENTS.md §21), but its BODY — the budget accounting,
// the outcome switch, the re-arm decision — had no template variables, and
// neither did the xlAutoOpen arm block. They are now
// xll::ribbon::RunConnectRetryTick() and xll::ribbon::ArmConnectRetry(). A second
// batch of assertions moved with them, all to
// internal/assets/ribbon_connect_cpp_test.go:
//
//	the tick's teardown self-abort + state gate  -> TestRibbonConnectRetryTickGates
//	no-bounce on retries                         -> TestRibbonConnectRetryTickGates
//	which class charges which counter            -> TestRibbonConnectRetryTickBudgetAccounting
//	both hard stops + the three-branch ORDER     -> TestRibbonConnectRetryTickBudgetAccounting
//	the uncharged branch charges nothing         -> TestRibbonConnectRetryTickBudgetAccounting
//	the pre-fix s_retryAttempts / bool retryNoApp-> TestRibbonConnectRetryTickBudgetAccounting
//	the re-arm + its inspected rc                -> TestRibbonConnectRetryTickReArm
//	the START-ONCE CAS + the arm rc              -> TestRibbonConnectArmIsStartOnceAndInspectsRc
//
// What is left HERE for the retry is exactly what a render can answer and the
// asset cannot: the export exists under the name the header literal dictates, it
// is registered as a macro, its body is a thin shim into the tick and nothing
// else, and xlAutoOpen arms the chain — from xlAutoOpen specifically, because
// that is the only VALID command context for the first xlcOnTime (§23.6 HIGH #2).
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
				// The export is a THIN SHIM into the asset: SEH boundary, call,
				// return 1 — the same shape __xllgen_RunDeferredCalcEnd has.
				"xll::ribbon::RunConnectRetryTick();",
				// The macro is registered so xlcOnTime can target it. The
				// registration SHAPE (macroType 2, TypeText "I",
				// FunctionText == Procedure, non-fatal on rejection, register id
				// released) moved to xll::RegisterOnTimeMacro in
				// xll_lifecycle.h and is EXECUTED by
				// internal/assets/testdata/ontime_macro_native_test.cpp; the old
				// needle here was the failure log line, which could not tell
				// macroType 2 from macroType 1. What stays checkable is that
				// this project registers it, under the shared name.
				`xll::RegisterOnTimeMacro(*xDLL, xll::RibbonConnectRetryMacroName(), "ribbon-connect OnTime retry");`,
				// xlAutoOpen arms the chain. The arm must be issued from HERE and
				// cannot move into the asset's own initialization: xlAutoOpen is a
				// VALID command context for xlc*, a COM-event context is not
				// (§23.6 HIGH #2).
				"xll::ribbon::ArmConnectRetry();",
			} {
				if !strings.Contains(src, want) {
					t.Errorf("[%s] xll_main.cpp (ribbon) missing %q", tc.name, want)
				}
			}

			// The shim must contain NOTHING but the SEH block and the call. This
			// is the do-not-re-inline guard for the retry body (AGENTS.md §18.6.1
			// discipline): a re-inlined copy would shadow the asset, leave
			// internal/assets/ribbon_connect_cpp_test.go green, and put untested
			// code back into the shipped XLL.
			shim := retryShimBody(t, src)
			for _, gone := range []string{
				"TryConnectRibbon",
				"RibbonAttempt",
				"fetch_add",
				"kRibbonRetry",
				"ScheduleOnTimeMacro",
				"g_ribbonConnectState",
				"g_ribbonRetryArmed",
				"nextDelaySec",
			} {
				if strings.Contains(shim, gone) {
					t.Errorf("[%s] __xllgen_RibbonConnectRetry re-inlines the relocated retry body (%q); "+
						"the export must be a thin shim over xll::ribbon::RunConnectRetryTick()\n---\n%s",
						tc.name, gone, shim)
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
		"RunConnectRetryTick",
		"ArmConnectRetry",
	} {
		if strings.Contains(noRibbonSrc, gone) {
			t.Errorf("ribbon-disabled render must not emit %q", gone)
		}
	}
}

// retryShimBody slices the __xllgen_RibbonConnectRetry function body out of a
// rendered xll_main.cpp, comments stripped, so the "thin shim" assertions cannot
// be satisfied (or violated) by the prose above it — which legitimately names
// TryConnectRibbon, the budgets and the state gate while explaining what moved.
func retryShimBody(t *testing.T, src string) string {
	t.Helper()
	code := stripCppComments(src)
	const sig = `extern "C" __declspec(dllexport) short __stdcall __xllgen_RibbonConnectRetry()`
	start := strings.Index(code, sig)
	if start < 0 {
		t.Fatalf("__xllgen_RibbonConnectRetry not found in the ribbon render")
	}
	body := code[start:]
	if e := strings.Index(body, "\n}"); e > 0 {
		body = body[:e]
	}
	return body
}

// TestXllMainRibbonRetryNoAppBudget was the MED regression for the retry's
// budget-accounting bug (2026-07-26) and the LOW kNotAttempted hole after it.
//
// RETIRED HERE, NOT DELETED (2026-08-03). Every assertion it made was a grep over
// the rendered runner body, and that body is now
// xll::ribbon::RunConnectRetryTick() in src/ribbon_connect.cpp. Re-greping the
// render for it would only prove the template still SAYS the right thing about
// code it no longer contains. The assertions live — unweakened, and with two
// additions the render could not express (the uncharged branch provably charges
// nothing; the branch bodies sliced out so a marker cannot be satisfied from a
// neighbouring branch) — in
// internal/assets/ribbon_connect_cpp_test.go::TestRibbonConnectRetryTickBudgetAccounting.
//
// The scenario is worth keeping written down where the wiring is: empty Excel +
// `ribbon.bounce: off`, the retry polls against no workbook, the user opens a
// manual-calc / no-formula workbook 90 s later. If noApp attempts are charged to
// the productive budget it is already spent, and calc-end never fires for such a
// book, so the ribbon tab is delayed indefinitely — the feature failing its own
// target case. The two budgets that fix it are asset constants now
// (TestRibbonConnectRetryBudgetConstants pins their VALUES).

// TestXllMainRibbonRetryArmSiteIsWiredFromXlAutoOpen is what remains of
// TestXllMainRibbonRetryArmRcAndSingleChain after the 2026-08-03 relocation.
//
// The two MED fixes it pinned (the arm rc must be inspected; the chain counters
// and the start-once latch must not be function-local) are now asset code and
// asset tests:
//
//	the START-ONCE CAS and the un-latch-on-rejection ->
//	  internal/assets/ribbon_connect_cpp_test.go::TestRibbonConnectArmIsStartOnceAndInspectsRc
//	both arm sites inspecting the xlcOnTime rc       -> ditto + TestRibbonConnectRetryTickReArm
//	file-scope (not function-local) chain counters   -> TestRibbonConnectRetryBudgetConstants
//
// What CANNOT move, and is asserted here: the arm has to be CALLED, and called
// from xlAutoOpen. xlAutoOpen is an SDK-standard command context, which is the
// only reason Excel accepts the first xlcOnTime at all (§23.6 HIGH #2, proven on
// real Excel with zero workbooks open). An asset that armed itself from a static
// initializer or from the connect path would be issuing that command from
// whatever context happened to reach it.
func TestXllMainRibbonRetryArmSiteIsWiredFromXlAutoOpen(t *testing.T) {
	t.Parallel()
	src := renderCppMain(t, ribbonConnectCfg())

	armIdx := strings.Index(src, "xll::ribbon::ArmConnectRetry();")
	if armIdx < 0 {
		t.Fatalf("xll_main.cpp never arms the ribbon OnTime connect retry")
	}
	// Inside xlAutoOpen, after the load-time connect attempt (arming before it
	// would schedule a retry for a connect that is about to succeed).
	openIdx := strings.Index(src, `extern "C" __declspec(dllexport) int __stdcall xlAutoOpen()`)
	closeIdx := strings.Index(src, "// Event Handlers")
	connectIdx := strings.Index(src, `xll::ribbon::TryConnectRibbon("xlAutoOpen", /*allowBounce=*/true);`)
	if openIdx < 0 || closeIdx < 0 || connectIdx < 0 {
		t.Fatalf("missing markers (xlAutoOpen=%d end=%d connect=%d)", openIdx, closeIdx, connectIdx)
	}
	if !(openIdx < armIdx && armIdx < closeIdx) {
		t.Errorf("the arm must be issued from INSIDE xlAutoOpen (xlAutoOpen@%d arm@%d end@%d): it is the "+
			"command context that makes the first xlcOnTime acceptable", openIdx, armIdx, closeIdx)
	}
	if connectIdx > armIdx {
		t.Errorf("the arm must run AFTER the load-time connect attempt (connect@%d arm@%d)", connectIdx, armIdx)
	}
	// It must be SEH-wrapped like every other C-API call xlAutoOpen makes: a fault
	// inside the schedule must not abort the rest of the load.
	head := src[:armIdx]
	beginIdx := strings.LastIndex(head, "XLL_SAFE_BLOCK_BEGIN")
	endIdx := strings.LastIndex(head, "XLL_SAFE_BLOCK_END")
	if beginIdx < 0 || beginIdx < endIdx {
		t.Errorf("xll::ribbon::ArmConnectRetry() must be called inside an XLL_SAFE_BLOCK")
	}

	// Do-not-re-inline: the arm's own gating (unload / still-unconnected /
	// start-once CAS) and its rc handling are the asset's, not the template's.
	code := stripCppComments(src)
	for _, gone := range []string{
		"g_ribbonRetryArmed.compare_exchange_strong",
		"int armRc =",
		"bool retryExpected = false;",
	} {
		if strings.Contains(code, gone) {
			t.Errorf("xll_main.cpp re-inlines the relocated arm logic (%q); it must live ONLY in "+
				"src/ribbon_connect.cpp", gone)
		}
	}
	// And no counter may reappear as a function-local static anywhere in the
	// retry export (the MED #2(d) shape: two chains sharing one counter).
	retryIdx := strings.Index(src, `__stdcall __xllgen_RibbonConnectRetry()`)
	if retryIdx < 0 {
		t.Fatalf("__xllgen_RibbonConnectRetry not found in the ribbon render")
	}
	if strings.Contains(src[retryIdx:], "static std::atomic<int>") {
		t.Errorf("__xllgen_RibbonConnectRetry declares a function-local static counter; " +
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
