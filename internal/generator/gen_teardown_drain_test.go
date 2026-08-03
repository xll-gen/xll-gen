package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// teardownCfg builds an RTD-enabled config with a sync and an async function, so
// every teardown-relevant piece of server.go is rendered.
func teardownCfg() *config.Config {
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "Sum", Return: "int", Args: []config.Arg{{Name: "a", Type: "int"}}},
			{Name: "SlowSum", Mode: "async", Async: true, Return: "int", Args: []config.Arg{{Name: "a", Type: "int"}}},
			{Name: "Tick", Mode: "rtd", Return: "string", Args: []config.Arg{{Name: "sym", Type: "string"}}},
		},
		Rtd: config.RtdConfig{
			Enabled:     true,
			ProgID:      "TestProj.Rtd",
			Clsid:       "{11111111-2222-3333-4444-555555555555}",
			Description: "t",
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}
	*cfg.Server.Launch.Enabled = true
	return cfg
}

// TestGenServer_TeardownDrainsBeforeUnmapping pins the fix for the in-flight-send
// vs client.Close() use-after-free (HIGH, 2026-07-29).
//
// THE DEFECT. Serve did `defer client.Close()` and the parent-death watcher's
// onExit called `client.Close()` directly. shm documents the contract it breaks
// (shm/go/direct.go, DirectGuest.Close): "Close must not run concurrently with an
// in-flight SendGuestCall ... unmapping the region while such a call still
// reads/writes a slot buffer is a use-after-free." Close's wg.Wait drains only the
// worker goroutines Start launched; the async batch flusher and every RTD pusher
// (including goroutines a USER handler spawned, which live until their topic
// disconnects — and Excel dying disconnects nothing) are caller-side and
// untracked. The `closing` flag is only consulted on the slot-acquire FAILURE
// path, so a send starting after Close returned still claims a slot in an unmapped
// region: deterministic, not a race. The resulting fault is a
// `fatal error: unexpected fault address`, which recover() cannot catch, so every
// Excel exit with a live RTD stream ended in a full goroutine dump.
func TestGenServer_TeardownDrainsBeforeUnmapping(t *testing.T) {
	t.Parallel()
	rendered := renderTemplate(t, "server.go.tmpl", serverDataFor(teardownCfg()))
	assertParses(t, "server.go", rendered)

	// WHERE THE OLD ASSERTIONS WENT (2026-08-02). The teardown body moved out of
	// the template into pkg/server (server.Lifecycle), so the invariants this
	// test used to check by substring are now checked by EXECUTING the code:
	//
	//   once-guarded                -> TestLifecycle_IsIdempotent
	//   signal before draining      -> TestLifecycle_SignalsBeforeDraining
	//   budgets, and rtd > async    -> TestLifecycle_UsesTheDocumentedBudgets
	//   a failed drain skips Close  -> TestLifecycle_AnyFailedDrainVetoesTheUnmap
	//                                  (every failing combination, not one string)
	//
	// That is a strengthening, not a loss: those were greps over generated text
	// and are now behavioral. What CANNOT move is whether a given project is
	// wired to the lifecycle at all -- that is what remains here.
	//
	// The negative assertions run on CODE only: the template documents the
	// rejected shapes in prose, so a naive substring search would false-positive
	// on the comments explaining the fix.
	srv := stripCppComments(rendered)

	// 1. The unguarded Close call sites are GONE. Both of them.
	if strings.Contains(srv, "defer client.Close()") {
		t.Errorf("server.go still does `defer client.Close()`; that unmaps the segment " +
			"underneath in-flight RTD/async sends")
	}
	if strings.Contains(srv, "func() { client.Close() }") {
		t.Errorf("the parent-death watcher still closes the client directly; it must go " +
			"through the drain (that path is the MORE dangerous one - the parent died " +
			"with no handshake, so a pusher is likely mid-send)")
	}
	// No client.Close() may appear in generated code at all now: the only one in
	// the system is inside server.Lifecycle.ShutdownAndClose, behind the valve.
	if got := strings.Count(srv, "client.Close()"); got != 0 {
		t.Errorf("client.Close() appears %d times in generated code, want 0 - releasing the "+
			"mapping is the lifecycle's decision, and a second call site would bypass its "+
			"drain valve", got)
	}

	// 2. Both teardown triggers route through the same idempotent entry point.
	if !strings.Contains(srv, "defer shutdownAndClose(client)") {
		t.Errorf("Serve does not defer shutdownAndClose:\n%s", srv)
	}
	if !strings.Contains(srv, "func() { shutdownAndClose(client) })") {
		t.Errorf("the parent-death watcher's onExit does not route through shutdownAndClose")
	}
	if !strings.Contains(srv, "lifecycle.ShutdownAndClose(client)") {
		t.Errorf("shutdownAndClose does not delegate to the lifecycle:\n%s", srv)
	}

	// 3. The lifecycle is constructed with THIS project's drains. An RTD project
	//    that passed nil would silently skip the RTD drain and unmap under a live
	//    pusher -- the original defect, reintroduced through the wiring rather
	//    than through the body.
	if !strings.Contains(srv, "server.NewLifecycle(asyncBatcher, rtd.GlobalRtd)") {
		t.Errorf("an RTD-enabled project must wire BOTH drains into the lifecycle "+
			"(want server.NewLifecycle(asyncBatcher, rtd.GlobalRtd)):\n%s", srv)
	}

	// 4. The job-worker drain still reaches the valve. The drain itself moved to
	//    pkg/server (server.RunAndDrain) with the rest of the message loop, so
	//    what is left here is the wiring: THIS project's pool and THIS project's
	//    lifecycle must both be handed to it. Pass nil for either and a timed-out
	//    drain stops vetoing the unmap.
	//
	//    WHERE THE OLD ASSERTION WENT: `lifecycle.MarkJobDrainFailed()` was a
	//    grep over generated text that could not tell whether the line was
	//    reachable on the timeout path. It is now
	//    TestRunAndDrain_TimedOutJobDrainVetoesTheUnmap in pkg/server, which
	//    asserts the OUTCOME (Lifecycle.wouldUnmap() == false) on a real
	//    Lifecycle. The ordering it also could not see (Handle before Start,
	//    Wait before Drain, a fatal Start error) is
	//    TestRunAndDrain_HandleIsInstalledBeforeStart /
	//    _JobDrainWaitsForTheWorkerRoutinesToExit / _StartFailureIsFatal.
	if !strings.Contains(srv, "server.RunAndDrain(client, dispatch, jobPool, lifecycle)") {
		t.Errorf("Serve does not hand the message loop to server.RunAndDrain with this " +
			"project's pool AND lifecycle; a job-worker drain timeout would then not reach " +
			"the valve and the unmap would proceed under a handler that may still be sending")
	}
	// The loop must not be re-inlined alongside the call: a second Handle/Start
	// pair would start shm's workers twice, and a re-inlined drain would run
	// without reporting to the lifecycle (§18.6.1's do-not-re-inline rule).
	for _, gone := range []string{"client.Handle(dispatch)", "client.Start()", "client.Wait()", "jobPool.Drain("} {
		if strings.Contains(srv, gone) {
			t.Errorf("server.go still contains %q; the message loop belongs to "+
				"server.RunAndDrain and a re-inlined copy would bypass its unit tests", gone)
		}
	}

	// 5. Done() is exported so a handler's long-lived goroutine can stop pushing.
	//    The per-topic ctx is not a substitute (it is cancelled by a DISCONNECT,
	//    which never happens when Excel dies).
	if !strings.Contains(srv, "func Done() <-chan struct{} { return lifecycle.Done() }") {
		t.Errorf("server.go does not export Done():\n%s", srv)
	}
}

