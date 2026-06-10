package server

import (
	"sync"
	"testing"
	"time"

	"github.com/xll-gen/types/go/protocol"
)

// TestQueueResult_DropsWhenFull verifies the non-blocking backpressure policy
// (IMPROVEMENT_BACKLOG.md §2): once the 1024-slot queue is saturated and no
// worker is draining it, QueueResult must return immediately (dropping the
// result) instead of blocking the calling SHM worker goroutine.
func TestQueueResult_DropsWhenFull(t *testing.T) {
	ab := NewAsyncBatcher()
	// Deliberately do NOT StartWorker, so nothing drains the queue.

	// Fill the queue to capacity.
	for i := 0; i < cap(ab.queue); i++ {
		ab.QueueResult([]byte("h"), int32(i), protocol.AnyValueInt, "")
	}
	if got := len(ab.queue); got != cap(ab.queue) {
		t.Fatalf("queue not full after %d sends: len=%d", cap(ab.queue), got)
	}

	// The next QueueResult must NOT block. Run it in a goroutine guarded by a
	// timeout; if it blocks, the test fails rather than hanging the suite.
	done := make(chan struct{})
	go func() {
		ab.QueueResult([]byte("overflow"), int32(-1), protocol.AnyValueInt, "")
		close(done)
	}()

	select {
	case <-done:
		// non-blocking: good. The overflow result was dropped.
	case <-time.After(2 * time.Second):
		t.Fatal("QueueResult blocked on a full queue; backpressure policy regressed")
	}

	// The dropped result must not have grown the queue beyond capacity.
	if got := len(ab.queue); got != cap(ab.queue) {
		t.Fatalf("queue length changed after a drop: len=%d, want %d", got, cap(ab.queue))
	}
}

// TestStartWorker_FlushPanicDoesNotKillWorker verifies the recover() guard
// around the flush body (IMPROVEMENT_BACKLOG.md §2): a panic in one flush must
// drop that batch but leave the worker alive to process subsequent results.
func TestStartWorker_FlushPanicDoesNotKillWorker(t *testing.T) {
	ab := NewAsyncBatcher()

	var mu sync.Mutex
	delivered := 0
	first := true
	flushed := make(chan struct{}, 16)

	ab.StartWorker(func(batch []PendingAsyncResult) {
		mu.Lock()
		panicNow := first
		first = false
		if !panicNow {
			delivered += len(batch)
		}
		mu.Unlock()
		// Signal AFTER recording, so the test can observe progress.
		flushed <- struct{}{}
		if panicNow {
			panic("simulated flush failure")
		}
	})

	// First result: triggers the panicking flush.
	ab.QueueResult([]byte("a"), int32(1), protocol.AnyValueInt, "")
	select {
	case <-flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("first flush never ran")
	}

	// Second result: must still be delivered (worker survived the panic).
	ab.QueueResult([]byte("b"), int32(2), protocol.AnyValueInt, "")
	select {
	case <-flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("worker died after a panicking flush; recover() guard regressed")
	}

	mu.Lock()
	got := delivered
	mu.Unlock()
	if got != 1 {
		t.Fatalf("delivered=%d after panic+recover, want 1", got)
	}
}
