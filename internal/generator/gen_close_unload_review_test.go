package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/assets"
	"github.com/xll-gen/xll-gen/internal/templates"
)

// Regression pins for the xll-cpp-reviewer findings on the 2026-07-29 close-time
// use-after-unload fix (AGENTS.md §20.2.1 / §23.6 Stage 5). Each of these guards a
// hazard the review found in the FIX itself, not in the original defect.

// TestCloseUnloadPhase2HandleGating pins review MED #4.
//
// MonitorProcess parks in WaitForMultipleObjects(2, { hProcess, hShutdownEvent }) —
// on exactly those two handles. If Phase 1 had to DETACH the monitor rather than reap
// it, that thread is still parked on them, so Phase 2 closing them lets the handle
// VALUES be recycled by an unrelated object; its signalling then wakes the monitor
// into the WAIT_OBJECT_0 branch and a GetExitCodeProcess on a foreign handle, whose
// visible end is a bogus "the server crashed" report during shutdown about a process
// that never crashed. (That report was a modal MessageBoxW until 2026-08-02 and is a
// native-log ERROR now; the handle-recycling defect is identical either way.) They must
// therefore be gated on the SAME reap flag as `delete g_phost`. hJob is the exception:
// closing it IS the server reap (KILL_ON_JOB_CLOSE) and nothing waits on it.
func TestCloseUnloadPhase2HandleGating(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	code := stripCppComments(m["src/xll_lifecycle.cpp"])
	rdIdx := strings.Index(code, "void xll::RunDestructiveTeardown()")
	if rdIdx < 0 {
		t.Fatalf("RunDestructiveTeardown not found")
	}
	rd := code[rdIdx:]

	jobIdx := strings.Index(rd, "CloseHandle(g_procInfo.hJob)")
	procIdx := strings.Index(rd, "CloseHandle(g_procInfo.hProcess)")
	evtIdx := strings.Index(rd, "CloseHandle(g_procInfo.hShutdownEvent)")
	if jobIdx < 0 || procIdx < 0 || evtIdx < 0 {
		t.Fatalf("expected all three CloseHandle sites in RunDestructiveTeardown (job@%d proc@%d evt@%d)", jobIdx, procIdx, evtIdx)
	}
	// hJob must be closed BEFORE the gate opens, i.e. unconditionally.
	gateIdx := strings.Index(rd, "if (g_backgroundThreadsReaped.load(std::memory_order_acquire)) {")
	if gateIdx < 0 {
		t.Fatalf("RunDestructiveTeardown must gate the two WAITABLE handles on g_backgroundThreadsReaped " +
			"(review MED #4): a detached MonitorProcess is parked on exactly hProcess + hShutdownEvent")
	}
	if jobIdx > gateIdx {
		t.Errorf("CloseHandle(hJob) must stay UNCONDITIONAL and precede the reap gate — it is the "+
			"KILL_ON_JOB_CLOSE server reap and nothing waits on it (job@%d gate@%d)", jobIdx, gateIdx)
	}
	if procIdx < gateIdx || evtIdx < gateIdx {
		t.Errorf("CloseHandle(hProcess) and CloseHandle(hShutdownEvent) must both sit INSIDE the "+
			"g_backgroundThreadsReaped gate (gate@%d proc@%d evt@%d)", gateIdx, procIdx, evtIdx)
	}
	// And the miss must be reported, not silent.
	if !strings.Contains(rd, "hProcess/hShutdownEvent deliberately LEAKED") {
		t.Errorf("the leak-instead-of-close path must log why (review LOW #7: at WARN level)")
	}
}

