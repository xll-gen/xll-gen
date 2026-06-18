package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/assets"
)

// TestRtdServerTerminateGatedOnHostShutdown pins the §23.6 remediation
// (2026-06-18) that fixes the reopen-crash (RPC 0x800706BA) on a workbook
// close+reopen with live RTD topics.
//
// PROVEN ROOT CAUSE (do not regress): Excel calls RtdServer::ServerTerminate
// WHENEVER the live RTD topic count drops to zero — NOT only at host shutdown.
// On an ordinary workbook close (the Excel Application stays alive, e.g. a COM
// automation client holds the Application ref) Excel issues DisconnectData per
// topic, then ServerTerminate, while OnBeginShutdown / GracefulTeardownOnce
// NEVER fire. The pre-fix ServerTerminate called xll::RunDestructiveTeardown()
// UNCONDITIONALLY, so this benign zero-topic blip killed the Go server
// (g_isUnloading, Stop/Join worker, delete g_phost, CloseHandle(hJob) =
// KILL_ON_JOB_CLOSE) while the XLL stayed loaded. The next reopen hit a dead
// server / null g_phost → 0x800706BA → AV.
//
// THE FIX: gate the destructive teardown trigger in ServerTerminate on a
// CONFIRMED host shutdown via xll::HostShutdownTeardownArmed(), armed ONLY in
// GracefulTeardownOnce's isHostShutdown Phase-1 branch and reset on
// DLL_PROCESS_ATTACH. ServerTerminate ALWAYS still releases m_callback (its
// normal job); it only conditionally drives RunDestructiveTeardown.
func TestRtdServerTerminateGatedOnHostShutdown(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}

	// --- include/rtd/server.h: ServerTerminate must GATE RunDestructiveTeardown. ---
	srv, ok := m["include/rtd/server.h"]
	if !ok {
		t.Fatalf("embedded asset include/rtd/server.h not found")
	}
	// Strip comments first so the prose explanation (which legitimately names the
	// call and the hazard) cannot mask the actual code shape.
	srvCode := stripCppComments(srv)

	// The accessor must be forward-declared so server.h can call it.
	if !strings.Contains(srvCode, "bool HostShutdownTeardownArmed();") {
		t.Errorf("rtd/server.h must forward-declare xll::HostShutdownTeardownArmed() so ServerTerminate can gate on it")
	}

	// Isolate the ServerTerminate body (signature to the next member).
	stIdx := strings.Index(srvCode, "HRESULT __stdcall ServerTerminate() override {")
	if stIdx < 0 {
		t.Fatalf("ServerTerminate not found in rtd/server.h")
	}
	body := srvCode[stIdx:]
	if end := strings.Index(body, "void ReleaseCallbackForTeardown()"); end > 0 {
		body = body[:end]
	}

	// ServerTerminate must STILL release m_callback unconditionally (its normal job).
	if !strings.Contains(body, "m_callback->Release();") {
		t.Errorf("ServerTerminate must still release m_callback on the STA (its normal job)\n---\n%s", body)
	}

	// The destructive teardown must be GATED on HostShutdownTeardownArmed(), not
	// called unconditionally. Assert the gate guards the call.
	gateIdx := strings.Index(body, "if (xll::HostShutdownTeardownArmed())")
	if gateIdx < 0 {
		t.Errorf("ServerTerminate must gate the destructive teardown on if (xll::HostShutdownTeardownArmed()) (§23.6 remediation 2026-06-18)\n---\n%s", body)
	}
	teardownIdx := strings.Index(body, "xll::RunDestructiveTeardown();")
	if teardownIdx < 0 {
		t.Errorf("ServerTerminate must still call xll::RunDestructiveTeardown() on the armed (host-shutdown) path\n---\n%s", body)
	}
	// The teardown call must come AFTER the gate (i.e. inside the armed branch),
	// proving it is not an unconditional call.
	if gateIdx >= 0 && teardownIdx >= 0 && teardownIdx < gateIdx {
		t.Errorf("xll::RunDestructiveTeardown() must appear AFTER the HostShutdownTeardownArmed() gate (inside the armed branch), not before it\n---\n%s", body)
	}

	// --- src/xll_lifecycle.cpp: arm only in the isHostShutdown branch; reset in ATTACH. ---
	lc, ok := m["src/xll_lifecycle.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/xll_lifecycle.cpp not found")
	}
	lcCode := stripCppComments(lc)

	// The accessor + backing flag must be defined.
	if !strings.Contains(lcCode, "bool HostShutdownTeardownArmed()") {
		t.Errorf("xll_lifecycle.cpp must define xll::HostShutdownTeardownArmed()")
	}
	if !strings.Contains(lcCode, "std::atomic<bool> g_hostShutdownTeardownArmed") {
		t.Errorf("xll_lifecycle.cpp must define the backing flag std::atomic<bool> g_hostShutdownTeardownArmed")
	}

	// ARM site: the store(true) must live inside the GracefulTeardownOnce
	// isHostShutdown Phase-1 branch, BEFORE its fast return. Bound the search to
	// the host-shutdown branch (from "if (isHostShutdown) {" to the synchronous
	// RunDestructiveTeardown call that follows the branch).
	gtIdx := strings.Index(lcCode, "void xll::GracefulTeardownOnce(bool isHostShutdown) {")
	if gtIdx < 0 {
		t.Fatalf("GracefulTeardownOnce not found in xll_lifecycle.cpp")
	}
	gtBody := lcCode[gtIdx:]
	if end := strings.Index(gtBody, "void xll::RunDestructiveTeardown()"); end > 0 {
		gtBody = gtBody[:end]
	}
	hostBranchIdx := strings.Index(gtBody, "if (isHostShutdown) {")
	if hostBranchIdx < 0 {
		t.Fatalf("isHostShutdown branch not found in GracefulTeardownOnce")
	}
	hostBranch := gtBody[hostBranchIdx:]
	armStmt := "g_hostShutdownTeardownArmed.store(true"
	armIdx := strings.Index(hostBranch, armStmt)
	if armIdx < 0 {
		t.Errorf("GracefulTeardownOnce must ARM the gate (g_hostShutdownTeardownArmed.store(true...)) inside the isHostShutdown branch\n---\n%s", hostBranch)
	}
	// The arm must precede the fast `return;` of the host-shutdown branch so it is
	// observable by the time ServerTerminate runs.
	retIdx := strings.Index(hostBranch, "return;")
	if armIdx >= 0 && retIdx >= 0 && armIdx > retIdx {
		t.Errorf("the gate must be armed BEFORE the host-shutdown fast return (so it is set by the time ServerTerminate fires)\n---\n%s", hostBranch)
	}

	// The arm must NOT happen anywhere else (i.e. not unconditionally / not in the
	// non-host-shutdown path). The only store(true) of the flag must be the one in
	// the host-shutdown branch.
	if got := strings.Count(lcCode, armStmt); got != 1 {
		t.Errorf("g_hostShutdownTeardownArmed.store(true...) must appear EXACTLY once (only in the isHostShutdown branch); found %d", got)
	}

	// RESET site: DLL_PROCESS_ATTACH must reset the gate to false, alongside the
	// other probe-unload-reuse resets.
	attachIdx := strings.Index(lcCode, "case DLL_PROCESS_ATTACH:")
	if attachIdx < 0 {
		t.Fatalf("DLL_PROCESS_ATTACH case not found")
	}
	attachBody := lcCode[attachIdx:]
	if end := strings.Index(attachBody, "case DLL_THREAD_ATTACH:"); end > 0 {
		attachBody = attachBody[:end]
	}
	if !strings.Contains(attachBody, "g_hostShutdownTeardownArmed.store(false") {
		t.Errorf("DLL_PROCESS_ATTACH must reset g_hostShutdownTeardownArmed to false (probe-unload-reuse symmetry)\n---\n%s", attachBody)
	}

	// --- include/xll_lifecycle.h: declare the accessor. ---
	hdr, ok := m["include/xll_lifecycle.h"]
	if !ok {
		t.Fatalf("embedded asset include/xll_lifecycle.h not found")
	}
	if !strings.Contains(hdr, "bool HostShutdownTeardownArmed();") {
		t.Errorf("xll_lifecycle.h must declare bool HostShutdownTeardownArmed()")
	}
}
