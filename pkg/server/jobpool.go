package server

import (
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"github.com/xll-gen/xll-gen/pkg/log"
)

// JobPool is the generated server's bounded async worker pool: the goroutines
// that run user handler code off the shm dispatch thread.
//
// It used to be built inline in server.go.tmpl — the channel, the worker loop,
// the per-job recover, the non-blocking submit and the shutdown drain, re-emitted
// into every project and covered by nothing but golden text. Only the worker
// COUNT ever varied, and that is now a constructor argument.
//
// The drain is the part that matters. A job runs user code that can still be
// sending on the SHM client, and teardown ends in client.Close(), which unmaps
// the shared segment. Draining the pool before that is what keeps a slow handler
// from writing into an unmapped region; see Lifecycle.
type JobPool struct {
	queue chan func()
	wg    sync.WaitGroup

	closeOnce sync.Once
}

// NewJobPool starts workers goroutines. A workers value <= 0 means
// runtime.NumCPU(), which is what the generated server passes when the project
// did not configure server.workers.
//
// The queue is sized to the worker count: it is a burst absorber, not a backlog.
// A deeper queue would let Excel enqueue work faster than it can be retired and
// turn "server busy" — which the caller answers immediately — into an unbounded
// latency tail with no signal.
func NewJobPool(workers int) *JobPool {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	p := &JobPool{queue: make(chan func(), workers)}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for job := range p.queue {
				p.run(job)
			}
		}()
	}
	return p
}

// run isolates one job's panic. A user handler that panics must not take the
// worker down with it: the pool is fixed-size, so a dead worker is capacity lost
// for the life of the process, and losing all of them wedges every async UDF.
func (p *JobPool) run(job func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("Worker panic recovered", "error", r, "stack", string(debug.Stack()))
		}
	}()
	job()
}

// Submit queues a job without blocking. It reports false when the queue is full,
// and the caller MUST then answer the request itself (the generated server
// returns a "Server Busy" result and releases the timeout context).
//
// Non-blocking on purpose: this runs on the shm dispatch path, so blocking here
// would stall every other message — including the RTD and teardown traffic —
// behind a full pool.
//
// After Drain has closed the queue, Submit reports false rather than panicking
// on a send to a closed channel.
func (p *JobPool) Submit(job func()) (accepted bool) {
	defer func() {
		// Only reachable if a job is submitted concurrently with Drain. The
		// caller's busy path is the correct answer in that case too.
		if recover() != nil {
			accepted = false
		}
	}()
	select {
	case p.queue <- job:
		return true
	default:
		return false
	}
}

// Drain stops accepting work and waits up to timeout for the in-flight jobs.
// It reports whether the pool finished; false means at least one user handler is
// still running, and the caller must NOT proceed to unmap anything that handler
// can touch.
//
// Idempotent: teardown has more than one trigger.
func (p *JobPool) Drain(timeout time.Duration) bool {
	p.closeOnce.Do(func() { close(p.queue) })
	return WaitGroupTimeout(&p.wg, timeout)
}
