package generator

import (
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
		// The retryable connect helper exists and threads allowBounce.
		"static bool TryConnectRibbon(const char* phase, bool allowBounce = false)",
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
		// The no-workbook-yet case is detected and does NOT burn the give-up budget.
		"bool noApp = false;",
		"if (noApp) return false;",
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
