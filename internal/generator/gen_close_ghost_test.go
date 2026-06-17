package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/assets"
)

// TestCloseGhostPhaseSplit pins the §23.6 Stage-4 close-time-ghost fix in the
// embedded src/xll_lifecycle.cpp asset: GracefulTeardownOnce must DEFER the
// destructive teardown on a host shutdown (so Excel can complete its RTD
// DisconnectData/ServerTerminate handshake against a LIVE g_phost) and run the
// destructive sequence from a separate Phase-2 helper (RunDestructiveTeardown).
//
// PROVEN ROOT CAUSE (do not regress): Excel does NOT dispatch its RTD teardown COM
// calls until AFTER OnBeginShutdown returns. The pre-Stage-4 code deleted g_phost +
// reaped the server synchronously inside that call, so DisconnectData/ServerTerminate
// found no host and Excel ghosted (windowless, holding live RTD topics). The fix
// returns FAST from Phase 1 (g_phost alive, g_isUnloading==false) and defers the
// destructive work to RunDestructiveTeardown.
//
// REMEDIATION (2026-06-17): Phase 2 is triggered ON THE STA from
// RtdServer::ServerTerminate (the COM-apartment-safe, naturally-serialized point
// after Excel finishes its RTD teardown), NOT from an off-STA watcher thread. The
// watcher (g_phase2Watcher) and its timeout loop are REMOVED — they ran destructive
// COM/teardown work off the STA and raced DLL_PROCESS_DETACH (BLOCKER C++ review).
// This test therefore asserts the watcher is GONE and Phase 1 arms no thread.
func TestCloseGhostPhaseSplit(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	src, ok := m["src/xll_lifecycle.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/xll_lifecycle.cpp not found")
	}
	code := stripCppComments(src)

	// --- Phase 2 helper exists, separately CAS-guarded. ---
	if !strings.Contains(code, "void xll::RunDestructiveTeardown()") {
		t.Errorf("missing the Phase-2 destructive-teardown helper void xll::RunDestructiveTeardown()")
	}
	if !strings.Contains(code, "std::atomic<bool> g_destructiveDone") {
		t.Errorf("missing the Phase-2 single-shot guard std::atomic<bool> g_destructiveDone (must be separate from g_teardownDone)")
	}
	if !strings.Contains(code, "g_destructiveDone.compare_exchange_strong") {
		t.Errorf("RunDestructiveTeardown must guard its body with the g_destructiveDone CAS (exactly-once across ServerTerminate + timeout + sync paths)")
	}

	// --- Isolate the GracefulTeardownOnce body (entry marker to the start of
	//     RunDestructiveTeardown) so we can assert PHASE 1 does NOT do destructive
	//     work on the host-shutdown path. ---
	const gtMarker = "void xll::GracefulTeardownOnce(bool isHostShutdown) {"
	gtIdx := strings.Index(code, gtMarker)
	if gtIdx < 0 {
		t.Fatalf("GracefulTeardownOnce not found")
	}
	rdIdx := strings.Index(code, "void xll::RunDestructiveTeardown()")
	if rdIdx <= gtIdx {
		t.Fatalf("RunDestructiveTeardown must be defined AFTER GracefulTeardownOnce")
	}
	gtBody := code[gtIdx:rdIdx]

	// Phase 1 must NOT do the destructive steps: those are deferred. If they
	// appear inside GracefulTeardownOnce's own body, the deferral has been broken
	// and the ghost will return (g_phost would be deleted before Excel's handshake).
	for _, banned := range []string{
		"StopWorker",
		"JoinWorker",
		"delete g_phost",
		"WaitForRtdConnectDrain",
		"WaitForCommandDrain",
	} {
		if strings.Contains(gtBody, banned) {
			t.Errorf("GracefulTeardownOnce body must NOT contain %q — the destructive teardown is DEFERRED to RunDestructiveTeardown (§23.6 Stage 4)\n---\n%s", banned, gtBody)
		}
	}

	// Phase 1 host-shutdown path must branch on isHostShutdown and RETURN before
	// falling through to the synchronous (non-host-shutdown) RunDestructiveTeardown
	// call — leaving g_phost alive across Excel's RTD handshake.
	if !strings.Contains(gtBody, "if (isHostShutdown) {") {
		t.Errorf("GracefulTeardownOnce must branch on isHostShutdown to defer on a host shutdown\n---\n%s", gtBody)
	}
	if !strings.Contains(gtBody, "xll::RunDestructiveTeardown();") {
		t.Errorf("the non-host-shutdown path must still call RunDestructiveTeardown synchronously\n---\n%s", gtBody)
	}

	// REMEDIATION (2026-06-17): the off-STA Phase-2 watcher thread is REMOVED. The
	// destructive teardown is triggered ON THE STA from RtdServer::ServerTerminate
	// (asserted in TestCloseGhostServerTerminateDrivesTeardown below). Running
	// destructive COM/teardown work off the STA raced DLL_PROCESS_DETACH and
	// violated COM apartment rules (BLOCKER + HIGH C++ review findings). The whole
	// asset must therefore contain NO watcher thread and NO timeout sleep loop.
	for _, banned := range []string{
		"g_phase2Watcher",
		"std::this_thread::sleep_for",
		"steady_clock",
	} {
		if strings.Contains(code, banned) {
			t.Errorf("the off-STA Phase-2 watcher must be fully removed; found %q in xll_lifecycle.cpp (§23.6 remediation: Phase 2 runs on the STA from ServerTerminate)", banned)
		}
	}
	// Phase 1 must NOT spawn any thread.
	if strings.Contains(gtBody, "std::thread") {
		t.Errorf("GracefulTeardownOnce must NOT spawn any thread on the host-shutdown path (Phase 2 is triggered from ServerTerminate on the STA)\n---\n%s", gtBody)
	}

	// --- RunDestructiveTeardown must preserve the §23.0 ordering: it latches
	//     g_isUnloading, stops/joins, runs BOTH drains, and deletes g_phost AFTER
	//     the drains. ---
	rdBody := code[rdIdx:]
	for _, want := range []string{
		"g_isUnloading = true",
		"StopWorker",
		"JoinWorker",
		"delete g_phost",
		"CloseHandle(g_procInfo.hJob)",
	} {
		if !strings.Contains(rdBody, want) {
			t.Errorf("RunDestructiveTeardown missing %q\n---\n%s", want, rdBody)
		}
	}
	// §23.0 ordering: the command drain must precede `delete g_phost`.
	drainIdx := strings.Index(rdBody, "WaitForCommandDrain(2000)")
	delIdx := strings.Index(rdBody, "delete g_phost")
	if drainIdx < 0 || delIdx < 0 || drainIdx > delIdx {
		t.Errorf("RunDestructiveTeardown must run WaitForCommandDrain BEFORE delete g_phost (§23.0 UAF ordering)")
	}
}

