package server

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xll-gen/shm/go"
	"github.com/xll-gen/xll-gen/pkg/log"
	"golang.org/x/sys/windows"
)

// Teardown drain budgets. Each is an upper bound on how long shutdown may wait
// for guest->host sends that are ALREADY in flight; with nothing in flight every
// drain returns immediately, so these do not add latency to a normal exit.
//
//   - async: sendWithRetry's ladder is ~2.56s of backoff plus the sends
//     themselves, so 5s covers one flush that is riding out a dead host.
//   - rtd: an RtdUpdate is capped at 1s, but SendOnceGrid is 5s PER FRAME and a
//     chunked grid sends many frames in order, so it needs the wider budget.
//   - job: the async worker pool. Its budget only has to cover ONE user handler
//     that is already running plus whatever it is sending; the async/rtd drains
//     cover the sends themselves.
const (
	AsyncDrainTimeout = 5 * time.Second
	RtdDrainTimeout   = 10 * time.Second
	JobDrainTimeout   = 5 * time.Second
)

// Drainer is the one thing Lifecycle needs from a guest->host send path: stop
// accepting new work and report whether the in-flight work finished inside the
// budget. Both *AsyncBatcher and *rtd.RtdManager already have this shape, and
// taking an interface keeps this file out of the pkg/server -> pkg/rtd
// direction of that dependency.
type Drainer interface {
	Stop(timeout time.Duration) bool
}

// Lifecycle owns the generated server's teardown. It used to live in
// server.go.tmpl, where it had no unit tests of its own (only golden-string
// greps) and was re-emitted into every generated project.
//
// WHY THE ORDER IS LOAD-BEARING. shm's contract is explicit (shm/go/direct.go,
// DirectGuest.Close): "Close must not run concurrently with an in-flight
// SendGuestCall ... unmapping the region while such a call still reads/writes a
// slot buffer is a use-after-free." Close drains only the workers IT started —
// caller-side senders are untracked. Ours are the async batch flusher, every RTD
// pusher (including goroutines the USER's handler spawned, which live until
// their topic disconnects, i.e. not at all when Excel simply dies), and the job
// worker pool. A bare `defer client.Close()` therefore unmapped underneath them
// on the ORDINARY exit path, and a fault on unmapped memory is a `fatal error:
// unexpected fault address` that recover() cannot catch.
type Lifecycle struct {
	ch   chan struct{}
	once sync.Once

	async Drainer
	rtd   Drainer

	// Set when the caller's job-worker drain timed out, i.e. a user handler may
	// still be running and still touching the SHM client. Atomic because the
	// parent-death watcher can run ShutdownAndClose from another goroutine.
	jobDrainFailed atomic.Bool

	// Injected for tests; production behavior is identical to os.Exit.
	Exit func(int)
}

// NewLifecycle wires the teardown to the two batcher-side drains. Either may be
// nil, which is treated as "already drained" — a project generated without RTD
// has no RTD manager to stop.
func NewLifecycle(async, rtdMgr Drainer) *Lifecycle {
	return &Lifecycle{
		ch:    make(chan struct{}),
		async: async,
		rtd:   rtdMgr,
		Exit:  os.Exit,
	}
}

// Done returns a channel closed when the server starts shutting down — either
// because the XLL host signalled shutdown or because the parent Excel process
// exited.
//
// A long-lived goroutine that a handler spawned should select on it. The
// per-topic ctx passed to an RTD handler is NOT a substitute: that ctx is
// cancelled by a topic DISCONNECT, which does not happen when Excel dies or
// crashes, so a streaming pusher keyed only on ctx.Done() outlives the server.
func (l *Lifecycle) Done() <-chan struct{} { return l.ch }

// MarkJobDrainFailed records that the caller's job-worker pool did not finish
// inside its budget, so ShutdownAndClose must not unmap.
func (l *Lifecycle) MarkJobDrainFailed() { l.jobDrainFailed.Store(true) }

