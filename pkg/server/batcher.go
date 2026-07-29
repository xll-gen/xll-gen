package server

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/xll-gen/xll-gen/pkg/log"
)

type AsyncBatcher struct {
	queue chan PendingAsyncResult

	// Shutdown gate (see Stop). `queue` is deliberately NEVER closed: QueueResult
	// is called from the SHM worker pool and from the generated async job
	// goroutines, and NOTHING in the generated server proves those have all
	// returned — closing the channel would turn a late result into a "send on
	// closed channel" PANIC, trading a leak for a crash. `stopped` makes
	// QueueResult drop instead, and the worker leaves on `stop`.
	stopOnce   sync.Once
	stopped    atomic.Bool
	stop       chan struct{}
	started    atomic.Bool
	workerDone chan struct{}
}

func NewAsyncBatcher() *AsyncBatcher {
	return &AsyncBatcher{
		queue:      make(chan PendingAsyncResult, 1024),
		stop:       make(chan struct{}),
		workerDone: make(chan struct{}),
	}
}

// QueueResult enqueues an async result for the batch worker. It is
// non-blocking: if the 1024-slot queue is full, the result is DROPPED and an
// error is logged (with the handle and value type for correlation) rather than
// blocking the caller.
//
// Rationale (AGENTS.md §23 / IMPROVEMENT_BACKLOG.md §2): the batch worker can
// sleep up to ~2.56s per flush in sendWithRetry (9 inter-attempt backoff sleeps)
// when the Excel host is gone.
// A blocking send here would back up onto the SHM worker pool goroutines that
// call QueueResult, wedging the entire pool behind a dead host. Dropping a
// result for a host that cannot receive it is the correct failure mode — the
// async handle will time out on the Excel side regardless.
//
// After Stop the same drop applies, for a stronger reason: the SHM segment is
// about to be (or has already been) unmapped, so a queued result could only ever
// become a send into freed memory.
func (ab *AsyncBatcher) QueueResult(handle []byte, val interface{}, valType AnyValue, errStr string) {
	if ab.stopped.Load() {
		log.Warn("AsyncBatcher stopped; dropping async result",
			"handle", handle, "valType", valType)
		return
	}
	res := PendingAsyncResult{
		Handle:  handle,
		Val:     val,
		ValType: valType,
		Err:     errStr,
	}
	select {
	case ab.queue <- res:
	default:
		log.Error("AsyncBatcher queue full; dropping async result",
			"handle", handle, "valType", valType, "queueCap", cap(ab.queue))
	}
}

// StartWorker starts the background worker that flushes the batch.
// flushFunc is called with a batch of results.
func (ab *AsyncBatcher) StartWorker(flushFunc func([]PendingAsyncResult)) {
	ab.started.Store(true)
	go func() {
		// Closed on return so Stop can wait for the IN-FLIGHT flush — the one
		// that may be inside client.SendGuestCall right now — to finish.
		defer close(ab.workerDone)

		const maxBatchSize = 256
		batch := make([]PendingAsyncResult, 0, maxBatchSize)

		for {
			// Check the gate BEFORE the blocking receive below. `select` picks
			// RANDOMLY among ready cases, so with a non-empty queue the select
			// alone could still flush one batch after Stop latched — i.e. issue a
			// guest->host send after Stop reported a completed drain and the
			// caller unmapped. This non-blocking pre-check makes the exit
			// deterministic instead of probabilistic. (It also covers a
			// StartWorker that races Stop: the worker then exits without ever
			// touching the client.)
			select {
			case <-ab.stop:
				return
			default:
			}

			var item PendingAsyncResult
			select {
			case it, ok := <-ab.queue:
				if !ok {
					return
				}
				item = it
			case <-ab.stop:
				// Whatever is still queued is abandoned, exactly as it was
				// before Stop existed (the process is exiting). Stop's only job
				// is to guarantee no flush is TOUCHING the SHM mapping when it
				// returns; delivering more results here would extend shutdown by
				// a full sendWithRetry ladder against a host that is already
				// gone.
				return
			}
			batch = append(batch, item)

		drain:
			for len(batch) < maxBatchSize {
				select {
				case nextItem, ok := <-ab.queue:
					if !ok {
						runFlush(flushFunc, batch)
						return
					}
					batch = append(batch, nextItem)
				default:
					break drain
				}
			}

			runFlush(flushFunc, batch)
			batch = batch[:0]
		}
	}()
}

// Stop closes the enqueue gate and waits until the worker goroutine has exited,
// i.e. until no flush is inside a guest->host send any more. It returns true if
// the worker drained within timeout, false if it did not.
//
// WHY THE CALLER MUST HONOR A false RETURN. shm's contract (shm/go/direct.go,
// DirectGuest.Close) is that Close must not run concurrently with an in-flight
// SendGuestCall: Close unmaps the shared region, and a send still reading or
// writing a slot buffer is then a use-after-free — which on unmapped memory is a
// `fatal error: unexpected fault address` that recover() CANNOT catch. So a
// false return means "do not unmap"; skipping the unmap costs nothing at process
// exit (the OS reclaims the mapping and the handles), while unmapping anyway
// converts a drain timeout into that fatal fault. Never promote one into the
// other.
//
// Stop is idempotent and safe to call from several goroutines; the gate is closed
// once and every caller waits on the same completion signal.
func (ab *AsyncBatcher) Stop(timeout time.Duration) bool {
	ab.stopOnce.Do(func() {
		// Order matters: latch the gate BEFORE signalling the worker, so a
		// QueueResult racing the shutdown cannot slip a result into a queue
		// nobody will ever read again.
		ab.stopped.Store(true)
		close(ab.stop)
	})

	if !ab.started.Load() {
		// No worker was ever started, so workerDone will never close and there
		// is nothing holding the mapping. (A StartWorker that lands after Stop
		// sees the closed `stop` on its first select and exits immediately.)
		return true
	}

	select {
	case <-ab.workerDone:
		return true
	case <-time.After(timeout):
		log.Warn("AsyncBatcher.Stop timed out waiting for the flush worker", "timeout", timeout)
		return false
	}
}

// runFlush invokes flushFunc with a recover() guard so a panic inside a single
// flush (e.g. a malformed value reaching the FlatBuffers builder) cannot kill
// the batcher goroutine and silently stop all future async deliveries. The
// panicking batch is dropped; the worker loop continues. See
// IMPROVEMENT_BACKLOG.md §2.
func runFlush(flushFunc func([]PendingAsyncResult), batch []PendingAsyncResult) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("AsyncBatcher flush panicked; batch dropped, worker continues",
				"panic", r, "batchSize", len(batch))
		}
	}()
	flushFunc(batch)
}
