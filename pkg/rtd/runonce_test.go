package rtd

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeSender records every SendUpdate call. It can also be told to fail.
type fakeSender struct {
	mu      sync.Mutex
	calls   []fakeSend
	sendErr error
}

type fakeSend struct {
	topicID int32
	value   any
}

func (f *fakeSender) SendUpdate(topicID int32, value any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeSend{topicID: topicID, value: value})
	return f.sendErr
}

func (f *fakeSender) snapshot() []fakeSend {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeSend(nil), f.calls...)
}

// TestRunOnce_PushesResultExactlyOnce: a successful handler result is pushed
// once and only once via SendUpdate, with the handler invoked exactly once.
func TestRunOnce_PushesResultExactlyOnce(t *testing.T) {
	var runs int32
	fn := func(ctx context.Context) (any, error) {
		atomic.AddInt32(&runs, 1)
		return 42.0, nil
	}

	fs := &fakeSender{}
	if err := RunOnce(context.Background(), fs, 7, fn); err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}

	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("handler ran %d times, want 1", got)
	}
	calls := fs.snapshot()
	if len(calls) != 1 {
		t.Fatalf("SendUpdate called %d times, want 1", len(calls))
	}
	if calls[0].topicID != 7 {
		t.Errorf("topicID = %d, want 7", calls[0].topicID)
	}
	if calls[0].value != 42.0 {
		t.Errorf("value = %v, want 42.0", calls[0].value)
	}
}

// TestRunOnce_HandlerErrorPushesString: a handler error is delivered as its
// string so the cell leaves #GETTING_DATA, and the handler is not retried.
func TestRunOnce_HandlerErrorPushesString(t *testing.T) {
	var runs int32
	fn := func(ctx context.Context) (any, error) {
		atomic.AddInt32(&runs, 1)
		return nil, errors.New("boom: upstream timeout")
	}

	fs := &fakeSender{}
	if err := RunOnce(context.Background(), fs, 3, fn); err != nil {
		t.Fatalf("RunOnce returned send error: %v", err)
	}

	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("handler ran %d times, want 1", got)
	}
	calls := fs.snapshot()
	if len(calls) != 1 {
		t.Fatalf("SendUpdate called %d times, want 1", len(calls))
	}
	if s, ok := calls[0].value.(string); !ok || s != "boom: upstream timeout" {
		t.Errorf("value = %v, want error string \"boom: upstream timeout\"", calls[0].value)
	}
}

// TestRunOnce_ContextAlreadyCancelled: when ctx is done before the handler
// runs, RunOnce pushes the cancellation reason and never invokes the handler.
func TestRunOnce_ContextAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var runs int32
	fn := func(ctx context.Context) (any, error) {
		atomic.AddInt32(&runs, 1)
		return "should not happen", nil
	}

	fs := &fakeSender{}
	if err := RunOnce(ctx, fs, 1, fn); err != nil {
		t.Fatalf("RunOnce returned send error: %v", err)
	}

	if got := atomic.LoadInt32(&runs); got != 0 {
		t.Fatalf("handler ran %d times on cancelled ctx, want 0", got)
	}
	calls := fs.snapshot()
	if len(calls) != 1 {
		t.Fatalf("SendUpdate called %d times, want 1", len(calls))
	}
	if s, ok := calls[0].value.(string); !ok || s != context.Canceled.Error() {
		t.Errorf("value = %v, want %q", calls[0].value, context.Canceled.Error())
	}
}

// TestRunOnce_HandlerObservesCancellation: a handler that returns ctx.Err()
// after observing cancellation gets that string delivered (the cell does not
// hang).
func TestRunOnce_HandlerObservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	fn := func(ctx context.Context) (any, error) {
		cancel() // simulate work that gets cancelled mid-flight
		return nil, ctx.Err()
	}

	fs := &fakeSender{}
	if err := RunOnce(ctx, fs, 5, fn); err != nil {
		t.Fatalf("RunOnce returned send error: %v", err)
	}

	calls := fs.snapshot()
	if len(calls) != 1 {
		t.Fatalf("SendUpdate called %d times, want 1", len(calls))
	}
	if s, ok := calls[0].value.(string); !ok || s != context.Canceled.Error() {
		t.Errorf("value = %v, want %q", calls[0].value, context.Canceled.Error())
	}
}

// TestRunOnce_NilHandler: a nil handler is a programming error; RunOnce returns
// an error and pushes nothing.
func TestRunOnce_NilHandler(t *testing.T) {
	fs := &fakeSender{}
	if err := RunOnce(context.Background(), fs, 1, nil); err == nil {
		t.Fatal("expected error for nil handler")
	}
	if len(fs.snapshot()) != 0 {
		t.Fatal("nil handler must not push anything")
	}
}

