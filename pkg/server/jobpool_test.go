package server

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestJobPool_RunsSubmittedJobs(t *testing.T) {
	p := NewJobPool(4)
	var n atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		if !p.Submit(func() { defer wg.Done(); n.Add(1) }) {
			wg.Done() // queue full is legal; do not hang the test on it
		}
	}
	wg.Wait()
	if !p.Drain(5 * time.Second) {
		t.Fatal("pool did not drain")
	}
	if n.Load() == 0 {
		t.Fatal("no job ran")
	}
}

func TestJobPool_DefaultsToNumCPU(t *testing.T) {
	// The generated server passes the configured worker count straight through,
	// and 0 is what "not configured" renders as. A pool with zero workers would
	// accept jobs into the buffer and never run one — every async UDF would hang
	// until its timeout with nothing in the log.
	for _, workers := range []int{0, -1} {
		p := NewJobPool(workers)
		done := make(chan struct{})
		if !p.Submit(func() { close(done) }) {
			t.Fatalf("workers=%d: submit rejected on an empty pool", workers)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("workers=%d: job never ran, so the pool has no workers", workers)
		}
		p.Drain(5 * time.Second)
	}
	if cap(NewJobPool(0).queue) != runtime.NumCPU() {
		t.Errorf("queue depth = %d, want NumCPU (%d)", cap(NewJobPool(0).queue), runtime.NumCPU())
	}
}

func TestJobPool_SubmitIsNonBlockingWhenFull(t *testing.T) {
	// One worker, queue depth 1. Occupy the worker, fill the buffer, and the
	// next Submit must REPORT full instead of blocking: this call happens on the
	// shm dispatch thread, so blocking would stall RTD and teardown traffic too.
	p := NewJobPool(1)
	release := make(chan struct{})
	started := make(chan struct{})
	if !p.Submit(func() { close(started); <-release }) {
		t.Fatal("first submit rejected")
	}
	<-started
	if !p.Submit(func() {}) {
		t.Fatal("second submit (into the buffer) rejected")
	}

	done := make(chan bool, 1)
	go func() { done <- p.Submit(func() {}) }()
	select {
	case accepted := <-done:
		if accepted {
			t.Error("submit into a full pool reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Submit BLOCKED on a full pool; the dispatch thread would stall behind it")
	}

	close(release)
	p.Drain(5 * time.Second)
}

func TestJobPool_PanicInOneJobDoesNotKillTheWorker(t *testing.T) {
	// The pool is fixed-size, so a worker lost to a user panic is capacity gone
	// for the life of the process.
	p := NewJobPool(1)
	if !p.Submit(func() { panic("user handler blew up") }) {
		t.Fatal("submit rejected")
	}
	ran := make(chan struct{})
	deadline := time.After(5 * time.Second)
	for {
		if p.Submit(func() { close(ran) }) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("could not submit after a panicking job — the worker died")
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not survive the panic")
	}
	p.Drain(5 * time.Second)
}

func TestJobPool_DrainWaitsForTheInFlightJob(t *testing.T) {
	p := NewJobPool(2)
	started := make(chan struct{})
	var finished atomic.Bool
	if !p.Submit(func() {
		close(started)
		time.Sleep(150 * time.Millisecond)
		finished.Store(true)
	}) {
		t.Fatal("submit rejected")
	}
	<-started

	if !p.Drain(5 * time.Second) {
		t.Fatal("Drain reported a timeout for a job that finishes well inside the budget")
	}
	if !finished.Load() {
		t.Error("Drain returned while a handler was still running; teardown would then " +
			"unmap the SHM segment under it")
	}
}

func TestJobPool_DrainReportsFalseWhenAJobHangs(t *testing.T) {
	p := NewJobPool(1)
	release := make(chan struct{})
	started := make(chan struct{})
	if !p.Submit(func() { close(started); <-release }) {
		t.Fatal("submit rejected")
	}
	<-started

	if p.Drain(50 * time.Millisecond) {
		t.Error("Drain reported success while a job was still running; that is the signal " +
			"teardown uses to decide NOT to unmap")
	}
	close(release)
}

func TestJobPool_DrainIsIdempotent(t *testing.T) {
	// Teardown has more than one trigger; a second Drain must not panic on a
	// double close.
	p := NewJobPool(2)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); p.Drain(5 * time.Second) }()
	}
	wg.Wait()
}

func TestJobPool_SubmitAfterDrainIsRejectedNotFatal(t *testing.T) {
	// A send on a closed channel panics, and this one would happen on the shm
	// dispatch thread during teardown — i.e. it would take the process down at
	// exactly the wrong moment.
	p := NewJobPool(2)
	p.Drain(5 * time.Second)
	if p.Submit(func() {}) {
		t.Error("a job was accepted after the pool drained")
	}
}
