package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/pkg/rtd"
)

func buildRtdConnect(topicID int32, strings []string) []byte {
	b := flatbuffers.NewBuilder(64)
	offs := make([]flatbuffers.UOffsetT, len(strings))
	for i, s := range strings {
		offs[i] = b.CreateString(s)
	}
	protocol.RtdConnectRequestStartStringsVector(b, len(offs))
	for i := len(offs) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offs[i])
	}
	vec := b.EndVector(len(offs))
	protocol.RtdConnectRequestStart(b)
	protocol.RtdConnectRequestAddTopicId(b, topicID)
	protocol.RtdConnectRequestAddStrings(b, vec)
	b.Finish(protocol.RtdConnectRequestEnd(b))
	return b.FinishedBytes()
}

func buildRtdDisconnect(topicID int32) []byte {
	b := flatbuffers.NewBuilder(32)
	protocol.RtdDisconnectRequestStart(b)
	protocol.RtdDisconnectRequestAddTopicId(b, topicID)
	b.Finish(protocol.RtdDisconnectRequestEnd(b))
	return b.FinishedBytes()
}

func newRtdSysHandler() *SystemHandler {
	return NewSystemHandler(NewChunkManager(), NewAsyncBatcher(), NewCommandBatcher(), NewRefCache(), rtd.NewRtdManager())
}

// TestHandleRtdConnect_DisconnectCancelsInFlight is the end-to-end intent test:
// HandleRtdConnect launches a long handler that blocks on ctx.Done(); a
// HandleRtdDisconnect for the same topicID while the handler is still in flight
// must cancel the handler's context so it stops. Pure Go (no Excel/server
// spawn) — unit-level.
func TestHandleRtdConnect_DisconnectCancelsInFlight(t *testing.T) {
	const topicID = int32(101)
	h := newRtdSysHandler()
	respBuf := make([]byte, 4096)
	b := flatbuffers.NewBuilder(256)

	started := make(chan struct{})
	finished := make(chan error, 1)
	onConnect := func(ctx context.Context, id int32, args []string, newValues bool) error {
		close(started)
		<-ctx.Done() // a long handler that observes cancellation
		err := ctx.Err()
		finished <- err
		return err
	}

	n, msgType := h.HandleRtdConnect(buildRtdConnect(topicID, []string{"StockTick", "AAPL"}), respBuf, b, onConnect)
	if msgType != MsgRtdConnect {
		t.Fatalf("connect ack msgType = %d, want MsgRtdConnect (%d)", msgType, MsgRtdConnect)
	}
	if n <= 0 {
		t.Fatalf("connect ack wrote no response, n=%d", n)
	}

	// Wait until the handler is actually running (in flight) before disconnect.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("connect handler never started")
	}

	// Disconnect mid-flight. This must cancel the in-flight handler's context.
	dn, dmsg := h.HandleRtdDisconnect(buildRtdDisconnect(topicID), respBuf, b, nil)
	if dn != 0 || dmsg != 0 {
		t.Fatalf("disconnect should write no response, got n=%d msgType=%d", dn, dmsg)
	}

	select {
	case err := <-finished:
		if err != context.Canceled {
			t.Fatalf("handler returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("disconnect did not cancel the in-flight connect handler (it ran to completion against a dead topic)")
	}
}

// TestHandleRtdConnect_StreamingCtxOutlivesConnect models the real streaming
// RTD shape (showcase Clock_RTD / StockTick_RTD): the onConnect handler captures
// ctx, launches a fire-and-forget goroutine that pushes on a short ticker until
// <-ctx.Done(), and RETURNS nil immediately. The streaming goroutine must keep
// running after HandleRtdConnect returns (ctx still LIVE) and must stop only on
// HandleRtdDisconnect (ctx cancelled via RtdManager.Unsubscribe).
//
// Before the fix, the connect goroutine's `defer cancel()` fired the moment the
// handler returned nil, killing ctx after exactly one tick — so the counter
// froze at its connect-time value. This test FAILS on that code and PASSES once
// cancellation is owned by disconnect.
func TestHandleRtdConnect_StreamingCtxOutlivesConnect(t *testing.T) {
	const topicID = int32(303)
	const tick = 10 * time.Millisecond
	h := newRtdSysHandler()
	respBuf := make([]byte, 4096)
	b := flatbuffers.NewBuilder(256)

	var ticks int64
	streaming := func(ctx context.Context, id int32, args []string, newValues bool) error {
		atomic.AddInt64(&ticks, 1) // immediate first value at connect
		go func() {
			t := time.NewTicker(tick)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					atomic.AddInt64(&ticks, 1)
				}
			}
		}()
		return nil // streaming handler returns immediately
	}

	h.HandleRtdConnect(buildRtdConnect(topicID, []string{"Clock"}), respBuf, b, streaming)

	// Let several ticker periods elapse; a LIVE ctx means the counter keeps
	// climbing well past the single connect-time value.
	time.Sleep(20 * tick)
	got := atomic.LoadInt64(&ticks)
	if got < 5 {
		t.Fatalf("streaming ctx was cancelled on connect return: counter=%d, want it to keep advancing (>=5). The stream delivered only the connect-time value(s) and then died.", got)
	}

	// Disconnect must cancel ctx so the streaming goroutine stops advancing.
	h.HandleRtdDisconnect(buildRtdDisconnect(topicID), respBuf, b, nil)
	// Allow the in-flight tick (if any) to land, then snapshot and confirm the
	// counter is frozen.
	time.Sleep(5 * tick)
	settled := atomic.LoadInt64(&ticks)
	time.Sleep(20 * tick)
	if final := atomic.LoadInt64(&ticks); final != settled {
		t.Fatalf("disconnect did not cancel the streaming ctx: counter advanced %d -> %d after disconnect", settled, final)
	}
}

// TestHandleRtdConnect_NormalCompletionDeregisters: a connect handler that
// completes normally must deregister its cancel so a LATER disconnect for the
// same topicID does not try to cancel an already-finished handler (and cannot
// clobber a reused-topicID registration). Verified indirectly: after normal
// completion, a fresh connect on the same topicID can be cancelled by its own
// disconnect.
func TestHandleRtdConnect_NormalCompletionDeregisters(t *testing.T) {
	const topicID = int32(202)
	h := newRtdSysHandler()
	respBuf := make([]byte, 4096)
	b := flatbuffers.NewBuilder(256)

	done := make(chan struct{})
	quick := func(ctx context.Context, id int32, args []string, newValues bool) error {
		close(done)
		return nil
	}
	h.HandleRtdConnect(buildRtdConnect(topicID, []string{"StockTick"}), respBuf, b, quick)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("quick handler never ran")
	}
	// Give the deferred deregister a moment to run after the handler returns.
	time.Sleep(50 * time.Millisecond)

	// A second, blocking connect reuses the topicID; its own disconnect must
	// still cancel it (the first connect's deregister must not have left a
	// stale entry that a disconnect would mis-target).
	started := make(chan struct{})
	finished := make(chan error, 1)
	blocking := func(ctx context.Context, id int32, args []string, newValues bool) error {
		close(started)
		<-ctx.Done()
		finished <- ctx.Err()
		return ctx.Err()
	}
	h.HandleRtdConnect(buildRtdConnect(topicID, []string{"StockTick"}), respBuf, b, blocking)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("second connect handler never started")
	}
	h.HandleRtdDisconnect(buildRtdDisconnect(topicID), respBuf, b, nil)
	select {
	case err := <-finished:
		if err != context.Canceled {
			t.Fatalf("second handler returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("disconnect did not cancel the reused-topicID connect handler")
	}
}
