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

	// Phase 1 must NOT do the steps that would break Excel's handshake: g_phost
	// must survive, g_isUnloading must stay false, and the server must not be
	// reaped. If any of these appear inside GracefulTeardownOnce's own body the
	// §23.6 Stage-4 deferral is broken and the ghost can return.
	//
	// NOTE (2026-07-29): StopWorker / the thread reap / the two §23.0 drains are
	// NO LONGER banned here — they MOVED INTO Phase 1 on purpose (BeginQuiesce),
	// because they are the operations that park or call back into Excel and they
	// are unsafe once Excel starts unmapping the XLL. What stays deferred is
	// exactly the part that would break DisconnectData: delete g_phost, the
	// g_isUnloading latch and the server reap. See TestCloseUnloadNoParkAfterPhase1.
	for _, banned := range []string{
		"delete g_phost",
		"g_isUnloading = true",
		"CloseHandle(g_procInfo.hJob)",
	} {
		if strings.Contains(gtBody, banned) {
			t.Errorf("GracefulTeardownOnce body must NOT contain %q — that part of the teardown is DEFERRED to RunDestructiveTeardown so Excel's DisconnectData still reaches a live server (§23.6 Stage 4)\n---\n%s", banned, gtBody)
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
	if strings.Contains(code, "g_phase2Watcher") {
		t.Errorf("the off-STA Phase-2 watcher must stay removed; found g_phase2Watcher in xll_lifecycle.cpp (§23.6 remediation: Phase 2 runs on the STA from ServerTerminate)")
	}
	// Phase 1 must NOT spawn any thread. (The bounded exit-flag polls the 2026-07-29
	// quiesce added use sleep_for/steady_clock on the CALLING thread — that is not a
	// watcher and is explicitly allowed; the ban is on spawning one.)
	if strings.Contains(gtBody, "std::thread") {
		t.Errorf("GracefulTeardownOnce must NOT spawn any thread on the host-shutdown path (Phase 2 is triggered from ServerTerminate on the STA)\n---\n%s", gtBody)
	}

	// --- RunDestructiveTeardown holds the deferred remainder: it latches
	//     g_isUnloading, deletes g_phost and reaps the server. ---
	rdBody := code[rdIdx:]
	for _, want := range []string{
		"g_isUnloading = true",
		"delete g_phost",
		"CloseHandle(g_procInfo.hJob)",
	} {
		if !strings.Contains(rdBody, want) {
			t.Errorf("RunDestructiveTeardown missing %q\n---\n%s", want, rdBody)
		}
	}
	// §23.0 ordering: the drains must still precede `delete g_phost`. They now live
	// in Phase 1 (BeginQuiesce), which is defined ABOVE GracefulTeardownOnce and
	// therefore above RunDestructiveTeardown — so file order encodes the ordering.
	bqIdx := strings.Index(code, "static void BeginQuiesce(bool hostShutdown)")
	if bqIdx < 0 {
		t.Fatalf("BeginQuiesce (Phase 1 quiesce) not found in xll_lifecycle.cpp")
	}
	if bqIdx > rdIdx {
		t.Errorf("BeginQuiesce must be defined before RunDestructiveTeardown (the drains it runs must precede delete g_phost — §23.0 UAF ordering)")
	}
	bqBody := code[bqIdx:gtIdx]
	for _, want := range []string{"WaitForRtdConnectDrain(2000)", "WaitForCommandDrain(2000)"} {
		if !strings.Contains(bqBody, want) {
			t.Errorf("BeginQuiesce missing the §23.0 drain %q — it must drain the detached senders BEFORE Phase 2's delete g_phost\n---\n%s", want, bqBody)
		}
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