// TestCloseGhostServerTerminateDrivesTeardown pins that RtdServer::ServerTerminate
// (in the embedded rtd/server.h asset) is the STA site that DRIVES the deferred
// Phase-2 destructive teardown: it must signal xll::SetRtdServerTerminated (kept for
// diagnosability/idempotence), release m_callback on the STA, and then call
// xll::RunDestructiveTeardown(). This is the §23.6 remediation (2026-06-17) replacing
// the off-STA watcher thread: ServerTerminate is the COM-apartment-safe,
// naturally-serialized point Excel calls on the STA after all DisconnectData.
func TestCloseGhostServerTerminateDrivesTeardown(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	src, ok := m["include/rtd/server.h"]
	if !ok {
		t.Fatalf("embedded asset include/rtd/server.h not found")
	}

	// Strip comments first so a doc-comment that merely MENTIONS a call cannot mask
	// its actual removal from the body (the rejected watcher shape moved the call
	// out but the explanatory comment still named it).
	srcNoComments := stripCppComments(src)
	stIdx := strings.Index(srcNoComments, "HRESULT __stdcall ServerTerminate() override {")
	if stIdx < 0 {
		t.Fatalf("ServerTerminate not found in rtd/server.h")
	}
	// Bound to the ServerTerminate body: from its opening brace to the start of the
	// next member (ReleaseCallbackForTeardown). After comment stripping the doc-block
	// is gone, so anchor on the method signature itself.
	body := srcNoComments[stIdx:]
	if end := strings.Index(body, "void ReleaseCallbackForTeardown()"); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "xll::SetRtdServerTerminated()") {
		t.Errorf("RtdServer::ServerTerminate must call xll::SetRtdServerTerminated() to record handshake completion (§23.6)\n---\n%s", body)
	}
	// Must release m_callback on the STA (its normal job).
	if !strings.Contains(body, "m_callback->Release();") {
		t.Errorf("RtdServer::ServerTerminate must release m_callback on the STA\n---\n%s", body)
	}
	// Must DRIVE the destructive teardown directly (the remediation: no watcher).
	if !strings.Contains(body, "xll::RunDestructiveTeardown();") {
		t.Errorf("RtdServer::ServerTerminate must call xll::RunDestructiveTeardown() to drive the deferred Phase-2 teardown on the STA (§23.6 remediation 2026-06-17)\n---\n%s", body)
	}
	// The teardown entry point + the signal must be declared as lifecycle entry
	// points in the header asset (so server.h can call them).
	lc, ok := m["include/xll_lifecycle.h"]
	if !ok {
		t.Fatalf("embedded asset include/xll_lifecycle.h not found")
	}
	if !strings.Contains(lc, "void SetRtdServerTerminated();") {
		t.Errorf("xll_lifecycle.h must declare void SetRtdServerTerminated()")
	}
	if !strings.Contains(lc, "void RunDestructiveTeardown();") {
		t.Errorf("xll_lifecycle.h must declare void RunDestructiveTeardown() so rtd/server.h can drive Phase 2 from the STA")
	}
}

// TestCloseGhostNoDiagInstrumentation pins that the temporary Stage-1/2/3 DiagLog
// instrumentation has been fully removed from the shipping assets (the close-time
// ghost is resolved). A reintroduction would re-add an unconditional log channel
// that bypasses the g_isUnloading suppression — fine for a debugging pass, but it
// must not ship.
func TestCloseGhostNoDiagInstrumentation(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	for _, path := range []string{
		"src/xll_lifecycle.cpp",
		"src/ribbon_addin.cpp",
		"src/xll_rtd.cpp",
		"src/xll_deferred_commands.cpp",
		"include/rtd/server.h",
		"include/xll_log.h",
	} {
		src, ok := m[path]
		if !ok {
			t.Fatalf("embedded asset %s not found", path)
		}
		code := stripCppComments(src)
		if strings.Contains(code, "DiagLog(") || strings.Contains(code, "void DiagLog") {
			t.Errorf("%s still contains DiagLog instrumentation — must be removed for the shipped fix", path)
		}
	}
	// The DiagLog definition must be gone from xll_log.cpp too.
	if src, ok := m["src/xll_log.cpp"]; ok {
		if strings.Contains(stripCppComments(src), "DiagLog") {
			t.Errorf("src/xll_log.cpp still defines DiagLog — must be removed")
		}
	}
}
