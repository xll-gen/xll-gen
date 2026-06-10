package log

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInit_ReinitClosesPreviousFile verifies that a second Init call closes
// the file handle opened by the first, instead of leaking it
// (IMPROVEMENT_BACKLOG.md §3 — pkg/log re-init leak). The test runs in-package
// so it can observe the unexported currentFile and assert the prior handle is
// no longer usable.
func TestInit_ReinitClosesPreviousFile(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "a.log")
	p2 := filepath.Join(dir, "b.log")

	if err := Init(p1, "info"); err != nil {
		t.Fatalf("Init(p1): %v", err)
	}
	first := currentFile
	if first == nil {
		t.Fatal("currentFile nil after Init with a path")
	}

	if err := Init(p2, "info"); err != nil {
		t.Fatalf("Init(p2): %v", err)
	}
	if currentFile == first {
		t.Fatal("currentFile not swapped on re-Init")
	}

	// The first handle must be closed: a Write on a closed *os.File returns an
	// error (ErrClosed). If the handle had leaked open, this would succeed.
	if _, err := first.Write([]byte("x")); err == nil {
		t.Fatal("previous log file handle still writable after re-Init; it leaked")
	}

	// Re-Init to stdout should also close the file handle.
	if err := Init("", "info"); err != nil {
		t.Fatalf("Init(stdout): %v", err)
	}
	if currentFile != nil {
		t.Fatal("currentFile should be nil after Init to stdout")
	}

	// Restore default state for any later tests in the package.
	_ = os.Remove(p1)
	_ = os.Remove(p2)
}

// TestInit_FailedReinitKeepsPreviousLogger verifies a failing re-Init does not
// close the working file from the prior Init (we only swap after a successful
// open).
func TestInit_FailedReinitKeepsPreviousLogger(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.log")

	if err := Init(good, "info"); err != nil {
		t.Fatalf("Init(good): %v", err)
	}
	working := currentFile

	// Point at a path whose parent is a regular file, so MkdirAll/OpenFile
	// fails and Init returns an error.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	bad := filepath.Join(blocker, "sub", "x.log")

	if err := Init(bad, "info"); err == nil {
		t.Fatal("Init with an impossible path unexpectedly succeeded")
	}

	if currentFile != working {
		t.Fatal("failed re-Init swapped the file handle; working logger was torn down")
	}
	// The working handle must still be usable.
	if _, err := working.Write([]byte("still ok\n")); err != nil {
		t.Fatalf("working log handle closed by a failed re-Init: %v", err)
	}

	// Cleanup.
	_ = Init("", "info")
}
