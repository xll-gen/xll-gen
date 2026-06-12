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