// TestCloseUnloadDisablePathBoundedReapAndPin pins review MED #3 (second round).
//
// The no-park rule (§20.2.1 rule 3) is UNCONDITIONAL, so both paths use the bounded,
// exit-flag-first reap. The add-in-DISABLE path cannot keep the historical
// unconditional blocking join: a bare g_monitorThread.join() parks the STA for however
// long MonitorProcess takes to return, and Excel is frozen for all of it.
//
// The historical trigger was that MonitorProcess popped a MODAL MessageBoxW on finding
// the Go server dead, making that park unbounded. That dialog was REMOVED on 2026-08-02
// (xll_launch.cpp logs at ERROR now). This test still stands, and the assertion below is
// deliberately about the SHAPE of the reap rather than about the dialog: rule 3 forbids
// parking whatever the reason, MonitorProcess still does real bounded work (waiting on
// the child, reading a log tail) that a hung filesystem can stretch, and a future edit
// can add a slow step back. Anyone who deletes the bound because "there is no dialog
// any more" has read a historical rationale as a current one.
//
// The detach that a budget miss falls back to would, on THAT path, leave a thread
// running inside an image Excel unmaps as soon as OnDisconnection returns — so a miss
// there (and ONLY there; the host path pinned already) must also PIN. The normal
// disable case, both threads reaped, pins nothing and unloads exactly as before.
func TestCloseUnloadDisablePathBoundedReapAndPin(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	code := stripCppComments(m["src/xll_lifecycle.cpp"])

	if !strings.Contains(code, "static void BeginQuiesce(bool hostShutdown)") {
		t.Fatalf("BeginQuiesce must take the path as a parameter (review MED #3)")
	}
	bqIdx := strings.Index(code, "static void BeginQuiesce(bool hostShutdown)")
	bq := code[bqIdx:]
	if e := strings.Index(bq, "int xll::RegisterFunction("); e > 0 {
		bq = bq[:e]
	}

	// ONE bounded reap, not two strategies: the exit-flag waits must NOT sit inside an
	// `if (hostShutdown)`.
	if strings.Contains(bq, "if (hostShutdown) {") {
		t.Errorf("the reap must be bounded on BOTH paths — §20.2.1 rule 3 is unconditional, and an "+
			"unconditional blocking join on the disable path parks Excel's STA for as long as "+
			"MonitorProcess takes to return\n---\n%s", bq)
	}
	for _, want := range []string{
		"xll::WaitForWorkerExit(kThreadReapBudgetMs)",
		"WaitForMonitorExit(kThreadReapBudgetMs)",
		"xll::ForceTerminateWorker();",
		"g_monitorThread.detach();",
	} {
		if !strings.Contains(bq, want) {
			t.Errorf("BeginQuiesce missing %q — both paths use the bounded, non-parking reap\n---\n%s", want, bq)
		}
	}

	// A detach on the NON-pinned path must pin.
	missGate := "if (!hostShutdown && !(workerOut && monitorOut)) {"
	pinMissIdx := strings.Index(bq, missGate)
	if pinMissIdx < 0 {
		t.Fatalf("a budget miss on the add-in-disable path must PIN the image: that path is not "+
			"otherwise pinned and Excel unmaps right after OnDisconnection returns, so the detached "+
			"thread would be executing in a hole\n---\n%s", bq)
	}
	if !strings.Contains(bq[pinMissIdx:], "PinModuleToPreventUnmap();") {
		t.Errorf("the disable-path miss branch must call PinModuleToPreventUnmap()\n---\n%s", bq[pinMissIdx:])
	}
	// ...and only there: exactly two pin call sites exist, the host-shutdown one and this.
	if got := strings.Count(code, "PinModuleToPreventUnmap();"); got != 2 {
		t.Errorf("expected EXACTLY 2 pin call sites (confirmed host shutdown + disable-path reap miss); "+
			"found %d — an unconditional pin would break unload/re-enable", got)
	}
	// The call site must forward the mode.
	if !strings.Contains(code, "BeginQuiesce(isHostShutdown);") {
		t.Errorf("GracefulTeardownOnce must forward isHostShutdown to BeginQuiesce")
	}
}

