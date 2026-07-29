package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/assets"
)

// Regression pins for the 2026-07-29 close-time USE-AFTER-UNLOAD crash
// (AGENTS.md §20.2 / §23.6).
//
// MEASURED MECHANISM (real Excel x64 16.0.20228, window-close with live streaming
// plain-rtd topics, 3/3 reproduced before the fix):
//
//  1. Excel delivers OnBeginShutdown -> GracefulTeardownOnce(isHostShutdown=true).
//     Phase 1 deliberately did NOT latch g_isUnloading (so Excel's later per-topic
//     DisconnectData could still send MSG_RTD_DISCONNECT — the §23.6 Stage-4 ghost
//     fix), which ALSO meant nothing stopped the worker thread or the hidden
//     RTD-notify window.
//  2. ~80-100 ms after Phase 1 returns, Excel calls FreeLibrary on the XLL and the
//     image is REALLY UNMAPPED (DllMain DLL_PROCESS_DETACH with lpReserved==NULL).
//     On the SAME close with no live RTD topics Excel never calls FreeLibrary at
//     all — DETACH arrives at process exit with lpReserved!=NULL and nothing is
//     unmapped, which is why only the streaming case crashed.
//  3. Meanwhile RtdServer::ServerTerminate had driven Phase 2
//     (RunDestructiveTeardown) on the STA, which PARKED in `g_monitorThread.join()`.
//     `join()` parks inside libwinpthread's pthread_join, whose code lives in the
//     XLL image. When the wait returned, the STA resumed executing UNMAPPED code:
//     0xC0000005, faulting module `<proj>.xll_unloaded`, faulting RIP the
//     instruction immediately after WaitForSingleObject inside pthread_join
//     (identified by disassembling the shipped Release XLL at the WER offset).
//     Excel's own side faulted the same way, dereferencing an IRtdServer vtable in
//     the hole (0xC0000005 in EXCEL.EXE / mso20win32client.dll).
//
// THE FIX HAS THREE PARTS, each pinned below:
//   (A) PIN the image on the confirmed-host-shutdown path so FreeLibrary cannot
//       unmap code that either side still executes.
//   (B) SPLIT the overloaded g_isUnloading flag: Phase 1 latches g_isQuiescing
//       ("stop background work") WITHOUT latching g_isUnloading ("g_phost is
//       going away"), so background work stops while DisconnectData keeps working.
//   (C) NEVER PARK after Phase 1: all joins/drains move into Phase 1, joins happen
//       only after the thread's own exit flag is observed, and
//       RunDestructiveTeardown is reduced to bounded kernel calls.
//
// Every assertion here FAILS on the parent commit.

