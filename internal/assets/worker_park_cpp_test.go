package assets

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestWorkerLoopParksAndIsWakeable pins the 2026-08-03 change that stopped
// xll::WorkerLoop burning a core for the life of the add-in.
//
// The loop now blocks in shm's DirectHost::WaitForGuestCall instead of spinning.
// Two properties make that safe, and BOTH are teardown properties, not
// performance ones -- getting either wrong turns a CPU saving into the
// use-after-unload crash class this repo spent 2026-07-29/30 removing:
//
//  1. StopWorker() must SIGNAL the parked worker, not merely clear the flag. A
//     store to g_workerRunning is invisible to a thread already blocked in the
//     OS wait, so without the wake the Phase 1 join waits the park out.
//
//  2. The park quantum must be STRICTLY LESS than the thread-reap budget. If it
//     were not, a missed wake would outlive Phase 1's bounded reap, which then
//     DETACHES the thread -- while it is still executing inside the XLL image --
//     and on the add-in-disable path that forces the module pin, leaving the XLL
//     mapped for the rest of the session.
//
// Neither cover is sufficient alone, which is why both are asserted. The numbers
// are read out of the sources rather than hard-coded here, so bumping either
// constant cannot silently invert the relationship.
func TestWorkerLoopParksAndIsWakeable(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	worker, ok := m["src/xll_worker.cpp"]
	if !ok {
		t.Fatalf("embedded src/xll_worker.cpp not found in assets")
	}
	lifecycle, ok := m["src/xll_lifecycle.cpp"]
	if !ok {
		t.Fatalf("embedded src/xll_lifecycle.cpp not found in assets")
	}
	workerCode := stripCppCommentsAsset(worker)

	// --- the loop actually parks -------------------------------------------
	if !strings.Contains(workerCode, "g_host.WaitForGuestCall(kWorkerParkMs)") {
		t.Errorf("WorkerLoop must park in shm's WaitForGuestCall rather than spinning. " +
			"NOTE: a plain sleep is NOT an acceptable substitute -- hostState on the guest " +
			"slots is published only from inside shm's wait, so without calling it the Go " +
			"sender's doorbell never fires and any hand-rolled wait expires on its own " +
			"timeout on every single call.")
	}

	// --- StopWorker wakes it ------------------------------------------------
	stop := funcBody(t, workerCode, "void StopWorker(")
	if !strings.Contains(stop, "WakeGuestCallWaiter()") {
		t.Errorf("StopWorker must call g_host.WakeGuestCallWaiter(): clearing g_workerRunning "+
			"is invisible to a thread blocked in the OS wait, so Phase 1's join would wait "+
			"out the whole park quantum\n---\n%s", stop)
	}
	if !strings.Contains(stop, "g_workerRunning = false") {
		t.Errorf("StopWorker must still clear g_workerRunning -- the wake only releases the "+
			"park, the flag is what ends the loop\n---\n%s", stop)
	}
	// The wake must be guarded: StopWorker is reachable on teardown paths where the
	// host is already gone.
	if !strings.Contains(stop, "if (g_phost)") {
		t.Errorf("StopWorker's wake must be guarded on g_phost -- it runs on teardown paths "+
			"where the host is already gone, and must not be the thing that faults there\n---\n%s", stop)
	}

	// --- park quantum < reap budget ----------------------------------------
	readConst := func(src, name string) int {
		t.Helper()
		re := regexp.MustCompile(`(?:constexpr|const)\s+(?:unsigned\s+int|unsigned|uint32_t|int)\s+` +
			regexp.QuoteMeta(name) + `\s*=\s*(\d+)`)
		mm := re.FindStringSubmatch(src)
		if len(mm) < 2 {
			t.Fatalf("could not read %s out of the source", name)
		}
		v, err := strconv.Atoi(mm[1])
		if err != nil {
			t.Fatalf("%s is not a number: %v", name, err)
		}
		return v
	}
	park := readConst(workerCode, "kWorkerParkMs")
	budget := readConst(stripCppCommentsAsset(lifecycle), "kThreadReapBudgetMs")

	if park <= 0 {
		t.Errorf("kWorkerParkMs = %d; a zero/negative park is a spin loop again", park)
	}
	if park >= budget {
		t.Errorf("kWorkerParkMs (%d) must be STRICTLY LESS than kThreadReapBudgetMs (%d). "+
			"Otherwise a missed wake outlives Phase 1's bounded reap, which then DETACHES a "+
			"thread that is still executing inside the XLL image -- and on the add-in-disable "+
			"path that forces PinModuleToPreventUnmap, leaving the XLL mapped for the rest of "+
			"the session.", park, budget)
	}
}
