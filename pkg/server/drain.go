package server

import (
	"sync"
	"time"
)

// WaitGroupTimeout waits for wg, but no longer than d. It reports whether the
// group finished; false means at least one member is still running.
//
// Teardown uses this to bound how long it will wait for a set of goroutines
// before giving up on them. A caller that gets false must NOT then proceed to
// tear down anything those goroutines still touch -- the point of the bound is
// to avoid an indefinite park, not to license a use-after-free. The generated
// server applies exactly that rule: a failed job-worker drain makes
// shutdownAndClose skip client.Close() and leave the SHM mapping to the OS.
//
// This lives here rather than in server.go.tmpl deliberately. It has no
// template variables in it, so as a template it was pure generated-code weight
// with no test of its own; as ordinary package code it is compiled once and
// covered by the test beside it.
func WaitGroupTimeout(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}
