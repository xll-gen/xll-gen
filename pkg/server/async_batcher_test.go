package server

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	shm "github.com/xll-gen/shm/go"
	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/pkg/chunk"
)

// ---------------------------------------------------------------------------
// Per-transfer size cap on the async flush path
// ---------------------------------------------------------------------------
//
// The receiver in this direction is the XLL's HandleChunk, which refuses a
// transfer whose declared total_size exceeds its compile-time 256 MiB cap
// (kMaxChunkTotalSize). These tests drive the SAME code path with a tiny
// `limit` instead, because the behavior under test is "what happens at the
// bound", not the bound's numeric value — materializing a 256 MiB batch to
// assert a branch would cost a gigabyte of test RSS and prove nothing extra.
// chunk.MaxTransferBytes (the production value FlushAsyncBatch passes) is
// pinned separately by TestFlushAsyncBatchUsesTheReceiverCap and, against the
// C++ constant, by internal/assets' chunk gate.

// stubGuestSender captures guest->host sends without an SHM segment.
//
// MaxRequestSize deliberately reports 0 — shm's "unknown, use a conservative
// fallback" — which makes chunk.GuestBudget fall back to chunk.DefaultChunkSize
// (524032). Every payload these tests build is far below that, so each delivered
// transfer is exactly one SendGuestCall carrying a whole BatchAsyncResponse —
// the frames are easy to decode and the per-frame chunking path (covered by the
// real-SHM tests) stays out of the way.
type stubGuestSender struct {
	mu    sync.Mutex
	sends []stubSend
	fail  error
}

type stubSend struct {
	payload []byte
	msgType shm.MsgType
}

func (s *stubGuestSender) MaxRequestSize() int { return 0 }

func (s *stubGuestSender) SendGuestCall(data []byte, msgType shm.MsgType) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return nil, s.fail
	}
	s.sends = append(s.sends, stubSend{payload: append([]byte(nil), data...), msgType: msgType})
	return nil, nil
}

func (s *stubGuestSender) transfers() []stubSend {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubSend(nil), s.sends...)
}

// delivered decodes every captured BatchAsyncResponse into handle -> outcome,
// where outcome is either "err:<message>" or "val". It also returns the byte
// size of each transfer so the cap can be asserted.
func (s *stubGuestSender) delivered(t *testing.T) (map[string]string, []int) {
	t.Helper()
	out := make(map[string]string)
	var sizes []int
	for _, snd := range s.transfers() {
		if snd.msgType != MsgBatchAsyncResponse {
			t.Fatalf("unexpected slot msgType %d; these tests keep payloads under one slot", snd.msgType)
		}
		sizes = append(sizes, len(snd.payload))
		batch := protocol.GetRootAsBatchAsyncResponse(snd.payload, 0)
		for i := 0; i < batch.ResultsLength(); i++ {
			var res protocol.AsyncResult
			if !batch.Results(&res, i) {
				t.Fatalf("result %d missing from a delivered batch", i)
			}
			h := string(res.HandleBytes())
			if _, dup := out[h]; dup {
				t.Fatalf("handle %q delivered twice", h)
			}
			if e := res.Error(); len(e) > 0 {
				out[h] = "err:" + string(e)
			} else {
				out[h] = "val"
			}
		}
	}
	return out, sizes
}

// strResult builds one async result whose serialized size is dominated by an
// n-byte string payload.
func strResult(handle string, n int) PendingAsyncResult {
	return PendingAsyncResult{
		Handle:  []byte(handle),
		Val:     strings.Repeat("x", n),
		ValType: protocol.AnyValueStr,
	}
}

// TestFlushAsyncBatch_SplitsOversizedBatch is the "255 innocent cells died with
// the big one" regression. Before the size-aware split, a batch whose SERIALIZED
// size crossed the receiver's per-transfer cap was framed and pushed whole; the
// host refused the first frame on total_size, the retry ladder absorbed the
// SYSTEM_ERROR, and every handle in the batch was dropped with one log line.
func TestFlushAsyncBatch_SplitsOversizedBatch(t *testing.T) {
	const limit = 4096
	const results = 16
	const perResult = 1000 // 16 x ~1 KiB is ~4x the limit; a single one fits easily

	batch := make([]PendingAsyncResult, results)
	for i := range batch {
		batch[i] = strResult(fmt.Sprintf("h%02d", i), perResult)
	}

	s := &stubGuestSender{}
	flushAsyncBatchBounded(batch, s, limit, false)

	got, sizes := s.delivered(t)
	if len(got) != results {
		t.Fatalf("delivered %d results, want all %d (handles: %v)", len(got), results, got)
	}
	for i := 0; i < results; i++ {
		h := fmt.Sprintf("h%02d", i)
		if got[h] != "val" {
			t.Errorf("handle %s: got %q, want the real value (splitting must not turn a deliverable result into an error)", h, got[h])
		}
	}
	if len(sizes) < 2 {
		t.Fatalf("an oversized batch must be split into several transfers; got %d", len(sizes))
	}
	for i, n := range sizes {
		if n > limit {
			t.Errorf("transfer %d is %d bytes, over the %d-byte per-transfer cap", i, n, limit)
		}
	}
}

