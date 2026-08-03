package server

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xll-gen/shm/go"
)

// The tail of the generated Serve — Handle, Start, Wait, then the job drain —
// moved out of internal/templates/server.go.tmpl into server.RunAndDrain.
//
// WHERE THE OLD ASSERTIONS WENT:
//
//	cmd/regression_static_test.go
//	  "jobPool.Drain(server.JobDrainTimeout)"
//	                        -> the WIRING half stayed there as
//	                           "server.RunAndDrain(client, dispatch, jobPool, lifecycle)";
//	                           the BUDGET is now RunAndDrain's own constant and is
//	                           executed by TestRunAndDrain_UsesTheJobDrainBudget
//	  internal/generator/gen_teardown_drain_test.go
//	    "lifecycle.MarkJobDrainFailed()"
//	                        -> the wiring assertion there now checks that the
//	                           lifecycle REACHES RunAndDrain (it is the 4th
//	                           argument); that the timeout actually calls
//	                           MarkJobDrainFailed is executed by
//	                           TestRunAndDrain_TimedOutJobDrainVetoesTheUnmap
//
// Nothing in the old grep set could see any of the ORDERING below, which is the
// whole reason this behavior is worth relocating: `client.Start()` appearing in
// the file says nothing about whether Handle preceded it, and a rendered
// template containing `lifecycle.MarkJobDrainFailed()` says nothing about
// whether that line is reachable on the path that matters.

type fakeShmRunner struct {
	mu       sync.Mutex
	order    []string
	handler  func(req []byte, respBuf []byte, msgType shm.MsgType) (int32, shm.MsgType)
	startErr error

	// waitUntil, when non-nil, blocks Wait until it is closed. It models shm's
	// worker routines still running.
	waitUntil chan struct{}
}

func (f *fakeShmRunner) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, s)
}

func (f *fakeShmRunner) steps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.order))
	copy(out, f.order)
	return out
}

func (f *fakeShmRunner) Handle(h func(req []byte, respBuf []byte, msgType shm.MsgType) (int32, shm.MsgType)) {
	f.mu.Lock()
	f.handler = h
	f.mu.Unlock()
	f.record("handle")
}

func (f *fakeShmRunner) Start() error {
	f.record("start")
	return f.startErr
}

func (f *fakeShmRunner) Wait() {
	if f.waitUntil != nil {
		<-f.waitUntil
	}
	f.record("wait")
}

type fakeJobDrainer struct {
	ok      bool
	calls   int
	budget  time.Duration
	onDrain func()
}

func (f *fakeJobDrainer) Drain(timeout time.Duration) bool {
	f.calls++
	f.budget = timeout
	if f.onDrain != nil {
		f.onDrain()
	}
	return f.ok
}

// noopDispatch is a dispatch closure whose only job is to be identifiable.
func sentinelDispatch(tag *int32) Dispatcher {
	return func(req []byte, respBuf []byte, msgType shm.MsgType) (int32, shm.MsgType) {
		return *tag, shm.MsgType(7)
	}
}

// THE INSTALL ORDER. shm spins its worker routines up in Start; if Handle has
// not run by then the server comes up with no dispatch at all and every UDF
// times out with nothing in the log — a failure with no symptom to grep for.
func TestRunAndDrain_HandleIsInstalledBeforeStart(t *testing.T) {
	c := &fakeShmRunner{}
	jobs := &fakeJobDrainer{ok: true}
	tag := int32(42)

	RunAndDrain(c, sentinelDispatch(&tag), jobs, NewLifecycle(nil, nil))

	got := c.steps()
	want := []string{"handle", "start", "wait"}
	if len(got) != len(want) {
		t.Fatalf("step order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("step order = %v, want %v", got, want)
		}
	}

	// The handler installed must be the caller's dispatch, not a wrapper that
	// swallows it.
	if c.handler == nil {
		t.Fatal("no handler was installed on the client")
	}
	if n, mt := c.handler(nil, nil, 0); n != 42 || mt != shm.MsgType(7) {
		t.Errorf("installed handler returned (%d,%d); it is not the dispatch that was passed in", n, mt)
	}
}

