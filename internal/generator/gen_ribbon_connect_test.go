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
// one-shot connect at xlAutoOpen fails permanently — the ribbon tab never
// appears even after the user opens a workbook.
//
// The fix routes the connect through TryConnectRibbon (idempotent + retryable)
// and drives retries from TWO STA-thread triggers:
//   - a Win32 thread timer (SetTimer hwnd=NULL + TimerProc) armed at xlAutoOpen
//     when the first connect defers — this is the PRIMARY trigger because a
//     brand-new EMPTY workbook runs no calculation, so the calc-end hook alone
//     never fires (the v0.5.0 bug: ribbon never appears for load-then-File>New);
//   - the calc-end callback (kept as a secondary/belt-and-braces trigger).
//
// This test pins:
//   - xlAutoOpen calls TryConnectRibbon (not the old inline one-shot connect)
//     and arms the timer when it defers;
//   - the timer TimerProc retries the connect and is guarded by g_isUnloading;
//   - the timer is killed at xlAutoClose BEFORE CoRevokeClassObject;
//   - the calc-end handler also retries it;
//   - the CalculationEnded event is registered whenever the ribbon is enabled;
//   - the no-workbook-yet ("noApp") case does NOT consume the give-up budget.
//
// Before the fix the rendered xll_main.cpp connected inline in xlAutoOpen with
// no retry path, and (post v0.5.0) retried ONLY from calc-end — which never
// fires for an empty workbook. So the timer substrings below were all absent.
func TestXllMainRibbonDeferredConnect(t *testing.T) {
	t.Parallel()
	src := renderCppMain(t, ribbonConnectCfg())

	for _, want := range []string{
		// The retryable connect helper exists.
		"static bool TryConnectRibbon(const char* phase)",
		// xlAutoOpen drives the connect through it (not an inline SetRibbonConnected(true)).
		`TryConnectRibbon("xlAutoOpen")`,
		// xlAutoOpen arms the STA retry timer when the first connect defers.
		"ArmRibbonConnectTimer();",
		// The timer machinery exists: a TimerProc retry hook on the STA thread.
		"RibbonConnectTimerProc",
		`TryConnectRibbon("timer");`,
		"SetTimer(NULL, kRibbonConnectTimerId",
		// The TimerProc honors the unload self-abort contract.
		"g_isUnloading.load(std::memory_order_acquire)",
		// Teardown stops the timer before revoking the class object.
		"StopRibbonConnectTimer();",
		"KillTimer(NULL, g_ribbonConnectTimer);",
		// The no-workbook-yet case is detected and does NOT burn the give-up budget.
		"bool noApp = false;",
		"if (noApp) return false;",
		// Calc-end retries the connect on the STA thread once a workbook exists.
		`TryConnectRibbon("calc end");`,
		// The connect state is a single atomic guard (pending/connected/gave-up).
		"g_ribbonConnectState",
		// CalculationEnded is registered as the retry hook for ribbon builds.
		"needRibbonRetry:",
		`xll::CallExcel(xlEventRegister, nullptr, L"CalculationEnded", xleventCalculationEnded);`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("xll_main.cpp (ribbon) missing %q\n---\n%s", want, src)
		}
	}

	// Win32 trap lock: with hwnd=NULL, SetTimer IGNORES the passed id and
	// returns a fresh system-assigned one — KillTimer with the CONSTANT id
	// kills nothing and leaves the TimerProc armed (unmap crash on forced
	// unload). KillTimer must always use the STORED returned id.
	if strings.Contains(src, "KillTimer(NULL, kRibbonConnectTimerId") {
		t.Errorf("KillTimer must use the stored returned id g_ribbonConnectTimer, not the constant kRibbonConnectTimerId (hwnd=NULL timers ignore the id param)")
	}

	// Teardown ordering: StopRibbonConnectTimer() must precede CoRevokeClassObject
	// so no WM_TIMER can re-enter the COM registration during teardown.
	stopIdx := strings.Index(src, "StopRibbonConnectTimer();")
	revokeIdx := strings.Index(src, "CoRevokeClassObject(g_ribbonCookie)")
	if stopIdx < 0 || revokeIdx < 0 || stopIdx > revokeIdx {
		t.Errorf("StopRibbonConnectTimer() must be called before CoRevokeClassObject in xlAutoClose (stop=%d revoke=%d)", stopIdx, revokeIdx)
	}

	// The connect must NOT be wired as a single inline best-effort call in
	// xlAutoOpen without the retry path. The old code logged exactly this on a
	// failed connect; the new code never does (failures go through
	// TryConnectRibbon's bounded-attempt warning instead).
	if strings.Contains(src, `SAFE_LOG_WARN("Ribbon: COMAddIns connect failed; ribbon UI disabled.");`) {
		t.Errorf("xll_main.cpp still contains the old one-shot connect failure path (no retry)")
	}

	// Negative: ribbon-disabled render must not reference the connect helper.
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
	if strings.Contains(noRibbonSrc, "needRibbonRetry") {
		t.Errorf("ribbon-disabled render must not emit the needRibbonRetry hook")
	}
	if strings.Contains(noRibbonSrc, "RibbonConnectTimerProc") || strings.Contains(noRibbonSrc, "ArmRibbonConnectTimer") {
		t.Errorf("ribbon-disabled render must not emit the ribbon connect timer machinery")
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
