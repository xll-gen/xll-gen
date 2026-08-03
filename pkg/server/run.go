package server

import (
	"time"

	"github.com/xll-gen/shm/go"
	"github.com/xll-gen/xll-gen/pkg/log"
)

// Dispatcher is the generated per-project message dispatch closure: the giant
// `switch uint32(mType)` in the generated server. It is the ONE thing in this
// file that is genuinely per-project, which is why it arrives as a parameter.
type Dispatcher func(req []byte, respBuf []byte, msgType shm.MsgType) (int32, shm.MsgType)

// ShmRunner is the slice of *shm.Client that RunAndDrain drives. Taking an
// interface is what makes the ORDER below testable without a live shared-memory
// segment; *shm.Client satisfies it as-is.
type ShmRunner interface {
	Handle(func(req []byte, respBuf []byte, msgType shm.MsgType) (int32, shm.MsgType))
	Start() error
	Wait()
}

// JobDrainer is the slice of *JobPool that RunAndDrain drives.
type JobDrainer interface {
	Drain(timeout time.Duration) bool
}

// JobDrainReporter is the slice of *Lifecycle that RunAndDrain needs: the
// safety valve that turns "the job pool did not finish" into "do not unmap".
type JobDrainReporter interface {
	MarkJobDrainFailed()
}

// RunAndDrain runs the generated server's message loop and then drains the async
// job pool. It used to be the tail of Serve in server.go.tmpl, where it carried
// no template variables at all and was covered by nothing but two greps.
//
// The four steps are ORDERED, and every one of the orderings is load-bearing:
//
//  1. Handle BEFORE Start. shm starts its worker routines in Start; a Start with
//     no handler installed brings the server up with NO dispatch at all, and the
//     symptom is invisible — every UDF just times out with nothing in the log.
//
//  2. Start's error is FATAL, loudly. Same altitude as the ConnectSHM failure
//     the caller already panics on. Today Handle above is unconditional so this
//     is always nil; the panic exists so that if that ever stops holding the
//     failure is a stack trace instead of a silent no-worker server.
//
//  3. Wait BEFORE Drain. Wait returns when shm's worker routines have exited,
//     which is what makes the drain safe: the only submitters are the dispatch
//     handlers those routines call, so no Submit can follow. (JobPool.Submit
//     refuses one anyway rather than panicking on a closed channel.)
//
//  4. Drain BEFORE the caller's deferred ShutdownAndClose. A job runs USER
//     handler code that can still be sending on the client, and ShutdownAndClose
//     ends in client.Close(), which UNMAPS the shared segment — the async/rtd
//     drains it performs cover the batchers, not a handler that is mid-send. So
//     RunAndDrain must return only after the pool is quiet, and when it is NOT
//     quiet it must say so: MarkJobDrainFailed is what makes ShutdownAndClose
//     skip the unmap. Dropping that one call turns a drain timeout into the
//     `fatal error: unexpected fault address` the whole valve exists to remove
//     (recover() cannot catch it).
//
// lc may be nil only in tests; the generated server always passes its lifecycle.
func RunAndDrain(client ShmRunner, dispatch Dispatcher, jobs JobDrainer, lc JobDrainReporter) {
	client.Handle(dispatch)

	if err := client.Start(); err != nil {
		log.Error("Failed to start SHM worker routines", "error", err)
		panic(err)
	}

	client.Wait()

	if jobs == nil {
		return
	}
	if !jobs.Drain(JobDrainTimeout) {
		if lc != nil {
			lc.MarkJobDrainFailed()
		}
		log.Warn("Async job workers did not finish within the drain budget; "+
			"shutdownAndClose will leave the SHM mapping to the OS rather than unmap "+
			"under a handler that may still be sending",
			"timeout", JobDrainTimeout)
	}
}
