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
	unloadIdx := strings.Index(body, "g_isUnloading.load")
	serverGuardIdx := strings.Index(body, "!g_rtdServer")
	if clearIdx < 0 {
		t.Errorf("WndProc must clear g_rtdNotifyPending FIRST so updates during the call re-post")
	}
	if notifyIdx < 0 {
		t.Errorf("WndProc must call g_rtdServer->NotifyUpdate()")
	}
	if unloadIdx < 0 || serverGuardIdx < 0 {
		t.Errorf("WndProc must guard on g_isUnloading AND g_rtdServer before NotifyUpdate")
	}
	if clearIdx >= 0 && notifyIdx >= 0 && clearIdx > notifyIdx {
		t.Errorf("WndProc must clear the coalescing flag BEFORE calling NotifyUpdate (so re-posts are not lost)")
	}
	if unloadIdx >= 0 && notifyIdx >= 0 && unloadIdx > notifyIdx {
		t.Errorf("WndProc must check g_isUnloading BEFORE calling NotifyUpdate")
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
	if !strings.Contains(sigBody, "g_isUnloading.load") {
		t.Errorf("SignalRtdUpdate must early-return on g_isUnloading")
	}

	// (d) xll_lifecycle.cpp RunDestructiveTeardown calls DestroyRtdNotifyWindow
	//     inside the XLL_RTD_ENABLED block, AFTER JoinWorker.
	lc, ok := m["src/xll_lifecycle.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/xll_lifecycle.cpp not found")
	}
	lcCode := stripCppComments(lc)
	rtdIdx := strings.Index(lcCode, "void xll::RunDestructiveTeardown()")
	if rtdIdx < 0 {
		t.Fatalf("RunDestructiveTeardown not found in xll_lifecycle.cpp")
	}
	rtdBody := lcCode[rtdIdx:]
	joinIdx := strings.Index(rtdBody, "xll::JoinWorker();")
	destroyIdx := strings.Index(rtdBody, "xll::DestroyRtdNotifyWindow();")
	gateIdx := strings.Index(rtdBody, "#ifdef XLL_RTD_ENABLED")
	if joinIdx < 0 {
		t.Fatalf("JoinWorker call not found in RunDestructiveTeardown")
	}
	if destroyIdx < 0 {
		t.Errorf("RunDestructiveTeardown must call xll::DestroyRtdNotifyWindow()")
	}
	if gateIdx < 0 {
		t.Fatalf("XLL_RTD_ENABLED block not found in RunDestructiveTeardown")
	}
	if destroyIdx >= 0 && destroyIdx < gateIdx {
		t.Errorf("DestroyRtdNotifyWindow must be inside the XLL_RTD_ENABLED block")
	}
	if destroyIdx >= 0 && destroyIdx < joinIdx {
		t.Errorf("DestroyRtdNotifyWindow must be called AFTER JoinWorker (no PostMessage can race the destroy)")
	}
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
