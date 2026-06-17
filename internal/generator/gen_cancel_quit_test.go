package generator

import (
	"regexp"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/assets"
	"github.com/xll-gen/xll-gen/internal/config"
)

// stripCppComments removes // line comments and /* */ block comments so the
// negative assertions below match on CODE only. These tests document the
// destructive steps in prose ("must NOT delete g_phost"), so a naive substring
// search would false-positive on the comments themselves.
var (
	reLineComment  = regexp.MustCompile(`//[^\n]*`)
	reBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

func stripCppComments(s string) string {
	s = reBlockComment.ReplaceAllString(s, "")
	s = reLineComment.ReplaceAllString(s, "")
	return s
}

// TestCancelQuitOnAutoCloseNonDestructive pins the cancel-quit teardown fix
// (design 2026-06-13, AGENTS.md §20) in the embedded src/xll_lifecycle.cpp asset.
//
// Excel calls xlAutoClose BEFORE the "Save changes? / Cancel" prompt on quit and
// it is the ONLY callback that fires on a CANCELLED quit. The pre-fix
// OnAutoClose() did irreversible teardown there (latched g_isUnloading, killed
// the server, deleted g_phost, joined the worker, ran the drains), so a cancelled
// quit left a zombie add-in. The fix makes OnAutoClose() non-destructive and
// moves the destructive work into GracefulTeardownOnce(), driven only by
// confirmed-shutdown signals.
//
// This test pins the embedded asset (the file that ships inside the XLL) so a
// refactor cannot silently reintroduce the cancelled-quit zombie hazard.
func TestCancelQuitOnAutoCloseNonDestructive(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	src, ok := m["src/xll_lifecycle.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/xll_lifecycle.cpp not found")
	}

	// Isolate the OnAutoClose body from GracefulTeardownOnce (which legitimately
	// contains the destructive steps) so the negative assertions cannot be
	// satisfied/violated by the relocated graceful path.
	const ocMarker = "int xll::OnAutoClose() {"
	ocIdx := strings.Index(src, ocMarker)
	if ocIdx < 0 {
		t.Fatalf("OnAutoClose not found in xll_lifecycle.cpp")
	}
	const gtMarker = "void xll::GracefulTeardownOnce(bool isHostShutdown) {"
	gtIdx := strings.Index(src, gtMarker)
	if gtIdx < 0 {
		t.Fatalf("GracefulTeardownOnce not found in xll_lifecycle.cpp (the destructive path must be factored out)")
	}
	if gtIdx <= ocIdx {
		t.Fatalf("expected GracefulTeardownOnce to follow OnAutoClose in the file")
	}
	onAutoCloseBody := stripCppComments(src[ocIdx:gtIdx])

	// The non-destructive OnAutoClose must NOT contain any of these destructive
	// steps — each is the cancelled-quit zombie hazard the fix removes.
	for _, forbidden := range []struct{ snippet, why string }{
		{"g_isUnloading = true", "OnAutoClose must NOT latch g_isUnloading (one-way; would zombie a cancelled quit)"},
		{"delete g_phost", "OnAutoClose must NOT delete g_phost (UDFs would null-deref after a cancelled quit)"},
		{"StopWorker", "OnAutoClose must NOT stop the worker on a cancelled quit"},
		{"JoinWorker", "OnAutoClose must NOT join the worker on a cancelled quit"},
		{"CloseHandle(g_procInfo.hJob)", "OnAutoClose must NOT close hJob (kills the server on a cancelled quit)"},
		{"SetEvent(g_procInfo.hShutdownEvent)", "OnAutoClose must NOT signal shutdown on a cancelled quit"},
		{"WaitForRtdConnectDrain", "OnAutoClose must NOT run the RTD drain on a cancelled quit"},
		{"WaitForCommandDrain", "OnAutoClose must NOT run the command drain on a cancelled quit"},
	} {
		if strings.Contains(onAutoCloseBody, forbidden.snippet) {
			t.Errorf("OnAutoClose still contains destructive step %q: %s\n---\n%s", forbidden.snippet, forbidden.why, onAutoCloseBody)
		}
	}

	// It must still be a valid xlAutoClose body: log + return 1.
	if !strings.Contains(onAutoCloseBody, "return 1;") {
		t.Errorf("OnAutoClose must still return 1 (Excel contract)\n---\n%s", onAutoCloseBody)
	}
}

