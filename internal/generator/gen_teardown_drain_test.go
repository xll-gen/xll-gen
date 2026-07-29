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

	// The negative assertions run on CODE only: shutdownAndClose documents the
	// rejected shapes in prose ("NOT `defer client.Close()`"), so a naive
	// substring search would false-positive on the comments explaining the fix.
	// stripCppComments handles // line comments, which is all Go needs here.
	srv := stripCppComments(rendered)

	// 1. The unguarded Close call sites are GONE. Both of them.
	if strings.Contains(srv, "defer client.Close()") {
		t.Errorf("server.go still does `defer client.Close()`; that unmaps the segment " +
			"underneath in-flight RTD/async sends")
	}
	if strings.Contains(srv, "func() { client.Close() }") {
		t.Errorf("the parent-death watcher still closes the client directly; it must go " +
			"through the drain (that path is the MORE dangerous one — the parent died " +
			"with no handshake, so a pusher is likely mid-send)")
	}

	// 2. Exactly ONE client.Close() remains, inside shutdownAndClose.
	if got := strings.Count(srv, "client.Close()"); got != 1 {
		t.Errorf("client.Close() appears %d times, want exactly 1 (inside shutdownAndClose)", got)
	}

	// 3. Both teardown triggers route through the same idempotent function.
	if !strings.Contains(srv, "defer shutdownAndClose(client)") {
		t.Errorf("Serve does not defer shutdownAndClose:\n%s", srv)
	}
	if !strings.Contains(srv, "func() { shutdownAndClose(client) })") {
		t.Errorf("the parent-death watcher's onExit does not route through shutdownAndClose")
	}
	if !strings.Contains(srv, "shutdownOnce.Do(func() {") {
		t.Errorf("shutdownAndClose is not once-guarded; both triggers can fire")
	}

	// 4. ORDERING inside shutdownAndClose: signal user goroutines, drain async,
	//    drain rtd, and only then unmap.
	body := srv[strings.Index(srv, "func shutdownAndClose("):]
	body = body[:strings.Index(body, "\n}")]
	iSignal := strings.Index(body, "close(shutdownCh)")
	iAsync := strings.Index(body, "asyncBatcher.Stop(")
	iRtd := strings.Index(body, "rtd.GlobalRtd.Stop(")
	iClose := strings.Index(body, "client.Close()")
	for _, s := range []struct {
		name string
		idx  int
	}{{"close(shutdownCh)", iSignal}, {"asyncBatcher.Stop", iAsync}, {"rtd.GlobalRtd.Stop", iRtd}, {"client.Close", iClose}} {
		if s.idx < 0 {
			t.Fatalf("shutdownAndClose is missing %s:\n%s", s.name, body)
		}
	}
	if !(iSignal < iAsync && iAsync < iRtd && iRtd < iClose) {
		t.Errorf("shutdownAndClose ordering wrong (signal=%d async=%d rtd=%d close=%d); the "+
			"unmap must come LAST:\n%s", iSignal, iAsync, iRtd, iClose, body)
	}

	// 5. THE SAFETY VALVE. A drain timeout must SKIP the unmap, never proceed to
	//    it: skipping costs nothing at process exit (the OS reclaims the mapping),
	//    while unmapping under a live sender is the fatal fault. Assert there is a
	//    `return` between the drain check and client.Close().
	iGuard := strings.Index(body, "if !asyncDrained || !rtdDrained {")
	if iGuard < 0 {
		t.Fatalf("shutdownAndClose does not inspect both drain results:\n%s", body)
	}
	between := body[iGuard:iClose]
	if !strings.Contains(between, "return") {
		t.Errorf("a failed drain does not skip client.Close(); a drain timeout must never be "+
			"promoted into a use-after-free:\n%s", body)
	}

	// 6. Done() is exported so a handler's long-lived goroutine can stop pushing.
	//    The per-topic ctx is not a substitute (it is cancelled by a DISCONNECT,
	//    which never happens when Excel dies).
	if !strings.Contains(srv, "func Done() <-chan struct{} { return shutdownCh }") {
		t.Errorf("server.go does not export Done():\n%s", srv)
	}
}

// TestGenServer_DrainBudgetsAreDeclared: the budgets must be explicit constants,
// not literals buried in the call, so they can be reviewed against the send
// timeouts they have to cover (sendWithRetry ~2.56s of backoff; SendOnceGrid 5s
// PER FRAME for a chunked grid).
func TestGenServer_DrainBudgetsAreDeclared(t *testing.T) {
	t.Parallel()
	srv := renderTemplate(t, "server.go.tmpl", serverDataFor(teardownCfg()))

	for _, want := range []string{
		"asyncDrainTimeout = 5 * time.Second",
		"rtdDrainTimeout   = 10 * time.Second",
		"asyncBatcher.Stop(asyncDrainTimeout)",
		"rtd.GlobalRtd.Stop(rtdDrainTimeout)",
	} {
		if !strings.Contains(srv, want) {
			t.Errorf("server.go is missing %q:\n%s", want, srv)
		}
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
