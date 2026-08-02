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
// FIX (#2b): a parent-death watcher goroutine opens the parent Excel process
// (PID = os.Getppid()) with SYNCHRONIZE and blocks on
// WaitForSingleObject(INFINITE); when the parent exits it closes the SHM client
// and exits. This is the backstop that reaps the server even when the Job reap
// is denied.
//
// WHERE THE OLD ASSERTIONS WENT (2026-08-02). The watcher body moved into
// pkg/server (server.Lifecycle.WatchParentDeath), so the Win32 details this
// test used to grep for — SYNCHRONIZE rights, OpenProcess, the INFINITE wait,
// exiting with 0 — are no longer generated text. They are now covered by code
// that actually runs:
//
//	skip when there is no parent      -> TestLifecycle_WatchParentDeath_SkipsWithoutAParent
//	skip when OpenProcess is denied   -> TestLifecycle_WatchParentDeath_SkipsWhenOpenFails
//	do not reap on a failed wait      -> TestLifecycle_WatchParentDeath_DoesNotReapWhenTheWaitFails
//	onExit runs, then Exit(0)         -> TestLifecycle_WatchParentDeath_ReapsAfterTheParentExits
//	SYNCHRONIZE / OpenProcess / INFINITE -> server.OpenParentProcess,
//	                                        server.WaitForProcessExit (compiled, single definition)
//
// Note what the old grep could NOT check and the new tests do: that onExit runs
// BEFORE the process terminates. A watcher that exited first would skip the SHM
// close entirely and reproduce the very orphan symptom it exists to fix, and
// every needle above would still have been present.
//
// What remains here is the wiring this template is still responsible for.
func TestServer_ParentDeathWatcher_Wired(t *testing.T) {
	t.Parallel()

	srv := renderTemplate(t, "server.go.tmpl", serverData(nil))

	checks := []struct {
		needle string
		why    string
	}{
		{"os.Getppid()", "the watcher must derive the parent PID from os.Getppid()"},
		{"server.OpenParentProcess", "must use the library's SYNCHRONIZE-rights OpenProcess"},
		{"server.WaitForProcessExit", "must use the library's blocking parent wait"},
		{"lifecycle.WatchParentDeath(", "the watcher must run through the lifecycle"},
		{"func() { shutdownAndClose(client) }", "onExit must close the SHM client via the drain, " +
			"not leave it to process exit — that is the orphan symptom"},
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
