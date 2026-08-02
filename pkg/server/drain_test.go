package server

import (
	"sync"
	"testing"
	"time"
)

func TestWaitGroupTimeout_ReturnsTrueWhenTheGroupFinishes(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			time.Sleep(10 * time.Millisecond)
		}()
	}
	if !WaitGroupTimeout(&wg, 5*time.Second) {
		t.Fatal("reported a timeout for a group that finishes well inside the budget")
	}
}

func TestWaitGroupTimeout_ReturnsFalseWhileAMemberIsStillRunning(t *testing.T) {
	var wg sync.WaitGroup
	release := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-release
	}()

	start := time.Now()
	if WaitGroupTimeout(&wg, 50*time.Millisecond) {
		close(release)
		t.Fatal("reported success while a member was still running; teardown would then " +
			"unmap under a live goroutine")
	}
	// It must actually wait for the budget rather than returning immediately —
	// a version that gave up at once would make every drain look failed and
	// permanently disable the unmap.
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Errorf("returned after %v, want it to wait out the ~50ms budget", elapsed)
	}

	close(release)
	if !WaitGroupTimeout(&wg, 5*time.Second) {
		t.Fatal("did not observe the group finishing after the member was released")
	}
}

func TestWaitGroupTimeout_EmptyGroupReturnsImmediately(t *testing.T) {
	var wg sync.WaitGroup
	start := time.Now()
	if !WaitGroupTimeout(&wg, 5*time.Second) {
		t.Fatal("an empty group reported a timeout")
	}
	// Nothing in flight must not add latency to an ordinary exit.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v for an empty group; a normal shutdown must not pay the budget", elapsed)
	}
}