// TestGenServer_NonRtdProjectWiresNoRtdDrain: the nil drain is meaningful, not a
// placeholder -- a project generated without RTD has no manager to stop, and
// treating that absence as a FAILED drain would permanently disable the unmap.
func TestGenServer_NonRtdProjectWiresNoRtdDrain(t *testing.T) {
	t.Parallel()
	cfg := teardownCfg()
	cfg.Rtd.Enabled = false
	cfg.Functions = cfg.Functions[:2] // drop the rtd function
	srv := renderTemplate(t, "server.go.tmpl", serverDataFor(cfg))
	assertParses(t, "server.go", srv)

	if !strings.Contains(srv, "server.NewLifecycle(asyncBatcher, nil)") {
		t.Errorf("a non-RTD project must wire a nil rtd drain:\n%s", srv)
	}
}

// TestGenScaffold_RtdPusherWatchesShutdown: the scaffold `main.go` is the example
// every project starts from, so its streaming pusher must demonstrate the
// contract. Keyed only on ctx.Done() it outlives the server (ctx is cancelled by
// a topic DISCONNECT, which Excel dying does not produce), which is exactly the
// goroutine class the drain has to wait for.
func TestGenScaffold_RtdPusherWatchesShutdown(t *testing.T) {
	t.Parallel()
	src := templateSource(t, "main.go.tmpl")

	if !strings.Contains(src, "case <-{{.Package}}.Done():") {
		t.Errorf("the scaffold RTD pusher does not select on the generated package's Done() channel:\n%s", src)
	}
	if !strings.Contains(src, "case <-ctx.Done():") {
		t.Errorf("the scaffold RTD pusher must still honor the per-topic ctx")
	}
}