// TestCloseUnloadPinsImageOnHostShutdown pins part (A).
func TestCloseUnloadPinsImageOnHostShutdown(t *testing.T) {
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

	if !strings.Contains(code, "static void PinModuleToPreventUnmap()") {
		t.Fatalf("missing PinModuleToPreventUnmap() — without an extra module reference " +
			"Excel's FreeLibrary unmaps the XLL ~80ms after OnBeginShutdown returns, while " +
			"both Excel and the teardown still have code/vtables in flight (AGENTS.md §20.2)")
	}
	// GetModuleHandleExW with PIN, keyed off our own code address. A matched
	// LoadLibrary/FreeLibrary pair is NOT acceptable: a self-FreeLibrary that
	// happened to drop the last reference would unmap the image under its own
	// return address, which is the bug being fixed.
	for _, want := range []string{
		"GetModuleHandleExW(",
		"GET_MODULE_HANDLE_EX_FLAG_FROM_ADDRESS",
		"GET_MODULE_HANDLE_EX_FLAG_PIN",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("PinModuleToPreventUnmap must take the reference via %q", want)
		}
	}
	if strings.Contains(code, "FreeLibrary(") {
		t.Errorf("xll_lifecycle.cpp must NOT call FreeLibrary on itself — a self-unload that drops " +
			"the last reference unmaps the image under its own return address")
	}

	// The pin must be gated on isHostShutdown: the add-in-DISABLE path must still
	// unload normally, so a later re-enable gets a fresh DLL_PROCESS_ATTACH with
	// its flag resets (probe-unload-reuse symmetry, §20.2).
	const gtMarker = "void xll::GracefulTeardownOnce(bool isHostShutdown) {"
	gtIdx := strings.Index(code, gtMarker)
	rdIdx := strings.Index(code, "void xll::RunDestructiveTeardown()")
	if gtIdx < 0 || rdIdx < gtIdx {
		t.Fatalf("could not delimit the GracefulTeardownOnce body")
	}
	gtBody := code[gtIdx:rdIdx]
	if !strings.Contains(gtBody, "if (isHostShutdown) PinModuleToPreventUnmap();") {
		t.Errorf("GracefulTeardownOnce must pin the image ONLY on the confirmed-host-shutdown path "+
			"(`if (isHostShutdown) PinModuleToPreventUnmap();`); pinning on add-in disable would "+
			"break the unload/re-enable cycle\n---\n%s", gtBody)
	}
	// It must happen BEFORE the COM hook, which pumps the STA and can therefore
	// let Excel re-enter (and, further along its shutdown, unmap us).
	pinIdx := strings.Index(gtBody, "PinModuleToPreventUnmap();")
	hookIdx := strings.Index(gtBody, "g_teardownHook(")
	quiesceIdx := strings.Index(gtBody, "BeginQuiesce(isHostShutdown);")
	if pinIdx < 0 || hookIdx < 0 || pinIdx > hookIdx {
		t.Errorf("the pin must precede the COM teardown hook (the hook PUMPS the STA): pin@%d hook@%d", pinIdx, hookIdx)
	}
	if quiesceIdx < 0 || pinIdx > quiesceIdx {
		t.Errorf("the pin must precede BeginQuiesce (everything the quiesce runs must already be un-unmappable): pin@%d quiesce@%d", pinIdx, quiesceIdx)
	}
}