// TestCancelQuitGracefulTeardownOnce pins GracefulTeardownOnce(): a single-shot
// CAS-guarded function that holds the destructive teardown relocated out of
// OnAutoClose, and is the ONLY place (besides the DETACH backstop) that latches
// g_isUnloading.
func TestCancelQuitGracefulTeardownOnce(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	src := m["src/xll_lifecycle.cpp"]

	const gtMarker = "void xll::GracefulTeardownOnce(bool isHostShutdown) {"
	gtIdx := strings.Index(src, gtMarker)
	if gtIdx < 0 {
		t.Fatalf("GracefulTeardownOnce not found in xll_lifecycle.cpp")
	}
	body := src[gtIdx:]

	for _, want := range []string{
		// Single-shot CAS guard so the heavy body runs exactly once regardless of
		// which confirmed-shutdown signal fires first.
		"g_teardownDone.compare_exchange_strong",
		// It latches g_isUnloading (the relocated, confirmed-shutdown latch).
		"g_isUnloading = true",
		// It invokes the registered COM teardown hook (decoupled from this TU).
		"g_teardownHook",
		// The relocated destructive steps live here now.
		"SetEvent(g_procInfo.hShutdownEvent)",
		"StopWorker",
		"JoinWorker",
		"WaitForCommandDrain(2000)",
		"delete g_phost",
		"CloseHandle(g_procInfo.hJob)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GracefulTeardownOnce missing %q\n---\n%s", want, body)
		}
	}

	// The CAS guard must be a single atomic bool with the exactly-once semantics.
	if !strings.Contains(src, "std::atomic<bool> g_teardownDone") {
		t.Errorf("xll_lifecycle.cpp missing the std::atomic<bool> g_teardownDone single-shot guard")
	}
	// The teardown hook plumbing must exist so the ribbon/RTD TU can register its
	// COM-specific destructive steps without coupling lifecycle.cpp to them.
	if !strings.Contains(src, "void SetGracefulTeardownHook(void (*hook)(bool))") {
		t.Errorf("xll_lifecycle.cpp missing SetGracefulTeardownHook")
	}
}

// TestCancelQuitDetachClosesJob pins the DLL_PROCESS_DETACH universal backstop:
// it must close hJob (kills/orphan-prevents the server in the add-in-disable
// case) while keeping the §20.2 loader-lock discipline — signal + detach, no
// thread join, no g_phost delete.
func TestCancelQuitDetachClosesJob(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	src := m["src/xll_lifecycle.cpp"]

	const detachMarker = "case DLL_PROCESS_DETACH:"
	dIdx := strings.Index(src, detachMarker)
	if dIdx < 0 {
		t.Fatalf("DLL_PROCESS_DETACH case not found")
	}
	// Bound the slice to the DETACH case (ends at the next case/closing of switch
	// — OnAutoClose follows the DllMain function, so cut at OnAutoClose).
	end := strings.Index(src[dIdx:], "int xll::OnAutoClose()")
	if end < 0 {
		end = len(src) - dIdx
	}
	detachBody := stripCppComments(src[dIdx : dIdx+end])

	// The new server-kill: closing hJob (KILL_ON_JOB_CLOSE) prevents an orphaned
	// server on add-in disable. This is the one addition the fix makes to DETACH.
	if !strings.Contains(detachBody, "CloseHandle(g_procInfo.hJob)") {
		t.Errorf("DLL_PROCESS_DETACH must CloseHandle(g_procInfo.hJob) to reap the server (orphan-prevent on add-in disable)\n---\n%s", detachBody)
	}
	// The loader-lock-safe signal must still be there.
	if !strings.Contains(detachBody, "SetEvent(g_procInfo.hShutdownEvent)") {
		t.Errorf("DLL_PROCESS_DETACH must still SetEvent(hShutdownEvent)\n---\n%s", detachBody)
	}
	if !strings.Contains(detachBody, "g_isUnloading = true") {
		t.Errorf("DLL_PROCESS_DETACH must still latch g_isUnloading\n---\n%s", detachBody)
	}
	// §20.2 invariants preserved: DETACH must NOT join threads or delete g_phost
	// under the loader lock.
	if strings.Contains(detachBody, "JoinWorker") || strings.Contains(detachBody, ".join()") {
		t.Errorf("DLL_PROCESS_DETACH must NOT join threads (§20.2 loader-lock rule)\n---\n%s", detachBody)
	}
	if strings.Contains(detachBody, "delete g_phost") {
		t.Errorf("DLL_PROCESS_DETACH must NOT delete g_phost (§20.2: leak, don't crash)\n---\n%s", detachBody)
	}
	// It must still detach (not join) the worker/monitor threads.
	if !strings.Contains(detachBody, "ForceTerminateWorker()") {
		t.Errorf("DLL_PROCESS_DETACH must still ForceTerminateWorker (detach the worker)\n---\n%s", detachBody)
	}
}

