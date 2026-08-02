package server

import (
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// fakeDrainer records the order and budget it was stopped with.
type fakeDrainer struct {
	ok      bool
	calls   int
	budget  time.Duration
	stopped func()
}

func (f *fakeDrainer) Stop(timeout time.Duration) bool {
	f.calls++
	f.budget = timeout
	if f.stopped != nil {
		f.stopped()
	}
	return f.ok
}

// ShutdownAndClose takes *shm.Client, which cannot be constructed without a real
// mapping, so these tests pass nil and assert on the DECISION (did we reach the
// close step) via the drains and the Done channel. The close step itself is
// nil-guarded in Lifecycle for exactly this reason.

func TestLifecycle_DoneClosesOnShutdown(t *testing.T) {
	l := NewLifecycle(&fakeDrainer{ok: true}, &fakeDrainer{ok: true})
	select {
	case <-l.Done():
		t.Fatal("Done() was already closed before shutdown")
	default:
	}
	l.ShutdownAndClose(nil)
	select {
	case <-l.Done():
	default:
		t.Fatal("Done() not closed after ShutdownAndClose")
	}
}

func TestLifecycle_SignalsBeforeDraining(t *testing.T) {
	// The ordering rule: user goroutines must be told to stop generating new
	// sends BEFORE we start waiting for the in-flight ones. If the drain ran
	// first, work queued during the drain would never be covered.
	var doneClosedAtDrain bool
	l := NewLifecycle(nil, nil)
	async := &fakeDrainer{ok: true}
	async.stopped = func() {
		select {
		case <-l.Done():
			doneClosedAtDrain = true
		default:
		}
	}
	l.async = async

	l.ShutdownAndClose(nil)
	if async.calls != 1 {
		t.Fatalf("async drain called %d times, want 1", async.calls)
	}
	if !doneClosedAtDrain {
		t.Error("the shutdown channel was still open when the drain ran; user goroutines " +
			"could enqueue sends the drain would then miss")
	}
}

func TestLifecycle_UsesTheDocumentedBudgets(t *testing.T) {
	async := &fakeDrainer{ok: true}
	rtdMgr := &fakeDrainer{ok: true}
	NewLifecycle(async, rtdMgr).ShutdownAndClose(nil)

	if async.budget != AsyncDrainTimeout {
		t.Errorf("async drain budget = %v, want %v", async.budget, AsyncDrainTimeout)
	}
	// RTD must get the wider budget: SendOnceGrid is 5s PER FRAME and a chunked
	// grid sends many frames in order.
	if rtdMgr.budget != RtdDrainTimeout {
		t.Errorf("rtd drain budget = %v, want %v", rtdMgr.budget, RtdDrainTimeout)
	}
	if RtdDrainTimeout <= AsyncDrainTimeout {
		t.Errorf("rtd budget %v must exceed the async budget %v", RtdDrainTimeout, AsyncDrainTimeout)
	}
}

func TestLifecycle_IsIdempotent(t *testing.T) {
	async := &fakeDrainer{ok: true}
	l := NewLifecycle(async, &fakeDrainer{ok: true})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.ShutdownAndClose(nil)
		}()
	}
	wg.Wait()

	if async.calls != 1 {
		t.Errorf("async drain ran %d times across 8 concurrent teardowns, want exactly 1", async.calls)
	}
}

// THE SAFETY VALVE. Each drain independently has to be able to veto the unmap;
// a timeout must never be promoted into a use-after-free. Every failing
// combination is checked, because the bug this guards against is one term
// silently dropping out of the condition.
func TestLifecycle_AnyFailedDrainVetoesTheUnmap(t *testing.T) {
	cases := []struct {
		name           string
		asyncOK, rtdOK bool
		jobFailed      bool
		wantUnmap      bool
	}{
		{"all drained", true, true, false, true},
		{"async timed out", false, true, false, false},
		{"rtd timed out", true, false, false, false},
		{"job workers timed out", true, true, true, false},
		{"everything timed out", false, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := NewLifecycle(&fakeDrainer{ok: tc.asyncOK}, &fakeDrainer{ok: tc.rtdOK})
			if tc.jobFailed {
				l.MarkJobDrainFailed()
			}
			if got := l.wouldUnmap(); got != tc.wantUnmap {
				t.Errorf("wouldUnmap = %v, want %v — a drain that timed out must leave the "+
					"mapping to the OS", got, tc.wantUnmap)
			}
		})
	}
}

func TestLifecycle_NilDrainerCountsAsDrained(t *testing.T) {
	// A project generated without RTD has no manager to stop; that must not be
	// mistaken for a drain failure and permanently disable the unmap.
	l := NewLifecycle(nil, nil)
	if !l.wouldUnmap() {
		t.Error("absent drains were treated as failures")
	}
}

func TestLifecycle_WatchParentDeath_SkipsWithoutAParent(t *testing.T) {
	l := NewLifecycle(nil, nil)
	exited := false
	l.Exit = func(int) { exited = true }

	opened := false
	l.WatchParentDeath(0,
		func(int) (windows.Handle, error) { opened = true; return 0, nil },
		func(windows.Handle) error { return nil },
		nil)

	if opened {
		t.Error("tried to open a parent process for pid 0")
	}
	if exited {
		t.Error("reaped the server despite having no parent handle; the Job reap is the " +
			"primary mechanism in that case and must be left alone")
	}
}

func TestLifecycle_WatchParentDeath_SkipsWhenOpenFails(t *testing.T) {
	l := NewLifecycle(nil, nil)
	exited := false
	l.Exit = func(int) { exited = true }
	onExitRan := false

	l.WatchParentDeath(1234,
		func(int) (windows.Handle, error) { return 0, errors.New("access denied") },
		func(windows.Handle) error { t.Fatal("waited on a handle that was never opened"); return nil },
		func() { onExitRan = true })

	if exited || onExitRan {
		t.Error("a denied OpenProcess must skip the watcher, not tear the server down")
	}
}

func TestLifecycle_WatchParentDeath_DoesNotReapWhenTheWaitFails(t *testing.T) {
	l := NewLifecycle(nil, nil)
	exited := false
	l.Exit = func(int) { exited = true }
	onExitRan := false

	l.WatchParentDeath(1234,
		func(int) (windows.Handle, error) { return windows.Handle(0), nil },
		func(windows.Handle) error { return errors.New("wait failed") },
		func() { onExitRan = true })

	if onExitRan || exited {
		t.Error("a failed wait is not evidence the parent died; reaping there would kill a " +
			"server whose Excel is still running")
	}
}

func TestLifecycle_WatchParentDeath_ReapsAfterTheParentExits(t *testing.T) {
	l := NewLifecycle(nil, nil)
	exitCode := -1
	l.Exit = func(c int) { exitCode = c }

	order := []string{}
	l.WatchParentDeath(1234,
		func(int) (windows.Handle, error) { return windows.Handle(0), nil },
		func(windows.Handle) error { order = append(order, "wait"); return nil },
		func() { order = append(order, "onExit") })

	if exitCode != 0 {
		t.Errorf("Exit(%d), want Exit(0)", exitCode)
	}
	// The clean shutdown must run BEFORE the process terminates, or the SHM
	// client is never closed and the orphan symptom returns.
	want := []string{"wait", "onExit"}
	if len(order) != 2 || order[0] != want[0] || order[1] != want[1] {
		t.Errorf("call order = %v, want %v (onExit must run before Exit)", order, want)
	}
}