// TestCloseUnloadQuiesceFlagSplit pins part (B): the flag split, and — critically —
// that DisconnectData was NOT swept up in it.
func TestCloseUnloadQuiesceFlagSplit(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}

	hdr, ok := m["include/xll_lifecycle.h"]
	if !ok {
		t.Fatalf("embedded asset include/xll_lifecycle.h not found")
	}
	hdrCode := stripCppComments(hdr)
	if !strings.Contains(hdrCode, "extern std::atomic<bool> g_isQuiescing;") {
		t.Errorf("xll_lifecycle.h must declare `extern std::atomic<bool> g_isQuiescing;` — the half of " +
			"the old g_isUnloading meaning that Phase 1 needs to latch on its own")
	}
	if !strings.Contains(hdrCode, "inline bool TeardownStarted()") {
		t.Errorf("xll_lifecycle.h must expose TeardownStarted() (g_isUnloading || g_isQuiescing) as the " +
			"single predicate every background work site checks")
	}

	lc := stripCppComments(m["src/xll_lifecycle.cpp"])
	if !strings.Contains(lc, "std::atomic<bool> g_isQuiescing(false)") {
		t.Errorf("xll_lifecycle.cpp must define g_isQuiescing")
	}
	// Latched in exactly ONE place — BeginQuiesce, reached only from
	// GracefulTeardownOnce, which fires only on a CONFIRMED teardown and never on a
	// cancelled quit. That single-site property is what makes the cancelled-quit
	// exposure identical to the pre-fix code (§20.3).
	if n := strings.Count(lc, "g_isQuiescing.store(true"); n != 1 {
		t.Errorf("g_isQuiescing must be latched in EXACTLY ONE place (BeginQuiesce); found %d stores — "+
			"any second site risks latching it on a cancelled quit, which would half-kill the add-in (§20.3)", n)
	}
	// Reset on a fresh load, like g_isUnloading (probe-unload-reuse symmetry). The
	// reset now lives in ResetLifecycleStateForFreshLoad because DLL_PROCESS_ATTACH is
	// no longer its only caller: a PINNED image gets no second ATTACH, so xlAutoOpen
	// has to be able to ask for the same reset (review HIGH #2).
	rlIdx := strings.Index(lc, "void xll::ResetLifecycleStateForFreshLoad() {")
	if rlIdx < 0 {
		t.Fatalf("ResetLifecycleStateForFreshLoad not found — the ATTACH reset block must be a " +
			"callable function, or a pinned image can never clear the lifecycle flags")
	}
	rlBody := lc[rlIdx:]
	if e := strings.Index(rlBody, "\n    }"); e > 0 {
		rlBody = rlBody[:e]
	}
	for _, want := range []string{
		"g_isUnloading = false",
		"g_isQuiescing.store(false",
		"g_teardownDone = false",
		"g_destructiveDone = false",
		"g_rtdServerTerminated.store(false",
		"g_hostShutdownTeardownArmed.store(false",
		"g_backgroundThreadsReaped.store(false",
	} {
		if !strings.Contains(rlBody, want) {
			t.Errorf("ResetLifecycleStateForFreshLoad missing %q — a flag left latched after a pinned "+
				"teardown makes the next xlAutoOpen come back half-alive\n---\n%s", want, rlBody)
		}
	}
	attachIdx := strings.Index(lc, "case DLL_PROCESS_ATTACH:")
	detachIdx := strings.Index(lc, "case DLL_PROCESS_DETACH:")
	if attachIdx < 0 || detachIdx < attachIdx {
		t.Fatalf("could not delimit the DLL_PROCESS_ATTACH case")
	}
	if !strings.Contains(lc[attachIdx:detachIdx], "ResetLifecycleStateForFreshLoad()") {
		t.Errorf("DLL_PROCESS_ATTACH must delegate to ResetLifecycleStateForFreshLoad()")
	}

	// --- The gate sites that MUST honour the quiesce flag. ---
	for _, tc := range []struct{ file, fn, marker, end string }{
		{"src/xll_rtd_notify.cpp", "SignalRtdUpdate", "void SignalRtdUpdate()", "void DestroyRtdNotifyWindow()"},
		{"src/xll_rtd_notify.cpp", "RtdNotifyWndProc", "LRESULT CALLBACK RtdNotifyWndProc(", "} // anonymous namespace"},
		{"src/xll_rtd.cpp", "ConnectData", "RtdServer::ConnectData(", "HRESULT __stdcall RtdServer::DisconnectData("},
		{"src/ribbon_addin.cpp", "SendCommandInvoke", "void SendCommandInvoke(", "} } // namespace"},
	} {
		body := stripCppComments(m[tc.file])
		i := strings.Index(body, tc.marker)
		if i < 0 {
			t.Fatalf("%s: %s not found", tc.file, tc.fn)
		}
		body = body[i:]
		if j := strings.Index(body, tc.end); j > 0 {
			body = body[:j]
		}
		if !strings.Contains(body, "TeardownStarted()") {
			t.Errorf("%s: %s must gate on xll::TeardownStarted() (quiesce OR unload), not g_isUnloading alone — "+
				"on a host shutdown g_isUnloading stays FALSE across Excel's whole RTD handshake, so the old "+
				"gate let background work run right up to the unmap\n---\n%s", tc.file, tc.fn, body)
		}
	}

	// rtd/server.h NotifyUpdate: must bail on the quiesce flag too, or a queued
	// notify drives IRTDUpdateEvent::UpdateNotify into a shutting-down Excel.
	sh := stripCppComments(m["include/rtd/server.h"])
	nuIdx := strings.Index(sh, "void NotifyUpdate() {")
	if nuIdx < 0 {
		t.Fatalf("NotifyUpdate not found in rtd/server.h")
	}
	nuBody := sh[nuIdx:]
	if e := strings.Index(nuBody, "static HRESULT CreateRefreshDataArray"); e > 0 {
		nuBody = nuBody[:e]
	}
	if !strings.Contains(nuBody, "g_isQuiescing.load(std::memory_order_acquire)") {
		t.Errorf("rtd/server.h NotifyUpdate must also bail on xll::g_isQuiescing\n---\n%s", nuBody)
	}
	if !strings.Contains(sh, "extern std::atomic<bool> g_isQuiescing;") {
		t.Errorf("rtd/server.h must declare the g_isQuiescing extern it reads")
	}

	// --- THE NEGATIVE ASSERTION THAT PROTECTS §23.6. ---
	// DisconnectData's send gate must stay g_isUnloading-ONLY. If it were swept
	// into TeardownStarted(), Phase 1's quiesce would silence MSG_RTD_DISCONNECT
	// and the close-time ghost Excel (§23.6 S1') would come straight back.
	rtd := stripCppComments(m["src/xll_rtd.cpp"])
	dIdx := strings.Index(rtd, "HRESULT __stdcall RtdServer::DisconnectData(long TopicID) {")
	if dIdx < 0 {
		t.Fatalf("RtdServer::DisconnectData not found")
	}
	dBody := rtd[dIdx:]
	if !strings.Contains(dBody, "!xll::g_isUnloading.load(std::memory_order_acquire) && g_phost") {
		t.Errorf("DisconnectData must gate its MSG_RTD_DISCONNECT send on g_isUnloading AND g_phost\n---\n%s", dBody)
	}
	if strings.Contains(dBody, "TeardownStarted()") {
		t.Errorf("DisconnectData must NOT use TeardownStarted() — Phase 1 latches g_isQuiescing while " +
			"Excel is still to issue its per-topic DisconnectData, and suppressing those sends is exactly " +
			"what caused the close-time ghost Excel (AGENTS.md §23.6 S1'). This gate is g_isUnloading-only " +
			"ON PURPOSE.")
	}
}