// TestCancelQuitRibbonComEventsDriveTeardown pins that the confirmed-shutdown COM
// events in the embedded src/ribbon_addin.cpp drive GracefulTeardownOnce():
// OnBeginShutdown and OnDisconnection (both ext_DisconnectMode values). These are
// the signals that fire on a REAL quit / add-in-disable but NEVER on a cancelled
// quit.
func TestCancelQuitRibbonComEventsDriveTeardown(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	src, ok := m["src/ribbon_addin.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/ribbon_addin.cpp not found")
	}

	// OnBeginShutdown must call GracefulTeardownOnce.
	obsIdx := strings.Index(src, "RibbonAddIn::OnBeginShutdown(")
	if obsIdx < 0 {
		t.Fatalf("OnBeginShutdown not found in ribbon_addin.cpp")
	}
	obsBody := src[obsIdx:]
	if end := strings.Index(obsBody[1:], "RibbonAddIn::"); end > 0 {
		obsBody = obsBody[:end]
	}
	// OnBeginShutdown fires only on a real quit -> host shutdown (true), which
	// triggers the §23.6 Stage-4 RTD revoke-skip + deferred Phase-1/Phase-2 teardown.
	if !strings.Contains(obsBody, "xll::GracefulTeardownOnce(/*isHostShutdown=*/true)") {
		t.Errorf("OnBeginShutdown must call xll::GracefulTeardownOnce(/*isHostShutdown=*/true)\n---\n%s", obsBody)
	}

	// OnDisconnection must call GracefulTeardownOnce (covers BOTH ext_dm_HostShutdown
	// and ext_dm_UserClosed). It threads the mode through: ext_dm_HostShutdown =>
	// isHostShutdown=true (revoke-skip + deferred Phase-1/Phase-2, §23.6 Stage 4);
	// ext_dm_UserClosed => isHostShutdown=false (synchronous revoke, session continues).
	odIdx := strings.Index(src, "RibbonAddIn::OnDisconnection(")
	if odIdx < 0 {
		t.Fatalf("OnDisconnection not found in ribbon_addin.cpp")
	}
	odBody := src[odIdx:]
	if end := strings.Index(odBody[1:], "RibbonAddIn::"); end > 0 {
		odBody = odBody[:end]
	}
	if !strings.Contains(odBody, "xll::GracefulTeardownOnce(isHostShutdown)") {
		t.Errorf("OnDisconnection must call xll::GracefulTeardownOnce(isHostShutdown)\n---\n%s", odBody)
	}
	// It must derive isHostShutdown from the RemoveMode (§23.6 Stage B: revoke
	// only on add-in disable, skip-revoke + pump on host shutdown).
	if !strings.Contains(odBody, "RemoveMode == ext_dm_HostShutdown") {
		t.Errorf("OnDisconnection must derive isHostShutdown from (RemoveMode == ext_dm_HostShutdown)\n---\n%s", odBody)
	}
}