// THE DRAIN ORDER. Draining while shm's worker routines are still calling the
// dispatch handlers would let a Submit land after the drain decided the pool was
// quiet. Wait returning is what makes "no Submit can follow" true.
func TestRunAndDrain_JobDrainWaitsForTheWorkerRoutinesToExit(t *testing.T) {
	release := make(chan struct{})
	c := &fakeShmRunner{waitUntil: release}

	drainedBeforeWait := false
	jobs := &fakeJobDrainer{ok: true}
	jobs.onDrain = func() {
		for _, s := range c.steps() {
			if s == "wait" {
				return
			}
		}
		drainedBeforeWait = true
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		RunAndDrain(c, sentinelDispatch(new(int32)), jobs, NewLifecycle(nil, nil))
	}()

	select {
	case <-done:
		t.Fatal("RunAndDrain returned while the shm worker routines were still running")
	case <-time.After(50 * time.Millisecond):
	}
	if jobs.calls != 0 {
		t.Fatalf("job pool was drained %d times before Wait returned", jobs.calls)
	}

	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunAndDrain did not return after Wait unblocked")
	}

	if drainedBeforeWait {
		t.Error("Drain ran before Wait returned")
	}
	if jobs.calls != 1 {
		t.Errorf("job pool drained %d times, want exactly 1", jobs.calls)
	}
}

// THE SAFETY VALVE, END TO END. A job that is still running may still be sending
// on the SHM client, so a timed-out drain has to disable the unmap. This asserts
// the OUTCOME on a real Lifecycle, not the presence of the call: with the
// MarkJobDrainFailed line removed, wouldUnmap goes back to true and the
// generated server unmaps under a live sender (the `fatal error: unexpected
// fault address` class that recover() cannot catch).
func TestRunAndDrain_TimedOutJobDrainVetoesTheUnmap(t *testing.T) {
	for _, tc := range []struct {
		name      string
		drainOK   bool
		wantUnmap bool
	}{
		{"job workers finished", true, true},
		{"job workers timed out", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := NewLifecycle(nil, nil)
			RunAndDrain(&fakeShmRunner{}, sentinelDispatch(new(int32)), &fakeJobDrainer{ok: tc.drainOK}, l)

			if got := l.wouldUnmap(); got != tc.wantUnmap {
				t.Errorf("after a job drain that returned %v, wouldUnmap = %v, want %v",
					tc.drainOK, got, tc.wantUnmap)
			}
		})
	}
}

// The budget is the pool's, not an arbitrary one: JobDrainTimeout only has to
// cover ONE already-running user handler, because the async/rtd drains cover the
// sends themselves.
func TestRunAndDrain_UsesTheJobDrainBudget(t *testing.T) {
	jobs := &fakeJobDrainer{ok: true}
	RunAndDrain(&fakeShmRunner{}, sentinelDispatch(new(int32)), jobs, NewLifecycle(nil, nil))
	if jobs.budget != JobDrainTimeout {
		t.Errorf("job drain budget = %v, want server.JobDrainTimeout (%v)", jobs.budget, JobDrainTimeout)
	}
}

// A Start failure is FATAL and LOUD. It is the "Handle was never called" wiring
// bug's only symptom; swallowing it produces a server with no worker goroutines
// and no diagnostic at all.
func TestRunAndDrain_StartFailureIsFatal(t *testing.T) {
	c := &fakeShmRunner{startErr: errors.New("boom")}
	jobs := &fakeJobDrainer{ok: true}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("a failed client.Start() did not panic; the server would come up with no workers")
		}
		if err, ok := r.(error); !ok || err.Error() != "boom" {
			t.Errorf("panic value = %v, want the Start error", r)
		}
		// Nothing past the failure may run.
		for _, s := range c.steps() {
			if s == "wait" {
				t.Error("Wait ran after Start failed")
			}
		}
		if jobs.calls != 0 {
			t.Error("the job pool was drained after Start failed")
		}
	}()

	RunAndDrain(c, sentinelDispatch(new(int32)), jobs, NewLifecycle(nil, nil))
}

// *shm.Client must keep satisfying ShmRunner: the interface exists only so the
// order above is testable, and a signature drift in shm would otherwise surface
// as a compile error inside a GENERATED project rather than here.
var _ ShmRunner = (*shm.Client)(nil)