// TestCloseUnloadDrainsFeedReapFlag pins review MED #2.
//
// g_backgroundThreadsReaped is what lets Phase 2 free g_phost and the two waitable
// handles. It used to record only the two long-lived THREADS, so a timed-out
// RTD-connect / command drain logged "a sender may still be touching g_phost" and then
// let the delete proceed anyway. All four verdicts must feed it.
func TestCloseUnloadDrainsFeedReapFlag(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	code := stripCppComments(m["src/xll_lifecycle.cpp"])
	bqIdx := strings.Index(code, "static void BeginQuiesce(bool hostShutdown)")
	if bqIdx < 0 {
		t.Fatalf("BeginQuiesce not found")
	}
	bq := code[bqIdx:]
	if e := strings.Index(bq, "int xll::RegisterFunction("); e > 0 {
		bq = bq[:e]
	}
	for _, want := range []string{
		"rtdDrained = WaitForRtdConnectDrain(2000)",
		"cmdDrained = xll::ribbon::WaitForCommandDrain(2000)",
	} {
		if !strings.Contains(bq, want) {
			t.Errorf("BeginQuiesce must CAPTURE the drain verdict (%q), not just log it\n---\n%s", want, bq)
		}
	}
	if !strings.Contains(bq, "workerOut && monitorOut && rtdDrained && cmdDrained") {
		t.Errorf("g_backgroundThreadsReaped must be the AND of all four verdicts — an undrained "+
			"detached sender touches g_phost just as much as an unreaped worker (review MED #2)\n---\n%s", bq)
	}

	// And no new detached sender may be SPAWNED once quiescing: the in-flight counters
	// are incremented INSIDE the lambdas, so a request arriving after the drain finished
	// would not be covered by it.
	rtd := stripCppComments(m["src/xll_rtd.cpp"])
	cd := rtd[strings.Index(rtd, "RtdServer::ConnectData("):]
	if e := strings.Index(cd, "HRESULT __stdcall RtdServer::DisconnectData("); e > 0 {
		cd = cd[:e]
	}
	spawnIdx := strings.Index(cd, "std::thread([TopicID, strings, newVal]()")
	gateIdx := strings.Index(cd, "if (xll::TeardownStarted()) {")
	if spawnIdx < 0 {
		t.Fatalf("ConnectData's detached sender not found")
	}
	if gateIdx < 0 || gateIdx > spawnIdx {
		t.Errorf("ConnectData must refuse to SPAWN its detached sender once a teardown has started "+
			"(gate@%d spawn@%d): the in-flight guard is acquired inside the lambda, so a thread "+
			"created after Phase 1's drain finished is invisible to it", gateIdx, spawnIdx)
	}
	rb := stripCppComments(m["src/ribbon_addin.cpp"])
	si := strings.Index(rb, "void SendCommandInvoke(")
	if si < 0 {
		t.Fatalf("SendCommandInvoke not found")
	}
	body := rb[si:]
	if e := strings.Index(body, "std::thread("); e > 0 {
		body = body[:e]
	}
	// The gate is a BLOCK since 2026-08-03 (it also reports post-teardown use —
	// AGENTS.md §20.3, pinned by lifecycle_post_teardown_cpp_test.go), so
	// assert the gate and its refusal separately rather than one literal statement.
	// What must hold is unchanged: the refusal happens BEFORE the thread exists.
	gate := strings.Index(body, "if (xll::TeardownStarted())")
	if gate < 0 {
		t.Errorf("SendCommandInvoke must refuse to spawn its detached sender once a teardown has "+
			"started, BEFORE the thread exists\n---\n%s", body)
	} else if !strings.Contains(body[gate:], "return;") {
		t.Errorf("SendCommandInvoke's teardown gate must RETURN (refuse), not fall through\n---\n%s",
			body[gate:])
	}
}

