package server

import "github.com/xll-gen/xll-gen/pkg/log"

type AsyncBatcher struct {
	queue chan PendingAsyncResult
}

func NewAsyncBatcher() *AsyncBatcher {
	return &AsyncBatcher{
		queue: make(chan PendingAsyncResult, 1024),
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
func (ab *AsyncBatcher) QueueResult(handle []byte, val interface{}, valType AnyValue, errStr string) {
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
	go func() {
		const maxBatchSize = 256
		batch := make([]PendingAsyncResult, 0, maxBatchSize)

		for {
			item, ok := <-ab.queue
			if !ok {
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
