package server

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
)

// newCalcHandler builds a SystemHandler with just the pieces the calc-boundary
// handlers touch. No SHM, no chunking: HandleCalculationEnded only reaches
// SendAckOrChunk when there ARE commands, and the tests that flush pass a
// respBuf large enough to keep it on the inline path.
func newCalcHandler() *SystemHandler {
	return &SystemHandler{
		CommandBatcher: NewCommandBatcher(),
		RefCache:       NewRefCache(),
		ChunkManager:   NewChunkManager(),
	}
}

// scheduleOneSet queues a single-cell Set command on the batcher.
func scheduleOneSet(t *testing.T, cb *CommandBatcher, val int32) {
	t.Helper()

	b := flatbuffers.NewBuilder(1024)
	rOff := createRange(b, 0, 0, 0, 0)
	b.Finish(rOff)
	rBuf := append([]byte(nil), b.FinishedBytes()...)
	rng := protocol.GetRootAsRange(rBuf, 0)

	b.Reset()
	vOff := createScalarAny(b, val)
	b.Finish(vOff)
	vBuf := append([]byte(nil), b.FinishedBytes()...)
	any := protocol.GetRootAsAny(vBuf, 0)

	cb.ScheduleSet(rng, any)
}

// flushEnded runs HandleCalculationEnded and returns the commands it emitted.
func flushEnded(t *testing.T, h *SystemHandler, onEnded func(context.Context) error) *protocol.CalculationEndedResponse {
	t.Helper()

	respBuf := make([]byte, 64*1024)
	b := flatbuffers.NewBuilder(1024)
	n, _ := h.HandleCalculationEnded(respBuf, b, onEnded)
	if n <= 0 {
		return nil
	}
	return protocol.GetRootAsCalculationEndedResponse(respBuf[:n], 0)
}

// TestHandleCalculationCanceled_PreservesBatchedCommands is the regression for
// the CommandBatcher.Clear() that HandleCalculationCanceled used to do.
//
// Excel fires CalculationCanceled and then CalculationEnded 2-6 ms later for
// ONE interrupted cycle (measured; AGENTS.md §19.4). Clearing the batcher on
// cancel therefore threw away that cycle's ScheduleSet/ScheduleFormat a few
// milliseconds before the Ended flush that is supposed to emit them — silent
// data loss, not cleanup. Cancellation means "the calculation was interrupted",
// not "discard the writes".
//
// FAIL-before: restoring `h.CommandBatcher.Clear()` in HandleCalculationCanceled
// makes every sub-case report 0 commands.
func TestHandleCalculationCanceled_PreservesBatchedCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// run performs the cancel step; each variant models a different source
		// of the scheduled command relative to the cancel.
		run func(t *testing.T, h *SystemHandler)
	}{
		{
			name: "scheduled before the cancel",
			run: func(t *testing.T, h *SystemHandler) {
				scheduleOneSet(t, h.CommandBatcher, 100)
				h.HandleCalculationCanceled(nil)
			},
		},
		{
			name: "scheduled by the cancel handler itself",
			run: func(t *testing.T, h *SystemHandler) {
				h.HandleCalculationCanceled(func(context.Context) error {
					scheduleOneSet(t, h.CommandBatcher, 100)
					return nil
				})
			},
		},
		{
			name: "cancel handler errors",
			run: func(t *testing.T, h *SystemHandler) {
				scheduleOneSet(t, h.CommandBatcher, 100)
				h.HandleCalculationCanceled(func(context.Context) error {
					return errors.New("boom")
				})
			},
		},
		{
			name: "cancel handler panics",
			run: func(t *testing.T, h *SystemHandler) {
				scheduleOneSet(t, h.CommandBatcher, 100)
				h.HandleCalculationCanceled(func(context.Context) error {
					panic("boom")
				})
			},
		},
		{
			name: "repeated cancels before the ended",
			run: func(t *testing.T, h *SystemHandler) {
				scheduleOneSet(t, h.CommandBatcher, 100)
				h.HandleCalculationCanceled(nil)
				h.HandleCalculationCanceled(nil)
				h.HandleCalculationCanceled(nil)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newCalcHandler()
			tc.run(t, h)

			resp := flushEnded(t, h, nil)
			if resp == nil {
				t.Fatal("CalculationEnded emitted no response: the cancel dropped the batched command")
			}
			if got := resp.CommandsLength(); got != 1 {
				t.Fatalf("commands surviving the cancel = %d, want 1", got)
			}

			var wrapper protocol.CommandWrapper
			resp.Commands(&wrapper, 0)
			if wrapper.CmdType() != protocol.CommandSetCommand {
				t.Fatalf("command type = %v, want SetCommand", wrapper.CmdType())
			}
			var setCmd protocol.SetCommand
			tbl := new(flatbuffers.Table)
			if !wrapper.Cmd(tbl) {
				t.Fatal("command wrapper carries no payload")
			}
			setCmd.Init(tbl.Bytes, tbl.Pos)
			var iv protocol.Int
			vtbl := new(flatbuffers.Table)
			if !setCmd.Value(nil).Val(vtbl) {
				t.Fatal("SetCommand carries no value")
			}
			iv.Init(vtbl.Bytes, vtbl.Pos)
			if iv.Val() != 100 {
				t.Errorf("SetCommand value = %d, want 100", iv.Val())
			}
		})
	}
}