// TestCloseUnloadPinnedReloadResetsFlags pins review HIGH #2 — the regression the PIN
// itself introduced.
//
// The lifecycle flags were reset in exactly one place, DLL_PROCESS_ATTACH. A pinned
// image gets no second ATTACH (FreeLibrary/LoadLibrary only move the reference count),
// so after a host-shutdown teardown the flags would stay latched for the life of the
// process: the next xlAutoOpen builds a fresh g_phost and server while MonitorThread
// returns immediately at TeardownStarted() and WorkerLoop breaks out at g_isQuiescing —
// an add-in that looks loaded but dispatches no RTD updates and no async results.
// Reachable via Application.Quit() from a COM client that keeps its Application ref
// (delivers OnBeginShutdown, so it pins, while Excel does not exit).
func TestCloseUnloadPinnedReloadResetsFlags(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	code := stripCppComments(m["src/xll_lifecycle.cpp"])
	hdr := stripCppComments(m["include/xll_lifecycle.h"])

	// The verdict type + entry point must be public (xlAutoOpen lives in the template).
	for _, want := range []string{
		"enum class FreshLoadVerdict",
		"kCleanLoad",
		"kResetAfterTeardown",
		"kUnrecoverable",
		"FreshLoadVerdict PrepareForFreshLoad();",
		"void ResetLifecycleStateForFreshLoad();",
	} {
		if !strings.Contains(hdr, want) {
			t.Errorf("xll_lifecycle.h must expose %q so xlAutoOpen can clear a latched teardown on a "+
				"pinned image (review HIGH #2)", want)
		}
	}

	pfIdx := strings.Index(code, "xll::FreshLoadVerdict xll::PrepareForFreshLoad() {")
	if pfIdx < 0 {
		t.Fatalf("PrepareForFreshLoad not defined in xll_lifecycle.cpp")
	}
	pf := code[pfIdx:]
	if e := strings.Index(pf, "\nvoid xll::"); e > 0 {
		pf = pf[:e]
	}
	// It must detect a latched teardown...
	if !strings.Contains(pf, "g_teardownDone.load(std::memory_order_acquire) || TeardownStarted()") {
		t.Errorf("PrepareForFreshLoad must detect a latched teardown via g_teardownDone OR "+
			"TeardownStarted()\n---\n%s", pf)
	}
	// ...refuse when a thread was detached rather than reaped...
	if !strings.Contains(pf, "g_backgroundThreadsReaped.load(std::memory_order_acquire)") {
		t.Errorf("PrepareForFreshLoad must gate the reset on g_backgroundThreadsReaped — reviving "+
			"background work beside a still-running detached thread is worse than refusing\n---\n%s", pf)
	}
	if !strings.Contains(pf, "FreshLoadVerdict::kUnrecoverable") {
		t.Errorf("PrepareForFreshLoad must return kUnrecoverable on the unreaped path\n---\n%s", pf)
	}
	// ...and the refusal must be loud even though g_isUnloading may still be latched
	// (every ordinary logger short-circuits on that flag).
	if !strings.Contains(pf, "LogTeardownWarn(") {
		t.Errorf("PrepareForFreshLoad must report the refusal through LogTeardownWarn: LogError/LogWarn "+
			"are suppressed while g_isUnloading is latched, which it still is here\n---\n%s", pf)
	}
	if !strings.Contains(pf, "ResetLifecycleStateForFreshLoad();") {
		t.Errorf("PrepareForFreshLoad must reset the flags on the recoverable path\n---\n%s", pf)
	}

	// The template must CALL it, before it builds anything, and honour kUnrecoverable.
	tmpl, err := templates.Get("xll_main.cpp.tmpl")
	if err != nil {
		t.Fatalf("templates.Get: %v", err)
	}
	callIdx := strings.Index(tmpl, "xll::PrepareForFreshLoad()")
	if callIdx < 0 {
		t.Fatalf("xll_main.cpp.tmpl xlAutoOpen must call xll::PrepareForFreshLoad()")
	}
	if !strings.Contains(tmpl, "case xll::FreshLoadVerdict::kUnrecoverable:") {
		t.Errorf("xlAutoOpen must handle kUnrecoverable explicitly")
	}
	// The unrecoverable arm must FAIL THE LOAD, not just log. Comment-stripped: the
	// arm's own doc-comment NAMES SAFE_LOG_ERROR (to explain why it is not used), and a
	// prose mention must not satisfy — or defeat — a structural assertion.
	tmplCode := stripCppComments(tmpl)
	unrec := tmplCode[strings.Index(tmplCode, "case xll::FreshLoadVerdict::kUnrecoverable:"):]
	if e := strings.Index(unrec, "case xll::FreshLoadVerdict::kCleanLoad:"); e > 0 {
		unrec = unrec[:e]
	}
	if !strings.Contains(unrec, "return 0;") {
		t.Errorf("the kUnrecoverable arm must return 0 from xlAutoOpen (fail loudly), not continue\n---\n%s", unrec)
	}
	// THE NEGATIVE ASSERTION (review HIGH #1). SAFE_LOG_* expands to
	// `if (!g_isUnloading) LogXxx(...)` and LogXxx re-checks the same flag — and the
	// COMMONEST way to reach kUnrecoverable is with g_isUnloading still latched from the
	// previous Phase 2. A "deliberately loud" failure logged that way is
	// double-suppressed, leaving the user nothing but "the add-in silently does not
	// load": the exact blind spot that made this defect expensive to diagnose.
	if strings.Contains(unrec, "SAFE_LOG_") {
		t.Errorf("the kUnrecoverable arm must NOT use SAFE_LOG_* — those short-circuit on "+
			"g_isUnloading, which is normally STILL LATCHED when this arm is reached, so the "+
			"failure would be silent. Use xll::LogTeardownWarn (AGENTS.md §20.2.1 rule 4)\n---\n%s", unrec)
	}
	if !strings.Contains(unrec, "xll::LogTeardownWarn(") {
		t.Errorf("the kUnrecoverable arm must report through xll::LogTeardownWarn, which bypasses the "+
			"g_isUnloading suppression by design\n---\n%s", unrec)
	}
	// It must run BEFORE the server launch / worker start.
	for _, later := range []string{"LaunchServer(", "xll::StartWorker();"} {
		if i := strings.Index(tmpl, later); i >= 0 && i < callIdx {
			t.Errorf("PrepareForFreshLoad must be called BEFORE %q (call@%d %s@%d)", later, callIdx, later, i)
		}
	}
}