// TestCancelQuitTemplateXllAutoClose pins the GENERATED xlAutoClose body: the
// early destructive sequence (ribbon disconnect / CoRevoke / unregister / drain)
// must be GONE from xlAutoClose, relocated into the GracefulComTeardownHook that
// GracefulTeardownOnce() invokes only on a confirmed shutdown. The hook must be
// registered at xlAutoOpen when a ribbon/command/RTD COM add-in exists.
func TestCancelQuitTemplateXllAutoClose(t *testing.T) {
	t.Parallel()
	cfg := ribbonConnectCfg() // ribbon + one command, the showcase shape
	src := renderCppMain(t, cfg)

	// Isolate the generated xlAutoClose body so the negative assertions target
	// the early path (not the relocated hook, which legitimately holds them).
	const acMarker = "int __stdcall xlAutoClose() {"
	acIdx := strings.Index(src, acMarker)
	if acIdx < 0 {
		t.Fatalf("generated xlAutoClose not found")
	}
	acBody := src[acIdx:]
	if end := strings.Index(acBody, "\n}"); end > 0 {
		acBody = acBody[:end]
	}
	acBody = stripCppComments(acBody)

	// The early xlAutoClose path must NOT do destructive teardown.
	for _, forbidden := range []struct{ snippet, why string }{
		{"SetRibbonConnected(false)", "xlAutoClose must NOT disconnect the ribbon in the early path (cancelled quit would kill the tab)"},
		{"CoRevokeClassObject", "xlAutoClose must NOT revoke the class object in the early path"},
		{"UnregisterOfficeAddinKey", "xlAutoClose must NOT unregister the Office add-in key in the early path"},
		{"UnregisterServer", "xlAutoClose must NOT unregister the COM server in the early path"},
		{"ShutdownRibbonImageEngine", "xlAutoClose must NOT shut down the ribbon image engine in the early path"},
		{"WaitForCommandDrain", "xlAutoClose must NOT drain commands in the early path"},
	} {
		if strings.Contains(acBody, forbidden.snippet) {
			t.Errorf("generated xlAutoClose still contains %q: %s\n---\n%s", forbidden.snippet, forbidden.why, acBody)
		}
	}
	// It must just call OnAutoClose (the non-destructive body).
	if !strings.Contains(acBody, "return xll::OnAutoClose();") {
		t.Errorf("generated xlAutoClose must just `return xll::OnAutoClose();`\n---\n%s", acBody)
	}

	// The COM-event-driven teardown hook must be emitted and registered.
	if !strings.Contains(src, "static void GracefulComTeardownHook(bool revokeRtdClassObject)") {
		t.Errorf("generated source must define GracefulComTeardownHook for ribbon/command/RTD builds")
	}
	if !strings.Contains(src, "xll::SetGracefulTeardownHook(&GracefulComTeardownHook);") {
		t.Errorf("xlAutoOpen must register the COM teardown hook via SetGracefulTeardownHook")
	}
	// The relocated destructive steps must live in the hook (not lost).
	hookIdx := strings.Index(src, "static void GracefulComTeardownHook(bool revokeRtdClassObject)")
	hookBody := src[hookIdx:]
	if end := strings.Index(hookBody, "\nextern \"C\""); end > 0 {
		hookBody = hookBody[:end]
	}
	for _, want := range []string{
		"SetRibbonConnected(false)",
		"CoRevokeClassObject(g_ribbonCookie)",
		"UnregisterOfficeAddinKey(g_szRibbonProgID)",
		"ShutdownRibbonImageEngine()",
	} {
		if !strings.Contains(hookBody, want) {
			t.Errorf("GracefulComTeardownHook missing relocated step %q\n---\n%s", want, hookBody)
		}
	}
}

// TestCancelQuitTemplateNoComAddIn pins the NON-RIBBON path: a pure-function
// (no COM add-in) build must NOT emit the teardown hook or its registration —
// that path relies solely on DLL_PROCESS_DETACH + the Job's KILL_ON_JOB_CLOSE,
// and xlAutoClose is still non-destructive (just calls OnAutoClose).
func TestCancelQuitTemplateNoComAddIn(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "Sum", Return: "int", Args: []config.Arg{{Name: "a", Type: "int"}}},
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}
	// Strip comments: the shared xlAutoClose prose mentions GracefulComTeardownHook
	// by name to explain where the steps moved, which is not an emission.
	src := stripCppComments(renderCppMain(t, cfg))

	if strings.Contains(src, "GracefulComTeardownHook") {
		t.Errorf("no-COM-add-in build must NOT emit GracefulComTeardownHook")
	}
	if strings.Contains(src, "SetGracefulTeardownHook") {
		t.Errorf("no-COM-add-in build must NOT register a teardown hook")
	}
	// xlAutoClose is still present and still just delegates (non-destructive).
	if !strings.Contains(src, "return xll::OnAutoClose();") {
		t.Errorf("no-COM-add-in xlAutoClose must still `return xll::OnAutoClose();`")
	}
}
