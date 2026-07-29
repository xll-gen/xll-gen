package rtd

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xll-gen/shm/go"
)

// The tests below pin RtdManager.Stop, the RTD half of the teardown drain
// (HIGH, 2026-07-29).
//
// WHY IT EXISTS. RTD senders run on goroutines RtdManager does not own: the
// detached OnRtdConnect goroutine, RunOnce/RunOnceGrid, and — the common case —
// a STREAMING handler's own pushing goroutine, which lives until its topic
// disconnects (and Excel dying never disconnects a topic). None of those are
// tracked by shm's DirectGuest.wg, so the generated server's former
// `defer client.Close()` unmapped the shared segment underneath them. shm calls
// that a use-after-free (shm/go/direct.go, DirectGuest.Close), and a fault on
// unmapped memory is a `fatal error: unexpected fault address` that recover()
// cannot catch.

// gateClient is an rtdClient/chunkSender that counts sends and can block, so a
// test can hold one send "inside the mapping" across a Stop.
type gateClient struct {
	mu       sync.Mutex
	sends    int
	started  chan struct{}
	once     sync.Once
	release  chan struct{}
	sendErr  error
	postStop atomic.Bool // set by the test once Stop has returned
	violated atomic.Int32
}

func (g *gateClient) record() {
	if g.postStop.Load() {
		// A send that BEGINS after Stop returned is precisely the
		// use-after-free: the caller has already unmapped by then.
		g.violated.Add(1)
	}
	g.mu.Lock()
	g.sends++
	g.mu.Unlock()
	if g.started != nil {
		g.once.Do(func() { close(g.started) })
	}
	if g.release != nil {
		<-g.release
	}
}

func (g *gateClient) SendGuestCallWithTimeout(data []byte, msgType shm.MsgType, _ time.Duration) ([]byte, error) {
	g.record()
	return nil, g.sendErr
}

func (g *gateClient) SendGuestCall(data []byte, msgType shm.MsgType) ([]byte, error) {
	g.record()
	return nil, g.sendErr
}

func (g *gateClient) sendCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sends
}

// TestRtdManager_StopRejectsEverySendPath: after Stop, all four entry points must
// return ErrStopped WITHOUT reaching the client. ErrStopped (not a formatted
// string) is what lets a pushing goroutine distinguish "shutting down, stop for
// good" from a transient 1s RtdUpdate timeout.
func TestRtdManager_StopRejectsEverySendPath(t *testing.T) {
	stub := &gateClient{}
	m := NewRtdManager()
	m.client = stub
	m.Subscribe("K", 7)

	if ok := m.Stop(2 * time.Second); !ok {
		t.Fatal("Stop timed out with no sends in flight")
	}

	cases := []struct {
		name string
		call func() error
	}{
		{"SendUpdate", func() error { return m.SendUpdate(7, 1.0) }},
		{"SendErrorUpdate", func() error { return m.SendErrorUpdate(7, "boom") }},
		{"Publish", func() error { return m.Publish("K", 1.0) }},
		{"SendOnceGrid", func() error { return m.SendOnceGrid("K", []byte("payload")) }},
	}
	for _, tc := range cases {
		if err := tc.call(); !errors.Is(err, ErrStopped) {
			t.Errorf("%s after Stop returned %v, want ErrStopped", tc.name, err)
		}
	}
	if got := stub.sendCount(); got != 0 {
		t.Fatalf("%d sends reached the client after Stop; each is a write into an unmapped segment", got)
	}
}