// TestRunOnce_SendErrorPropagates: a SendUpdate failure is returned to the
// caller (so it can be logged) and the handler still ran exactly once.
func TestRunOnce_SendErrorPropagates(t *testing.T) {
	var runs int32
	fn := func(ctx context.Context) (any, error) {
		atomic.AddInt32(&runs, 1)
		return 1.0, nil
	}
	fs := &fakeSender{sendErr: errors.New("host stalled")}
	err := RunOnce(context.Background(), fs, 1, fn)
	if err == nil || err.Error() != "host stalled" {
		t.Fatalf("expected send error to propagate, got %v", err)
	}
	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("handler ran %d times, want 1", got)
	}
}

// fakeGridSender records, IN ORDER, every SendOnceGrid and SendUpdate call so
// tests can assert the grid-then-readiness ordering. Either call can be told
// to fail.
type fakeGridSender struct {
	mu          sync.Mutex
	events      []gridEvent
	onceGridErr error
	updateErr   error
}

type gridEvent struct {
	kind    string // "grid" or "update"
	key     string // for "grid"
	payload []byte // for "grid"
	topicID int32  // for "update"
	value   any    // for "update"
}

func (f *fakeGridSender) SendOnceGrid(key string, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := append([]byte(nil), payload...)
	f.events = append(f.events, gridEvent{kind: "grid", key: key, payload: cp})
	return f.onceGridErr
}

func (f *fakeGridSender) SendUpdate(topicID int32, value any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, gridEvent{kind: "update", topicID: topicID, value: value})
	return f.updateErr
}

func (f *fakeGridSender) snapshot() []gridEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gridEvent(nil), f.events...)
}

// TestRunOnceGrid_SuccessShipsGridThenReadiness: on success the grid is shipped
// via SendOnceGrid(onceKey, payload) FIRST, and only then is a readiness token
// pushed via SendUpdate(topicID, token). The ordering is load-bearing (the host
// must hold the grid before the recalc re-enters the wrapper).
func TestRunOnceGrid_SuccessShipsGridThenReadiness(t *testing.T) {
	var runs int32
	payload := []byte("serialized-RtdOnceGridResult-bytes")
	run := func(ctx context.Context) ([]byte, error) {
		atomic.AddInt32(&runs, 1)
		return payload, nil
	}

	fs := &fakeGridSender{}
	const onceKey = "BDH\x1f AAPL "
	if err := RunOnceGrid(context.Background(), fs, 9, onceKey, run); err != nil {
		t.Fatalf("RunOnceGrid returned error: %v", err)
	}

	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("handler ran %d times, want 1", got)
	}

	ev := fs.snapshot()
	if len(ev) != 2 {
		t.Fatalf("expected 2 events (grid then update), got %d: %+v", len(ev), ev)
	}
	if ev[0].kind != "grid" {
		t.Fatalf("first event must be the grid send, got %q", ev[0].kind)
	}
	if ev[0].key != onceKey {
		t.Errorf("grid key = %q, want %q", ev[0].key, onceKey)
	}
	if string(ev[0].payload) != string(payload) {
		t.Errorf("grid payload = %q, want %q", ev[0].payload, payload)
	}
	if ev[1].kind != "update" {
		t.Fatalf("second event must be the readiness update, got %q", ev[1].kind)
	}
	if ev[1].topicID != 9 {
		t.Errorf("readiness topicID = %d, want 9", ev[1].topicID)
	}
	if _, ok := ev[1].value.(string); !ok {
		t.Errorf("readiness token must be a string, got %T (%v)", ev[1].value, ev[1].value)
	}
}

// TestRunOnceGrid_SendOnceGridErrorPushesErrorNoToken: if SendOnceGrid fails,
// the error string is pushed via SendUpdate and NO fresh readiness token is
// pushed (a recalc would just miss the absent grid).
func TestRunOnceGrid_SendOnceGridErrorPushesErrorNoToken(t *testing.T) {
	run := func(ctx context.Context) ([]byte, error) {
		return []byte("grid-bytes"), nil
	}
	fs := &fakeGridSender{onceGridErr: errors.New("host stalled on grid")}

	if err := RunOnceGrid(context.Background(), fs, 4, "K\x1f1", run); err != nil {
		t.Fatalf("RunOnceGrid returned error: %v", err)
	}

	ev := fs.snapshot()
	if len(ev) != 2 {
		t.Fatalf("expected 2 events (failed grid send + error update), got %d: %+v", len(ev), ev)
	}
	if ev[0].kind != "grid" {
		t.Fatalf("first event must be the (failing) grid send, got %q", ev[0].kind)
	}
	if ev[1].kind != "update" {
		t.Fatalf("second event must be the error update, got %q", ev[1].kind)
	}
	s, ok := ev[1].value.(string)
	if !ok || s != "host stalled on grid" {
		t.Errorf("update value = %v, want error string %q", ev[1].value, "host stalled on grid")
	}
}