// TestCloseUnloadNoParkAfterPhase1 pins part (C): the non-parking rule.
func TestCloseUnloadNoParkAfterPhase1(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	code := stripCppComments(m["src/xll_lifecycle.cpp"])

	bqIdx := strings.Index(code, "static void BeginQuiesce(bool hostShutdown)")
	gtIdx := strings.Index(code, "void xll::GracefulTeardownOnce(bool isHostShutdown) {")
	rdIdx := strings.Index(code, "void xll::RunDestructiveTeardown()")
	if bqIdx < 0 || gtIdx < bqIdx || rdIdx < gtIdx {
		t.Fatalf("could not delimit BeginQuiesce / GracefulTeardownOnce / RunDestructiveTeardown")
	}
	// BeginQuiesce is the last member of the `namespace xll { ... }` block at the
	// top of the file, so delimit it at RegisterFunction (the first definition after
	// that block). Slicing all the way to GracefulTeardownOnce would swallow DllMain,
	// whose DETACH backstop legitimately contains the very tokens banned below.
	bqBody := code[bqIdx:]
	if e := strings.Index(bqBody, "int xll::RegisterFunction("); e > 0 {
		bqBody = bqBody[:e]
	}
	rdBody := code[rdIdx:]

	// Phase 1 owns everything that stops background work.
	for _, want := range []string{
		"g_isQuiescing.store(true, std::memory_order_release)",
		"SetEvent(g_procInfo.hShutdownEvent)",
		"xll::StopWorker();",
		"xll::DestroyRtdNotifyWindow();",
		"WaitForRtdConnectDrain(2000)",
		"WaitForCommandDrain(2000)",
		"ReleaseCallbackForTeardown()",
	} {
		if !strings.Contains(bqBody, want) {
			t.Errorf("BeginQuiesce (Phase 1) missing %q\n---\n%s", want, bqBody)
		}
	}
	// Phase 1 must NOT do the deferred-on-purpose parts.
	for _, banned := range []string{"delete g_phost", "g_isUnloading = true", "CloseHandle(g_procInfo.hJob)"} {
		if strings.Contains(bqBody, banned) {
			t.Errorf("BeginQuiesce must NOT contain %q — g_phost, the server and g_isUnloading==false must "+
				"survive Phase 1 so Excel's DisconnectData still lands (§23.6 Stage 4)\n---\n%s", banned, bqBody)
		}
	}

	// The NON-PARKING joins: wait on the thread's own exit flag, and only then join.
	for _, want := range []string{
		"xll::WaitForWorkerExit(kThreadReapBudgetMs)",
		"WaitForMonitorExit(kThreadReapBudgetMs)",
	} {
		if !strings.Contains(bqBody, want) {
			t.Errorf("BeginQuiesce must wait on the thread EXIT FLAG with a bounded budget before joining (%q). "+
				"A bare join() parks inside libwinpthread's pthread_join — inside the XLL image — and returns "+
				"into unmapped code if Excel unmaps us meanwhile\n---\n%s", want, bqBody)
		}
	}
	workerWaitIdx := strings.Index(bqBody, "xll::WaitForWorkerExit(")
	joinIdx := strings.Index(bqBody, "xll::JoinWorker();")
	if workerWaitIdx < 0 || joinIdx < 0 || workerWaitIdx > joinIdx {
		t.Errorf("BeginQuiesce must observe WorkerExited() BEFORE calling JoinWorker() (wait@%d join@%d)", workerWaitIdx, joinIdx)
	}
	// A thread that misses its budget is DETACHED, not joined (leak, don't crash).
	if !strings.Contains(bqBody, "xll::ForceTerminateWorker();") {
		t.Errorf("BeginQuiesce must DETACH the worker (ForceTerminateWorker) when it misses its exit budget — " +
			"§20.2 leak-don't-crash, never a blocking join")
	}
	if !strings.Contains(bqBody, "g_monitorThread.detach();") {
		t.Errorf("BeginQuiesce must DETACH the monitor thread when it misses its exit budget")
	}
	// And the miss must be recorded, so Phase 2 leaks g_phost instead of freeing
	// memory a still-running detached thread may read.
	if !strings.Contains(bqBody, "g_backgroundThreadsReaped.store(") {
		t.Errorf("BeginQuiesce must record whether BOTH threads were actually reaped")
	}
	if !strings.Contains(rdBody, "g_backgroundThreadsReaped.load(std::memory_order_acquire)") {
		t.Errorf("RunDestructiveTeardown must gate `delete g_phost` on g_backgroundThreadsReaped — a detached, " +
			"still-running thread may still touch g_phost, so leak it instead (§20.2)")
	}

	// PHASE 2 MUST NOT PARK. This is the invariant that actually fixes the crash.
	for _, banned := range []string{
		"JoinWorker",
		".join()",
		"WaitForRtdConnectDrain",
		"WaitForCommandDrain",
		"sleep_for",
		"WaitForSingleObject",
		"WaitForMultipleObjects",
	} {
		if strings.Contains(rdBody, banned) {
			t.Errorf("RunDestructiveTeardown must NOT contain %q. It runs from RtdServer::ServerTerminate, "+
				"inside Excel's own RTD/COM shutdown, at a point where Excel has been MEASURED to FreeLibrary "+
				"and unmap the XLL; anything that PARKS there returns into unmapped code (0xC0000005 against "+
				"`<proj>.xll_unloaded`). Bounded kernel calls only\n---\n%s", banned, rdBody)
		}
	}

	// The worker must expose the exit flag the non-parking join depends on.
	wh := stripCppComments(m["include/xll_worker.h"])
	for _, want := range []string{"bool WorkerExited();", "bool WaitForWorkerExit(unsigned int timeoutMs);"} {
		if !strings.Contains(wh, want) {
			t.Errorf("xll_worker.h must declare %q", want)
		}
	}
	wc := stripCppComments(m["src/xll_worker.cpp"])
	if !strings.Contains(wc, "g_workerExited.store(true, std::memory_order_release)") {
		t.Errorf("WorkerLoop must set g_workerExited as its LAST act — that flag is what turns the teardown's " +
			"join into a non-parking one")
	}
	if !strings.Contains(stripCppComments(m["include/xll_lifecycle.h"]), "bool MonitorExited();") {
		t.Errorf("xll_lifecycle.h must declare MonitorExited() (same contract as WorkerExited)")
	}
}