// ShutdownAndClose closes the guest->host send paths and only THEN releases the
// SHM mapping. It is idempotent — every teardown trigger calls it.
//
// If ANY drain times out it deliberately SKIPS client.Close(). Skipping is free:
// the process is on its way out and the OS reclaims the mapping, the section
// handle and the events. Unmapping anyway would promote a drain timeout into
// exactly the fatal fault this function exists to remove, so the safety valve is
// not optional.
func (l *Lifecycle) ShutdownAndClose(client *shm.Client) {
	l.once.Do(func() {
		// Signal user goroutines first so they stop generating new sends while
		// the drains below wait for the in-flight ones.
		close(l.ch)

		if !l.wouldUnmap() {
			return
		}
		if client != nil {
			client.Close()
		}
	})
}

// wouldUnmap runs every drain and reports whether releasing the mapping is
// safe. It is a separate method so the decision — the safety valve — can be
// unit-tested: constructing a real *shm.Client needs a live mapping, so a test
// cannot observe Close() directly.
func (l *Lifecycle) wouldUnmap() bool {
	asyncDrained := drain(l.async, AsyncDrainTimeout)
	rtdDrained := drain(l.rtd, RtdDrainTimeout)
	jobsDrained := !l.jobDrainFailed.Load()

	if !asyncDrained || !rtdDrained || !jobsDrained {
		log.Warn("Shutdown drain incomplete; leaving the SHM mapping to the OS instead of unmapping under a live sender",
			"asyncDrained", asyncDrained, "rtdDrained", rtdDrained, "jobsDrained", jobsDrained)
		return false
	}
	return true
}

func drain(d Drainer, timeout time.Duration) bool {
	if d == nil {
		return true
	}
	return d.Stop(timeout)
}

// WatchParentDeath reaps THIS server when the parent Excel process exits, even
// when the C++ side's Job-object KILL_ON_JOB_CLOSE reap is denied (locked-down
// environments where AssignProcessToJobObject fails — see xll_launch.cpp #2a).
// It directly fixes the orphaned-server symptom: an orphaned server keeps the
// inherited _go.log handle open, leaving the file undeletable while NO Excel
// process exists.
//
// It opens the parent process with SYNCHRONIZE rights and blocks until it exits,
// then runs onExit (the clean shutdown) and terminates. Failure modes
// (parentPID == 0, OpenProcess denied) are handled gracefully: the watcher is
// skipped with a warning and the Job reap remains the primary mechanism.
//
// openParent / waitOne are parameters rather than direct calls so the logic is
// testable without a real parent process.
func (l *Lifecycle) WatchParentDeath(parentPID int, openParent func(pid int) (windows.Handle, error), waitOne func(windows.Handle) error, onExit func()) {
	if parentPID == 0 {
		log.Warn("Parent-death watcher skipped: getppid returned 0 (no parent handle)")
		return
	}
	h, err := openParent(parentPID)
	if err != nil {
		log.Warn("Parent-death watcher skipped: could not open parent process", "ppid", parentPID, "error", err)
		return
	}
	defer windows.CloseHandle(h)

	log.Info("Parent-death watcher armed", "ppid", parentPID)
	if err := waitOne(h); err != nil {
		log.Warn("Parent-death watcher wait failed; not reaping", "error", err)
		return
	}
	log.Info("Parent (Excel) exited — shutting down server cleanly", "ppid", parentPID)
	if onExit != nil {
		onExit()
	}
	l.Exit(0)
}

// OpenParentProcess is the production openParent for WatchParentDeath.
func OpenParentProcess(pid int) (windows.Handle, error) {
	return windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
}

// WaitForProcessExit is the production waitOne for WatchParentDeath.
func WaitForProcessExit(h windows.Handle) error {
	ev, err := windows.WaitForSingleObject(h, windows.INFINITE)
	if err != nil {
		return err
	}
	if ev == uint32(windows.WAIT_FAILED) {
		return fmt.Errorf("WaitForSingleObject returned WAIT_FAILED")
	}
	return nil
}
