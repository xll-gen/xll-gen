package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/assets"
	"github.com/xll-gen/xll-gen/internal/templates"
)

// TestRtdNotifyWindowStaRouting pins the IMPROVEMENT_BACKLOG §0 [MED] fix:
// RtdServer::NotifyUpdate must NOT be called directly from the background worker
// thread (a raw cross-apartment COM call on Excel's STA-obtained
// IRTDUpdateEvent). Instead the worker PostMessages a coalesced signal to a
// hidden HWND_MESSAGE window created on the STA; Excel's STA pump dispatches the
// WndProc, which calls NotifyUpdate on the correct apartment.
//
// All asserts are asset-level (grep the embedded assets via assets.Assets(),
// comments stripped) mirroring gen_rtd_terminate_gate_test.go.
func TestRtdNotifyWindowStaRouting(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}

	// (a) xll_rtd.cpp ProcessRtdUpdate routes through xll::SignalRtdUpdate() and
	//     does NOT call g_rtdServer->NotifyUpdate() directly anymore.
	rtd, ok := m["src/xll_rtd.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/xll_rtd.cpp not found")
	}
	rtdCode := stripCppComments(rtd)
	if !strings.Contains(rtdCode, "xll::SignalRtdUpdate();") {
		t.Errorf("ProcessRtdUpdate must call xll::SignalRtdUpdate() (STA-routed notify)")
	}
	if strings.Contains(rtdCode, "g_rtdServer->NotifyUpdate()") {
		t.Errorf("ProcessRtdUpdate must NOT call g_rtdServer->NotifyUpdate() directly from the worker thread anymore (cross-apartment COM call)")
	}
	if !strings.Contains(rtdCode, "#include \"xll_rtd_notify.h\"") {
		t.Errorf("xll_rtd.cpp must include xll_rtd_notify.h")
	}

	// (b) The new assets exist and define the three entry points plus an
	//     HWND_MESSAGE window, a WM_APP-based message, and a coalescing atomic.
	hdr, ok := m["include/xll_rtd_notify.h"]
	if !ok {
		t.Fatalf("embedded asset include/xll_rtd_notify.h not found")
	}
	for _, fn := range []string{
		"void CreateRtdNotifyWindow();",
		"void SignalRtdUpdate();",
		"void DestroyRtdNotifyWindow();",
	} {
		if !strings.Contains(hdr, fn) {
			t.Errorf("xll_rtd_notify.h must declare %q", fn)
		}
	}
	if !strings.Contains(hdr, "#ifdef XLL_RTD_ENABLED") {
		t.Errorf("xll_rtd_notify.h must be gated on XLL_RTD_ENABLED")
	}

	src, ok := m["src/xll_rtd_notify.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/xll_rtd_notify.cpp not found")
	}
	srcCode := stripCppComments(src)
	if !strings.Contains(srcCode, "#ifdef XLL_RTD_ENABLED") {
		t.Errorf("xll_rtd_notify.cpp must be gated on XLL_RTD_ENABLED")
	}
	for _, def := range []string{
		"void CreateRtdNotifyWindow()",
		"void SignalRtdUpdate()",
		"void DestroyRtdNotifyWindow()",
	} {
		if !strings.Contains(srcCode, def) {
			t.Errorf("xll_rtd_notify.cpp must define %q", def)
		}
	}
	// HWND_MESSAGE message-only window.
	if !strings.Contains(srcCode, "HWND_MESSAGE") {
		t.Errorf("xll_rtd_notify.cpp must create an HWND_MESSAGE (message-only) window")
	}
	if !strings.Contains(srcCode, "CreateWindowExW") {
		t.Errorf("xll_rtd_notify.cpp must CreateWindowExW the notify window")
	}
	// WM_APP-based coalesced message.
	if !strings.Contains(srcCode, "WM_XLLGEN_RTD_NOTIFY = WM_APP") {
		t.Errorf("xll_rtd_notify.cpp must define WM_XLLGEN_RTD_NOTIFY = WM_APP + N")
	}
	// PostMessage is the non-blocking signal from the worker.
	if !strings.Contains(srcCode, "PostMessageW") {
		t.Errorf("SignalRtdUpdate must PostMessage (non-blocking) to the notify window")
	}
	// Coalescing atomic.
	if !strings.Contains(srcCode, "std::atomic<bool> g_rtdNotifyPending") {
		t.Errorf("xll_rtd_notify.cpp must use a coalescing std::atomic<bool> g_rtdNotifyPending")
	}
	if !strings.Contains(srcCode, "g_rtdNotifyPending.exchange(true") {
		t.Errorf("SignalRtdUpdate must coalesce via g_rtdNotifyPending.exchange(true...)")
	}
	if !strings.Contains(srcCode, "DestroyWindow") {
		t.Errorf("DestroyRtdNotifyWindow must DestroyWindow the notify window")
	}

	// (c) The WndProc clears the coalescing flag FIRST, then guards on
	//     g_isUnloading AND g_rtdServer before calling NotifyUpdate.
	procIdx := strings.Index(srcCode, "RtdNotifyWndProc(")
	if procIdx < 0 {
		t.Fatalf("RtdNotifyWndProc not found in xll_rtd_notify.cpp")
	}
	body := srcCode[procIdx:]
	if end := strings.Index(body, "} // anonymous namespace"); end > 0 {
		body = body[:end]
	}
	clearIdx := strings.Index(body, "g_rtdNotifyPending.store(false")
	notifyIdx := strings.Index(body, "g_rtdServer->NotifyUpdate();")
	// TeardownStarted() == g_isUnloading || g_isQuiescing. The quiesce half is what
	// stops a QUEUED notify from driving IRTDUpdateEvent::UpdateNotify into an Excel
	// that has already begun shutting down: on a host shutdown g_isUnloading stays
	// false across Excel's whole RTD handshake, so the old g_isUnloading-only guard
	// let those through (2026-07-29 close-time crash; see xll_lifecycle.h).
	unloadIdx := strings.Index(body, "xll::TeardownStarted()")
	serverGuardIdx := strings.Index(body, "!g_rtdServer")
	if clearIdx < 0 {
		t.Errorf("WndProc must clear g_rtdNotifyPending FIRST so updates during the call re-post")
	}
	if notifyIdx < 0 {
		t.Errorf("WndProc must call g_rtdServer->NotifyUpdate()")
	}
	if unloadIdx < 0 || serverGuardIdx < 0 {
		t.Errorf("WndProc must guard on xll::TeardownStarted() AND g_rtdServer before NotifyUpdate")
	}
	if clearIdx >= 0 && notifyIdx >= 0 && clearIdx > notifyIdx {
		t.Errorf("WndProc must clear the coalescing flag BEFORE calling NotifyUpdate (so re-posts are not lost)")
	}
	if unloadIdx >= 0 && notifyIdx >= 0 && unloadIdx > notifyIdx {
		t.Errorf("WndProc must check xll::TeardownStarted() BEFORE calling NotifyUpdate")
	}
	if serverGuardIdx >= 0 && notifyIdx >= 0 && serverGuardIdx > notifyIdx {
		t.Errorf("WndProc must check g_rtdServer BEFORE calling NotifyUpdate")
	}

	// SignalRtdUpdate must bail on g_isUnloading and a null window from any thread.
	sigIdx := strings.Index(srcCode, "void SignalRtdUpdate()")
	if sigIdx < 0 {
		t.Fatalf("SignalRtdUpdate definition not found")
	}
	sigBody := srcCode[sigIdx:]
	if end := strings.Index(sigBody, "void DestroyRtdNotifyWindow()"); end > 0 {
		sigBody = sigBody[:end]
	}
	if !strings.Contains(sigBody, "TeardownStarted()") {
		t.Errorf("SignalRtdUpdate must early-return on xll::TeardownStarted() (quiesce OR unload)")
	}

	// (d) xll_lifecycle.cpp destroys the notify window on the STA, inside an
	//     XLL_RTD_ENABLED block, AFTER the worker has been stopped and reaped.
	//     Since the 2026-07-29 close-time fix the PRIMARY site is Phase 1's
	//     BeginQuiesce (which is where the worker reap lives, and which runs while
	//     the image is still certainly mapped); RunDestructiveTeardown keeps an
	//     idempotent repeat. Assert BOTH sites, and that the primary one orders the
	//     destroy after the worker reap.
	lc, ok := m["src/xll_lifecycle.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/xll_lifecycle.cpp not found")
	}
	lcCode := stripCppComments(lc)
	rtdIdx := strings.Index(lcCode, "void xll::RunDestructiveTeardown()")
	if rtdIdx < 0 {
		t.Fatalf("RunDestructiveTeardown not found in xll_lifecycle.cpp")
	}
	// Primary site: BeginQuiesce (Phase 1).
	bqIdx := strings.Index(lcCode, "static void BeginQuiesce(bool hostShutdown)")
	if bqIdx < 0 {
		t.Fatalf("BeginQuiesce not found in xll_lifecycle.cpp")
	}
	bqBody := lcCode[bqIdx:]
	if end := strings.Index(bqBody, "int xll::RegisterFunction("); end > 0 {
		bqBody = bqBody[:end]
	}
	if !strings.Contains(bqBody, "xll::DestroyRtdNotifyWindow();") {
		t.Errorf("BeginQuiesce (Phase 1) must destroy the RTD notify window on the STA - a leaked "+
			"message-only WndProc is a raw code pointer the OS keeps dispatching after the image is "+
			"unmapped (AGENTS.md 20.3 / the 2026-07-29 close-time crash)\n---\n%s", bqBody)
	}
	if !strings.Contains(bqBody, "#ifdef XLL_RTD_ENABLED") {
		t.Errorf("BeginQuiesce's notify-window destroy must sit inside an XLL_RTD_ENABLED block")
	}
	// The destroy must follow the REAP, not merely the StopWorker request: only once
	// the worker has actually RETURNED is it impossible for a SignalRtdUpdate ->
	// PostMessage to race the DestroyWindow (review LOW #5 — the ordering
	// xll_rtd_notify.cpp documents as its own precondition).
	bqStopIdx := strings.Index(bqBody, "xll::StopWorker();")
	bqReapIdx := strings.Index(bqBody, "xll::JoinWorker();")
	bqDestroyIdx := strings.Index(bqBody, "xll::DestroyRtdNotifyWindow();")
	if bqStopIdx < 0 || bqDestroyIdx < 0 || bqStopIdx > bqDestroyIdx {
		t.Errorf("BeginQuiesce must StopWorker BEFORE destroying the notify window (no PostMessage may race the destroy)")
	}
	if bqReapIdx < 0 || bqReapIdx > bqDestroyIdx {
		t.Errorf("BeginQuiesce must REAP the worker (JoinWorker) BEFORE destroying the notify window: "+
			"a still-running worker can PostMessage to the HWND we are about to destroy "+
			"(reap@%d destroy@%d)", bqReapIdx, bqDestroyIdx)
	}

	rtdBody := lcCode[rtdIdx:]
	destroyIdx := strings.Index(rtdBody, "xll::DestroyRtdNotifyWindow();")
	gateIdx := strings.Index(rtdBody, "#ifdef XLL_RTD_ENABLED")
	if destroyIdx < 0 {
		t.Errorf("RunDestructiveTeardown must call xll::DestroyRtdNotifyWindow()")
	}
	if gateIdx < 0 {
		t.Fatalf("XLL_RTD_ENABLED block not found in RunDestructiveTeardown")
	}
	if destroyIdx >= 0 && destroyIdx < gateIdx {
		t.Errorf("DestroyRtdNotifyWindow must be inside the XLL_RTD_ENABLED block")
	}
	// (The "after the worker reap" ordering is asserted on the PRIMARY site above.
	// RunDestructiveTeardown no longer joins anything — it must not park, see
	// TestCloseUnloadNoParkAfterPhase1 — so there is no join to order against here;
	// its DestroyRtdNotifyWindow is the idempotent repeat.)
	if !strings.Contains(lcCode, "#include \"xll_rtd_notify.h\"") {
		t.Errorf("xll_lifecycle.cpp must include xll_rtd_notify.h (RTD-gated)")
	}

	// (e) The template xll_main.cpp.tmpl xlAutoOpen calls CreateRtdNotifyWindow in
	//     the RTD branch.
	tmpl, err := templates.Get("xll_main.cpp.tmpl")
	if err != nil {
		t.Fatalf("templates.Get(xll_main.cpp.tmpl): %v", err)
	}
	if !strings.Contains(tmpl, "xll::CreateRtdNotifyWindow();") {
		t.Errorf("xll_main.cpp.tmpl xlAutoOpen must call xll::CreateRtdNotifyWindow() in the RTD branch")
	}
	// It must be inside an {{if .Rtd.Enabled}} block and precede the worker start.
	createIdx := strings.Index(tmpl, "xll::CreateRtdNotifyWindow();")
	startIdx := strings.Index(tmpl, "xll::StartWorker();")
	if createIdx >= 0 && startIdx >= 0 && createIdx > startIdx {
		t.Errorf("CreateRtdNotifyWindow must be called BEFORE xll::StartWorker() (window must exist before RTD updates arrive)")
	}
	if !strings.Contains(tmpl, "#include \"xll_rtd_notify.h\"") {
		t.Errorf("xll_main.cpp.tmpl must include xll_rtd_notify.h in the RTD-gated block")
	}
}