// TestCloseUnloadTeardownIsLoggable pins the diagnosability half of the fix: the
// destructive teardown must be VISIBLE in the log. Every other logger short-circuits
// on g_isUnloading, which RunDestructiveTeardown latches as its first act — so the
// whole teardown used to be invisible, and its absence from the log was misread as
// "Phase 2 never ran" while it was in fact running and parked in a join.
func TestCloseUnloadTeardownIsLoggable(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	logH := stripCppComments(m["include/xll_log.h"])
	if !strings.Contains(logH, "void LogTeardown(const std::string& msg);") {
		t.Fatalf("xll_log.h must declare LogTeardown — the teardown-path logger that is NOT suppressed by g_isUnloading")
	}
	logC := stripCppComments(m["src/xll_log.cpp"])
	ltIdx := strings.Index(logC, "void LogTeardown(const std::string& msg) {")
	if ltIdx < 0 {
		t.Fatalf("LogTeardown not defined in xll_log.cpp")
	}
	ltBody := logC[ltIdx:]
	if e := strings.Index(ltBody, "\n}"); e > 0 {
		ltBody = ltBody[:e]
	}
	if strings.Contains(ltBody, "g_isUnloading") {
		t.Errorf("LogTeardown must NOT check g_isUnloading — that is the whole point of it\n---\n%s", ltBody)
	}
	if !strings.Contains(ltBody, "WriteLogUnconditional(") {
		t.Errorf("LogTeardown must write via WriteLogUnconditional\n---\n%s", ltBody)
	}
	// The other levels must KEEP their suppression (that guard exists for the
	// forced-unload path, §20.2).
	for _, fn := range []string{"void LogError(", "void LogWarn(", "void LogInfo("} {
		i := strings.Index(logC, fn)
		if i < 0 {
			t.Fatalf("%s not found", fn)
		}
		b := logC[i:]
		if e := strings.Index(b, "\n}"); e > 0 {
			b = b[:e]
		}
		if !strings.Contains(b, "g_isUnloading") {
			t.Errorf("%s must keep its g_isUnloading suppression (§20.2 forced-unload path)\n---\n%s", fn, b)
		}
	}

	// And the teardown must actually USE it, or the log goes dark again.
	lc := stripCppComments(m["src/xll_lifecycle.cpp"])
	rdIdx := strings.Index(lc, "void xll::RunDestructiveTeardown()")
	if rdIdx < 0 {
		t.Fatalf("RunDestructiveTeardown not found")
	}
	rdBody := lc[rdIdx:]
	if !strings.Contains(rdBody, "LogTeardown(") {
		t.Errorf("RunDestructiveTeardown must log via LogTeardown, not LogInfo/LogWarn (which it has already silenced)\n---\n%s", rdBody)
	}
	if strings.Contains(rdBody, "LogInfo(") || strings.Contains(rdBody, "LogWarn(") {
		t.Errorf("RunDestructiveTeardown must not use LogInfo/LogWarn — it latches g_isUnloading first, so those " +
			"calls are guaranteed no-ops and produce a silently-empty teardown log")
	}
}