// TestRunOnceGrid_RunErrorNeverShipsGrid: a handler/serialize error pushes the
// error string and NEVER calls SendOnceGrid.
func TestRunOnceGrid_RunErrorNeverShipsGrid(t *testing.T) {
	run := func(ctx context.Context) ([]byte, error) {
		return nil, errors.New("serialize failed: jagged grid")
	}
	fs := &fakeGridSender{}

	if err := RunOnceGrid(context.Background(), fs, 2, "K\x1f1", run); err != nil {
		t.Fatalf("RunOnceGrid returned error: %v", err)
	}

	ev := fs.snapshot()
	if len(ev) != 1 {
		t.Fatalf("expected exactly 1 event (the error update), got %d: %+v", len(ev), ev)
	}
	if ev[0].kind != "update" {
		t.Fatalf("the only event must be the error update (no grid send), got %q", ev[0].kind)
	}
	for _, e := range ev {
		if e.kind == "grid" {
			t.Fatal("SendOnceGrid must NOT be called when the handler errors")
		}
	}
	s, ok := ev[0].value.(string)
	if !ok || s != "serialize failed: jagged grid" {
		t.Errorf("update value = %v, want error string", ev[0].value)
	}
}

// TestRunOnceGrid_ReadinessTokensChange: two successful runs push DIFFERENT
// readiness tokens, so Excel always sees a value change and recalcs.
func TestRunOnceGrid_ReadinessTokensChange(t *testing.T) {
	run := func(ctx context.Context) ([]byte, error) { return []byte("g"), nil }

	fs := &fakeGridSender{}
	if err := RunOnceGrid(context.Background(), fs, 1, "K\x1fa", run); err != nil {
		t.Fatalf("first RunOnceGrid: %v", err)
	}
	if err := RunOnceGrid(context.Background(), fs, 1, "K\x1fa", run); err != nil {
		t.Fatalf("second RunOnceGrid: %v", err)
	}

	var tokens []string
	for _, e := range fs.snapshot() {
		if e.kind == "update" {
			s, ok := e.value.(string)
			if !ok {
				t.Fatalf("readiness token not a string: %T", e.value)
			}
			tokens = append(tokens, s)
		}
	}
	if len(tokens) != 2 {
		t.Fatalf("expected 2 readiness tokens, got %d", len(tokens))
	}
	if tokens[0] == tokens[1] {
		t.Fatalf("readiness tokens must change between deliveries; both were %q", tokens[0])
	}
}

// TestRunOnceGrid_NilGuards: nil run/manager are programming errors that return
// immediately and push nothing.
func TestRunOnceGrid_NilGuards(t *testing.T) {
	fs := &fakeGridSender{}
	if err := RunOnceGrid(context.Background(), fs, 1, "K", nil); err == nil {
		t.Fatal("expected error for nil run")
	}
	if len(fs.snapshot()) != 0 {
		t.Fatal("nil run must not push anything")
	}
	run := func(ctx context.Context) ([]byte, error) { return []byte("g"), nil }
	if err := RunOnceGrid(context.Background(), nil, 1, "K", run); err == nil {
		t.Fatal("expected error for nil manager")
	}
}

// TestRunOnceGrid_ContextAlreadyCancelled: a ctx done before the handler runs
// pushes the cancellation reason and never ships a grid.
func TestRunOnceGrid_ContextAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var runs int32
	run := func(ctx context.Context) ([]byte, error) {
		atomic.AddInt32(&runs, 1)
		return []byte("g"), nil
	}
	fs := &fakeGridSender{}
	if err := RunOnceGrid(ctx, fs, 1, "K", run); err != nil {
		t.Fatalf("RunOnceGrid returned error: %v", err)
	}
	if got := atomic.LoadInt32(&runs); got != 0 {
		t.Fatalf("handler ran %d times on cancelled ctx, want 0", got)
	}
	ev := fs.snapshot()
	if len(ev) != 1 || ev[0].kind != "update" {
		t.Fatalf("expected a single error update, got %+v", ev)
	}
	if s, ok := ev[0].value.(string); !ok || s != context.Canceled.Error() {
		t.Errorf("value = %v, want %q", ev[0].value, context.Canceled.Error())
	}
}
