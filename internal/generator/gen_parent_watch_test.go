package generator

import (
	"strings"
	"testing"
)

// TestServer_ParentDeathWatcher_Wired is the structural regression for the
// orphaned-Go-server symptom (S2): after the user closes Excel, an orphaned Go
// server can keep the inherited <proj>_go.log handle open, leaving the file
// undeletable while NO Excel process exists. The root cause is that the Job
// object's KILL_ON_JOB_CLOSE reap can be DENIED in locked-down environments
// (AssignProcessToJobObject fails — see xll_launch.cpp #2a), so the server is
// never reaped.
//
// FIX (#2b): the generated server starts a parent-death watcher goroutine that
// opens the parent Excel process (PID = os.Getppid()) with SYNCHRONIZE and
// blocks on WaitForSingleObject(INFINITE); when the parent exits it closes the
// SHM client and os.Exit(0). This is the backstop that reaps the server even
// when the Job reap is denied.
//
// This test pins that the template wires the watcher. It FAILS on the parent
// commit (no watcher) and PASSES after the fix.
func TestServer_ParentDeathWatcher_Wired(t *testing.T) {
	t.Parallel()

	srv := renderTemplate(t, "server.go.tmpl", serverData(nil))

	// The watcher must use the parent PID, the SYNCHRONIZE-rights OpenProcess,
	// and the blocking wait. These are the load-bearing pieces of the backstop.
	checks := []struct {
		needle string
		why    string
	}{
		{"os.Getppid()", "watcher must derive the parent PID from os.Getppid()"},
		{"watchParentDeath", "Serve() must start the parent-death watcher"},
		{"windows.SYNCHRONIZE", "must open the parent with SYNCHRONIZE rights"},
		{"windows.OpenProcess", "must OpenProcess the parent handle"},
		{"windows.INFINITE", "must block on the parent handle with an INFINITE wait"},
		{"osExit(0)", "must terminate cleanly once the parent (Excel) exits"},
		{"golang.org/x/sys/windows", "must import golang.org/x/sys/windows for the Win32 calls"},
	}
	for _, c := range checks {
		if !strings.Contains(srv, c.needle) {
			t.Errorf("server.go.tmpl missing %q: %s\n---\n%s", c.needle, c.why, srv)
		}
	}

	// The watcher must be started in a goroutine so it never blocks Serve().
	if !strings.Contains(srv, "go watchParentDeath(") {
		t.Errorf("parent-death watcher must run in its own goroutine (go watchParentDeath(...)):\n%s", srv)
	}

	// The rendered server must still be syntactically valid Go.
	assertParses(t, "server.go", srv)
}
