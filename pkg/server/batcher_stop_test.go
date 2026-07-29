package server

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xll-gen/types/go/protocol"
)

// The tests below pin AsyncBatcher.Stop, the async half of the teardown drain
// (HIGH, 2026-07-29).
//
// WHY IT EXISTS. The generated server used to `defer client.Close()`.
// shm/go/direct.go documents that Close "must not run concurrently with an
// in-flight SendGuestCall ... unmapping the region while such a call still
// reads/writes a slot buffer is a use-after-free", and Close drains only the
// worker goroutines IT started — the async flush worker is the caller's, so it
// was never drained. A fault on unmapped memory is a `fatal error: unexpected
// fault address` that recover() cannot catch, so the observable symptom was a
// goroutine dump plus a non-zero exit on Excel shutdown.

// TestAsyncBatcher_StopWaitsForTheInFlightFlush is the core guarantee: when Stop
// returns true, no flush is still inside a guest->host send. If this does not
// hold, the caller unmaps the segment underneath one.
func TestAsyncBatcher_StopWaitsForTheInFlightFlush(t *testing.T) {
	ab := NewAsyncBatcher()

	inFlush := make(chan struct{})
	release := make(chan struct{})
	var flushReturned atomic.Bool

	ab.StartWorker(func(batch []PendingAsyncResult) {
		close(inFlush)
		<-release // stand in for a blocking client.SendGuestCall
		flushReturned.Store(true)
	})

	ab.QueueResult([]byte("h"), int32(1), protocol.AnyValueInt, "")
	select {
	case <-inFlush:
	case <-time.After(5 * time.Second):
		t.Fatal("flush never started")
	}

	stopped := make(chan bool, 1)
	go func() { stopped <- ab.Stop(5 * time.Second) }()

	// Stop must NOT report a completed drain while the flush is still running.
	select {
	case ok := <-stopped:
		t.Fatalf("Stop returned (%v) while a flush was still in a send; the caller would "+
			"now unmap the SHM segment underneath it", ok)
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	select {
	case ok := <-stopped:
		if !ok {
			t.Fatal("Stop reported a timeout even though the flush completed well inside the budget")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after the flush completed")
	}
	if !flushReturned.Load() {
		t.Fatal("Stop returned true before the flush body finished")
	}
}

// TestAsyncBatcher_StopRejectsLaterResults: after Stop, QueueResult must drop
// rather than enqueue. Anything queued past that point could only ever become a
// send into a segment the caller is about to unmap — and it must not PANIC
// either, which is why the queue channel is never closed (QueueResult is called
// from the SHM worker pool and the generated async job goroutines, neither of
// which the server can prove has returned).
func TestAsyncBatcher_StopRejectsLaterResults(t *testing.T) {
	ab := NewAsyncBatcher()

	var flushes atomic.Int32
	ab.StartWorker(func(batch []PendingAsyncResult) { flushes.Add(1) })

	if ok := ab.Stop(5 * time.Second); !ok {
		t.Fatal("Stop timed out with an idle worker")
	}

	// Must not panic, must not enqueue.
	for i := 0; i < 100; i++ {
		ab.QueueResult([]byte("late"), int32(i), protocol.AnyValueInt, "")
	}
	if got := len(ab.queue); got != 0 {
		t.Fatalf("%d results were enqueued after Stop; they can only become sends into an unmapped segment", got)
	}

	time.Sleep(50 * time.Millisecond)
	if got := flushes.Load(); got != 0 {
		t.Fatalf("worker flushed %d batches after Stop", got)
	}
}

// TestAsyncBatcher_StopIsIdempotentAndConcurrencySafe: both teardown triggers in
// the generated server (Serve's deferred shutdown and the parent-death watcher)
// can call it, possibly at the same time.
func TestAsyncBatcher_StopIsIdempotentAndConcurrencySafe(t *testing.T) {
	ab := NewAsyncBatcher()
	ab.StartWorker(func(batch []PendingAsyncResult) {})

	var wg sync.WaitGroup
	results := make([]bool, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = ab.Stop(5 * time.Second)
		}(i)
	}
	wg.Wait()

	for i, ok := range results {
		if !ok {
			t.Errorf("concurrent Stop caller %d reported a timeout", i)
		}
	}
}

// TestAsyncBatcher_StopWithoutWorker: a batcher whose worker was never started
// holds no mapping, so Stop must report success instead of blocking for the full
// budget (which would push the caller onto the skip-the-unmap path for nothing).
func TestAsyncBatcher_StopWithoutWorker(t *testing.T) {
	ab := NewAsyncBatcher()
	start := time.Now()
	if ok := ab.Stop(2 * time.Second); !ok {
		t.Fatal("Stop on a never-started batcher must report a completed drain")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Stop on a never-started batcher blocked for %v", elapsed)
	}
}

// TestAsyncBatcher_StopReportsFalseWhenTheFlushHangs pins the SAFETY VALVE
// contract. A false return is the signal the caller uses to SKIP client.Close():
// skipping an unmap is free at process exit, while unmapping under a live sender
// is the fatal fault. Stop must therefore be able to say "not drained" instead of
// blocking forever or lying.
func TestAsyncBatcher_StopReportsFalseWhenTheFlushHangs(t *testing.T) {
	ab := NewAsyncBatcher()

	release := make(chan struct{})
	defer close(release)
	inFlush := make(chan struct{})

	ab.StartWorker(func(batch []PendingAsyncResult) {
		close(inFlush)
		<-release
	})

	ab.QueueResult([]byte("h"), int32(1), protocol.AnyValueInt, "")
	select {
	case <-inFlush:
	case <-time.After(5 * time.Second):
		t.Fatal("flush never started")
	}

	if ok := ab.Stop(100 * time.Millisecond); ok {
		t.Fatal("Stop claimed a completed drain while the flush was still in a send")
	}
}

// TestAsyncBatcher_StopWithANonEmptyQueueNeverFlushes pins that the worker's exit
// is DETERMINISTIC, not probabilistic. `select` picks randomly among ready cases,
// so a worker that only selected on {queue, stop} could still pick a queued item
// and flush it — issuing a guest->host send after Stop reported a completed drain
// and the caller unmapped the segment. The non-blocking gate check at the top of
// the loop is what removes that. This also covers a StartWorker that lands AFTER
// Stop (the generated server arms the parent-death watcher before StartWorker, so
// the ordering is reachable).
func TestAsyncBatcher_StopWithANonEmptyQueueNeverFlushes(t *testing.T) {
	for i := 0; i < 200; i++ { // repeat: the pre-fix bug is a coin flip per iteration
		ab := NewAsyncBatcher()

		// Fill the queue while nothing is draining it.
		for j := 0; j < 32; j++ {
			ab.QueueResult([]byte("h"), int32(j), protocol.AnyValueInt, "")
		}
		if len(ab.queue) == 0 {
			t.Fatal("the queue is empty; the test would be vacuous")
		}

		if ok := ab.Stop(5 * time.Second); !ok {
			t.Fatal("Stop timed out with no worker running")
		}

		var flushes atomic.Int32
		ab.StartWorker(func(batch []PendingAsyncResult) { flushes.Add(1) })

		// Give the worker a real chance to misbehave.
		time.Sleep(time.Millisecond)
		if got := flushes.Load(); got != 0 {
			t.Fatalf("iteration %d: worker flushed %d batches after Stop returned; those are sends "+
				"into a segment the caller has already unmapped", i, got)
		}
	}
}

// TestAsyncBatcher_StopDoesNotStrandAQueuedResultAsAPanic: a result racing the
// shutdown must be dropped, never sent on a closed channel. The gate is latched
// before the worker is signalled specifically so this holds.
func TestAsyncBatcher_StopDoesNotStrandAQueuedResultAsAPanic(t *testing.T) {
	ab := NewAsyncBatcher()
	ab.StartWorker(func(batch []PendingAsyncResult) {})

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				// A panic here (send on closed channel) fails the test by
				// crashing it, which is the point.
				ab.QueueResult([]byte("h"), int32(j), protocol.AnyValueInt, "")
			}
		}(i)
	}

	time.Sleep(2 * time.Millisecond)
	ab.Stop(5 * time.Second)
	wg.Wait()
}