// TestRtdManager_StopWaitsForTheInFlightSend is the core guarantee. It also pins
// that beginSend's registration cannot slip past the drain: the send is already
// inside the client when Stop is called, and Stop must not report success until
// it returns.
func TestRtdManager_StopWaitsForTheInFlightSend(t *testing.T) {
	stub := &gateClient{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	m := NewRtdManager()
	m.client = stub

	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		_ = m.SendUpdate(1, 42.0)
	}()

	select {
	case <-stub.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the send never reached the client")
	}

	stopped := make(chan bool, 1)
	go func() { stopped <- m.Stop(5 * time.Second) }()

	select {
	case ok := <-stopped:
		t.Fatalf("Stop returned (%v) while a send was still inside the client", ok)
	case <-time.After(150 * time.Millisecond):
	}

	close(stub.release)
	select {
	case ok := <-stopped:
		if !ok {
			t.Fatal("Stop reported a timeout even though the send completed inside the budget")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after the send completed")
	}

	select {
	case <-sendDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop returned before the sending goroutine left the client")
	}
}

// TestRtdManager_StopReportsFalseWhenASendHangs pins the safety valve: Stop must
// be able to say "not drained" so the caller SKIPS the unmap. Skipping is free at
// process exit; unmapping anyway is the fatal fault.
func TestRtdManager_StopReportsFalseWhenASendHangs(t *testing.T) {
	stub := &gateClient{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(stub.release)

	m := NewRtdManager()
	m.client = stub
	go func() { _ = m.SendUpdate(1, 42.0) }()

	select {
	case <-stub.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the send never reached the client")
	}

	if ok := m.Stop(100 * time.Millisecond); ok {
		t.Fatal("Stop claimed a completed drain while a send was still inside the client")
	}
}

// TestRtdManager_StopUnderLiveStreamingPushers is the scenario from the report:
// active RTD push goroutines running while teardown is driven. Not one send may
// BEGIN after Stop returns — that is exactly the window in which the caller
// unmaps.
//
// Run under -race; the gate's mutual exclusion between latching `stopped` and
// WaitGroup.Add is what the race detector would otherwise catch as an
// Add-after-Wait.
func TestRtdManager_StopUnderLiveStreamingPushers(t *testing.T) {
	const pushers = 32

	stub := &gateClient{}
	m := NewRtdManager()
	m.client = stub
	for i := 0; i < pushers; i++ {
		m.Subscribe("K", int32(i))
	}

	var wg sync.WaitGroup
	var stoppedSeen atomic.Int32
	quit := make(chan struct{})

	for i := 0; i < pushers; i++ {
		wg.Add(1)
		go func(id int32) {
			defer wg.Done()
			for {
				select {
				case <-quit:
					return
				default:
				}
				// Mix all four paths, exactly as a real project does (streaming
				// pushes, error pushes, a fan-out publish, a one-shot grid).
				var err error
				switch id % 4 {
				case 0:
					err = m.SendUpdate(id, float64(id))
				case 1:
					err = m.SendErrorUpdate(id, "e")
				case 2:
					err = m.Publish("K", float64(id))
				default:
					err = m.SendOnceGrid("K", []byte("payload"))
				}
				if errors.Is(err, ErrStopped) {
					stoppedSeen.Add(1)
					return // the documented pusher contract
				}
			}
		}(int32(i))
	}

	// Let the pushers get going, then tear down.
	time.Sleep(20 * time.Millisecond)
	if ok := m.Stop(5 * time.Second); !ok {
		t.Fatal("Stop timed out draining live pushers")
	}
	// From here on, the caller would call client.Close(). Any send that starts
	// now is a use-after-free.
	stub.postStop.Store(true)

	close(quit)
	wg.Wait()

	if v := stub.violated.Load(); v != 0 {
		t.Fatalf("%d sends began AFTER Stop returned; client.Close() would unmap underneath them", v)
	}
	if stoppedSeen.Load() == 0 {
		t.Fatal("no pusher ever observed ErrStopped; the test did not exercise the gate")
	}
	if stub.sendCount() == 0 {
		t.Fatal("no send ever reached the client; the test is vacuous")
	}
}

// TestRtdManager_StopIsIdempotent: both teardown triggers in the generated server
// (Serve's deferred shutdown and the parent-death watcher) may call it, possibly
// concurrently.
func TestRtdManager_StopIsIdempotent(t *testing.T) {
	m := NewRtdManager()
	m.client = &gateClient{}

	var wg sync.WaitGroup
	results := make([]bool, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = m.Stop(5 * time.Second)
		}(i)
	}
	wg.Wait()
	for i, ok := range results {
		if !ok {
			t.Errorf("concurrent Stop caller %d reported a timeout", i)
		}
	}
}

// TestRtdManager_NotConnectedErrorsSurvive: the gate replaced the old
// `client == nil` checks, so the "not connected" behavior must be unchanged for a
// manager that was never given a client (a real state — SetClient only runs when
// rtd.enabled).
func TestRtdManager_NotConnectedErrorsSurvive(t *testing.T) {
	m := NewRtdManager()
	m.Subscribe("K", 3)

	if err := m.SendUpdate(3, 1.0); err == nil || errors.Is(err, ErrStopped) {
		t.Errorf("SendUpdate with no client: got %v, want a plain not-connected error", err)
	}
	if err := m.Publish("K", 1.0); err == nil || errors.Is(err, ErrStopped) {
		t.Errorf("Publish with no client: got %v, want a plain not-connected error", err)
	}
	if err := m.SendOnceGrid("K", []byte("x")); err == nil || errors.Is(err, ErrStopped) {
		t.Errorf("SendOnceGrid with no client: got %v, want a plain not-connected error", err)
	}
	// Publish to a key with NO subscribers is still a no-op success, gate or not.
	if err := m.Publish("unsubscribed", 1.0); err != nil {
		t.Errorf("Publish to an unsubscribed key: got %v, want nil", err)
	}
}