// TestHandleCalculationCanceled_InvokesHandlerSynchronously pins the ordering
// guarantee. The XLL blocks Excel's STA thread inside its MSG_CALCULATION_CANCELED
// send, so Excel cannot fire CalculationEnded until this returns — but that only
// yields the measured Canceled -> Ended handler order if the guest runs
// onCanceled BEFORE replying. A goroutine dispatch (the previous behavior) lets
// OnCalculationCanceled land after, or concurrently with, OnCalculationEnded.
//
// FAIL-before: with the old `go func(){...}()` dispatch, `done` is false when
// HandleCalculationCanceled returns (racily, but reliably under -race).
func TestHandleCalculationCanceled_InvokesHandlerSynchronously(t *testing.T) {
	t.Parallel()

	h := newCalcHandler()
	done := false
	h.HandleCalculationCanceled(func(context.Context) error {
		// Give a goroutine dispatch every chance to be observed as async.
		runtime.Gosched()
		done = true
		return nil
	})
	if !done {
		t.Fatal("OnCalculationCanceled had not completed when HandleCalculationCanceled returned; " +
			"the Canceled -> Ended handler ordering is not guaranteed")
	}
}

// TestCalculationCanceledThenEnded_HandlerOrder replays the sequence Excel
// actually produces for an Esc-interrupted recalc (AGENTS.md §19.4): the XLL
// dispatches 132 and then, once that round-trip has returned, 131. Both user
// handlers must run, in that order — this is the documented user contract, and
// hiding it (e.g. suppressing Ended after a Cancel) would break handlers that
// do their cleanup at calc end.
func TestCalculationCanceledThenEnded_HandlerOrder(t *testing.T) {
	t.Parallel()

	h := newCalcHandler()

	var mu sync.Mutex
	var order []string
	record := func(name string) func(context.Context) error {
		return func(context.Context) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}

	// The XLL's sends are sequential on the STA thread: the second only starts
	// after the first has returned.
	h.HandleCalculationCanceled(record("canceled"))
	flushEnded(t, h, record("ended"))

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "canceled" || order[1] != "ended" {
		t.Fatalf("handler order = %v, want [canceled ended]", order)
	}
}

// TestHandleCalculationCanceled_ReplyIsEmpty pins the wire shape: the cancel is
// a notification, so nothing is folded into its response. The C++ side
// (xll_events.cpp HandleCalculationCanceled) reads no payload back.
func TestHandleCalculationCanceled_ReplyIsEmpty(t *testing.T) {
	t.Parallel()

	h := newCalcHandler()
	scheduleOneSet(t, h.CommandBatcher, 7)

	n, msgType := h.HandleCalculationCanceled(nil)
	if n != 0 || msgType != 0 {
		t.Fatalf("HandleCalculationCanceled = (%d, %d), want (0, 0): the cancel must not flush commands", n, msgType)
	}
}
