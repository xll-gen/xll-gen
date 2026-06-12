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
// and retries it from the calc-end callback, which fires on the STA thread once
// a workbook exists. This test pins:
//   - xlAutoOpen calls TryConnectRibbon (not the old inline one-shot connect);
//   - the calc-end handler retries it;
//   - the CalculationEnded event is registered whenever the ribbon is enabled
//     (so the retry hook actually fires).
//
// Before the fix the rendered xll_main.cpp connected inline in xlAutoOpen with
// no retry path, so none of these substrings were present.
func TestXllMainRibbonDeferredConnect(t *testing.T) {
	t.Parallel()
	src := renderCppMain(t, ribbonConnectCfg())

	for _, want := range []string{
		// The retryable connect helper exists.
		"static bool TryConnectRibbon(const char* phase)",
		// xlAutoOpen drives the connect through it (not an inline SetRibbonConnected(true)).
		`TryConnectRibbon("xlAutoOpen");`,
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