// TestFlushAsyncBatch_SingleOversizedResultAnswersTheHandle pins the contract
// for the case splitting CANNOT fix: one result that alone exceeds the cap (the
// realistic trigger — an async grid of ~8-10M cells). The handle must be
// answered with an error result, because AsyncResult.error is rendered by the
// XLL's ProcessAsyncBatchResponse into the cell as a string; dropping it instead
// leaves the cell at #GETTING_DATA until Excel's own async timeout, with no
// diagnostic anywhere the user can see.
func TestFlushAsyncBatch_SingleOversizedResultAnswersTheHandle(t *testing.T) {
	const limit = 4096

	s := &stubGuestSender{}
	flushAsyncBatchBounded([]PendingAsyncResult{strResult("huge", 10000)}, s, limit, false)

	got, sizes := s.delivered(t)
	if len(got) != 1 {
		t.Fatalf("want exactly one delivered result, got %v", got)
	}
	msg := got["huge"]
	if !strings.HasPrefix(msg, "err:") {
		t.Fatalf("handle must be answered with an error result, got %q", msg)
	}
	for _, want := range []string{"too large to deliver", "10", "4096"} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic %q must mention %q (it is what the user sees in the cell)", msg, want)
		}
	}
	if sizes[0] > limit {
		t.Errorf("the replacement transfer is %d bytes, itself over the %d-byte cap", sizes[0], limit)
	}
}

// TestFlushAsyncBatch_OversizedResultDoesNotKillItsSiblings combines the two:
// one undeliverable giant plus small neighbours. The giant is answered with an
// error, the neighbours with their real values.
func TestFlushAsyncBatch_OversizedResultDoesNotKillItsSiblings(t *testing.T) {
	const limit = 4096

	batch := []PendingAsyncResult{
		strResult("small-a", 100),
		strResult("giant", 20000),
		strResult("small-b", 100),
		strResult("small-c", 100),
	}

	s := &stubGuestSender{}
	flushAsyncBatchBounded(batch, s, limit, false)

	got, sizes := s.delivered(t)
	if len(got) != 4 {
		t.Fatalf("every handle must be answered exactly once; got %v", got)
	}
	for _, h := range []string{"small-a", "small-b", "small-c"} {
		if got[h] != "val" {
			t.Errorf("handle %s: got %q, want its real value", h, got[h])
		}
	}
	if !strings.HasPrefix(got["giant"], "err:") {
		t.Errorf("the oversized result must be answered with an error, got %q", got["giant"])
	}
	for i, n := range sizes {
		if n > limit {
			t.Errorf("transfer %d is %d bytes, over the %d-byte cap", i, n, limit)
		}
	}
}

// TestFlushAsyncBatch_UndeliverableDiagnosticTerminates guards the substitution
// fixpoint: with a limit so small that even the error result does not fit, the
// flush must give up and log rather than substitute forever. (Reachable only
// from a test — the production limit is 256 MiB — but an unbounded recursion
// here would be a stack overflow inside the batch worker.)
func TestFlushAsyncBatch_UndeliverableDiagnosticTerminates(t *testing.T) {
	s := &stubGuestSender{}
	flushAsyncBatchBounded([]PendingAsyncResult{strResult("h", 64)}, s, 8, false)

	if n := len(s.transfers()); n != 0 {
		t.Fatalf("nothing can be delivered under an 8-byte cap; got %d transfers", n)
	}
}

// TestFlushAsyncBatch_FittingBatchIsUnchanged is the no-regression half: a batch
// under the cap must still go out as ONE transfer, exactly as before the split
// logic existed.
func TestFlushAsyncBatch_FittingBatchIsUnchanged(t *testing.T) {
	batch := []PendingAsyncResult{strResult("a", 10), strResult("b", 10), strResult("c", 10)}

	s := &stubGuestSender{}
	flushAsyncBatchBounded(batch, s, chunk.MaxTransferBytes, false)

	got, sizes := s.delivered(t)
	if len(sizes) != 1 {
		t.Fatalf("a fitting batch must be one transfer, got %d", len(sizes))
	}
	for _, h := range []string{"a", "b", "c"} {
		if got[h] != "val" {
			t.Errorf("handle %s: got %q", h, got[h])
		}
	}
}

// TestFlushAsyncBatchUsesTheReceiverCap pins the production wiring: the
// exported entry point must bound transfers by chunk.MaxTransferBytes, the
// mirror of the C++ receiver's kMaxChunkTotalSize. A drifting or absent limit
// here is exactly the defect the tests above describe.
func TestFlushAsyncBatchUsesTheReceiverCap(t *testing.T) {
	if chunk.MaxTransferBytes != 256<<20 {
		t.Fatalf("chunk.MaxTransferBytes = %d, want 256 MiB — the XLL's kMaxChunkTotalSize "+
			"(internal/assets/files/src/xll_worker.cpp) is a COMPILE-TIME constant the sender "+
			"cannot learn at runtime, so the two are hand-kept equal", chunk.MaxTransferBytes)
	}
	if int64(chunk.MaxTransferBytes) != DefaultMaxChunkBufferBytes {
		t.Errorf("chunk.MaxTransferBytes (%d) and DefaultMaxChunkBufferBytes (%d) are equal by hand, "+
			"not by construction — they govern OPPOSITE directions. If you change one on purpose, "+
			"update this test and AGENTS.md §18.6.1 rather than deriving one from the other",
			chunk.MaxTransferBytes, DefaultMaxChunkBufferBytes)
	}
}

// TestFlushAsyncBatch_NilClientDoesNotPanic covers the typed-nil trap: handing a
// nil *shm.Client to an interface parameter yields a NON-nil interface, so the
// guard has to live at the concrete-type boundary in FlushAsyncBatch.
func TestFlushAsyncBatch_NilClientDoesNotPanic(t *testing.T) {
	FlushAsyncBatch([]PendingAsyncResult{strResult("a", 4)}, nil)
}