// TestCloseUnloadTeardownWarnLevel pins review LOW #7: the teardown's FAILURE lines
// must survive `logging.level: warn`, which is exactly the configuration a user
// reporting a close-time problem is likely to be running. A single INFO-gated
// LogTeardown swallowed all of them.
func TestCloseUnloadTeardownWarnLevel(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	logH := stripCppComments(m["include/xll_log.h"])
	if !strings.Contains(logH, "void LogTeardownWarn(const std::string& msg);") {
		t.Fatalf("xll_log.h must declare LogTeardownWarn (review LOW #7)")
	}
	logC := stripCppComments(m["src/xll_log.cpp"])
	i := strings.Index(logC, "void LogTeardownWarn(const std::string& msg) {")
	if i < 0 {
		t.Fatalf("LogTeardownWarn not defined")
	}
	body := logC[i:]
	if e := strings.Index(body, "\n}"); e > 0 {
		body = body[:e]
	}
	if !strings.Contains(body, "LogLevel::WARN") {
		t.Errorf("LogTeardownWarn must be gated at WARN, not INFO\n---\n%s", body)
	}
	if !strings.Contains(body, "WriteLogUnconditional(") {
		t.Errorf("LogTeardownWarn must bypass the g_isUnloading suppression like LogTeardown\n---\n%s", body)
	}
	if strings.Contains(body, "g_isUnloading") {
		t.Errorf("LogTeardownWarn must NOT check g_isUnloading\n---\n%s", body)
	}

	// Every diagnosis-critical failure line must use the WARN variant. The narrative
	// lines stay on LogTeardown; what must not happen is a failure vanishing at
	// logging.level: warn.
	lc := stripCppComments(m["src/xll_lifecycle.cpp"])
	for _, frag := range []string{
		"GetModuleHandleExW(PIN) failed",
		"RTD ConnectData drain timed out",
		"CommandInvoke drain timed out",
		"worker thread did not exit within budget",
		"monitor thread did not exit within budget",
		"g_phost deliberately LEAKED",
		"hProcess/hShutdownEvent deliberately LEAKED",
	} {
		fi := strings.Index(lc, frag)
		if fi < 0 {
			t.Errorf("expected the failure message %q somewhere in xll_lifecycle.cpp", frag)
			continue
		}
		// Walk back to the nearest logger call and check which one it is.
		head := lc[:fi]
		lw := strings.LastIndex(head, "LogTeardownWarn(")
		li := strings.LastIndex(head, "LogTeardown(")
		if lw < 0 || li > lw {
			t.Errorf("failure message %q is logged through the INFO-gated LogTeardown; it must use "+
				"LogTeardownWarn so it survives logging.level: warn (review LOW #7)", frag)
		}
	}
}

// TestCloseUnloadMonitorStartFlag pins review LOW #6: publish "monitor has not exited"
// BEFORE constructing the thread. g_monitorExited starts true, so a teardown landing
// between the std::thread construction and the thread's first instruction would read a
// stale "exited" and take a REAL blocking join inside a function whose contract is
// "never park".
func TestCloseUnloadMonitorStartFlag(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	if !strings.Contains(stripCppComments(m["include/xll_lifecycle.h"]), "void MarkMonitorStarting();") {
		t.Errorf("xll_lifecycle.h must declare MarkMonitorStarting() (mirrors StartWorker's pre-store)")
	}
	if !strings.Contains(stripCppComments(m["src/xll_lifecycle.cpp"]),
		"void MarkMonitorStarting() { g_monitorExited.store(false, std::memory_order_release); }") {
		t.Errorf("MarkMonitorStarting must publish g_monitorExited=false with a release store")
	}
	tmpl, err := templates.Get("xll_main.cpp.tmpl")
	if err != nil {
		t.Fatalf("templates.Get: %v", err)
	}
	markIdx := strings.Index(tmpl, "xll::MarkMonitorStarting();")
	ctorIdx := strings.Index(tmpl, "g_monitorThread = std::thread(MonitorThread")
	if markIdx < 0 || ctorIdx < 0 || markIdx > ctorIdx {
		t.Errorf("xll_main.cpp.tmpl must call xll::MarkMonitorStarting() BEFORE constructing "+
			"g_monitorThread (mark@%d ctor@%d)", markIdx, ctorIdx)
	}
}
