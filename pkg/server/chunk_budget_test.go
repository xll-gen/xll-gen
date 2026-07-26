package server

import (
	"bytes"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"
	shm "github.com/xll-gen/shm/go"
	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/pkg/chunk"
)

// TestChunkBudget verifies the per-call chunk size is derived from the real
// respBuf, capped at DefaultChunkSize, and floored at 1.
func TestChunkBudget(t *testing.T) {
	cases := []struct {
		name    string
		respLen int
		want    int
	}{
		{"large slot caps at default", DefaultChunkSize + ChunkFramingOverhead + 10_000, DefaultChunkSize},
		{"exactly default after overhead", DefaultChunkSize + ChunkFramingOverhead, DefaultChunkSize},
		{"real 512 KiB half-slot", chunk.HalfSlotSize, DefaultChunkSize},
		{"small slot scales down", 4096, 4096 - ChunkFramingOverhead},
		{"tiny slot floors at 1", ChunkFramingOverhead - 5, 1},
		{"zero respBuf floors at 1", 0, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ChunkBudget(make([]byte, c.respLen))
			if got != c.want {
				t.Fatalf("ChunkBudget(len=%d) = %d, want %d", c.respLen, got, c.want)
			}
		})
	}
}

// TestChunkBudgetFitsRealRequestBuffer is the invariant that the P0 defect
// violated: the guest->host chunk budget must never exceed what shm will accept
// in one slot. shm rejects `len(data) > len(slot.reqBuffer)` outright, and the
// request buffer is only HALF the configured slot payload.
//
// Before the fix DefaultChunkSize was 950 KiB against a 512 KiB request buffer
// (1.86x over) and this assertion failed, i.e. every chunk frame was rejected.
func TestChunkBudgetFitsRealRequestBuffer(t *testing.T) {
	const realRequestBuffer = chunk.HalfSlotSize // 1 MiB payloadSize / 2
	if DefaultChunkSize+ChunkFramingOverhead > realRequestBuffer {
		t.Fatalf("DefaultChunkSize=%d + framing=%d exceeds the real %d-byte request buffer: every guest->host chunk frame would be rejected with \"data too large\"",
			DefaultChunkSize, ChunkFramingOverhead, realRequestBuffer)
	}
}

// TestSendAckOrChunk_OversizedResponseIsExplicitError is the defect-B
// regression. The host->guest chunk transport is ACK-pull and the C++ host has
// NO MSG_ACK sender, so handing back a protocol.Chunk frame in a Response slot
// made the generated wrapper's unconditional
// `GetRoot<ipc::XResponse>(slot.GetRespBuffer())` read a Chunk frame through the
// Response vtable — silent misparse.
//
// SendAckOrChunk must therefore refuse an oversized payload with
// shm.MsgTypeSystemError (which shm's C++ SlotHandle::Send turns into
// Error::InternalError -> the wrapper's res.HasError() cell error) and must NOT
// write anything into respBuf or register an outgoing chunk.
func TestSendAckOrChunk_OversizedResponseIsExplicitError(t *testing.T) {
	cm := NewChunkManager()
	defer cm.Close()
	b := flatbuffers.NewBuilder(0)

	respBuf := make([]byte, 4096)
	payload := make([]byte, 64*1024) // far larger than respBuf
	for i := range payload {
		payload[i] = 0x5A
	}

	n, mt := SendAckOrChunk(payload, respBuf, shm.MsgType(MsgUserStart), cm, b)

	if mt != shm.MsgTypeSystemError {
		t.Fatalf("oversized response msgType = %d, want shm.MsgTypeSystemError (%d)", mt, shm.MsgTypeSystemError)
	}
	if n != 0 {
		t.Fatalf("oversized response size = %d, want 0 (nothing may be published in the Response slot)", n)
	}
	// No chunk frame may be emitted: the peer cannot consume one.
	for i, v := range respBuf {
		if v != 0 {
			t.Fatalf("respBuf must be left untouched; byte %d = %#x (a chunk frame was written into a Response slot)", i, v)
		}
	}
	cm.outgoingMutex.Lock()
	residue := len(cm.outgoingChunks)
	cm.outgoingMutex.Unlock()
	if residue != 0 {
		t.Fatalf("refused response must not register an OutgoingChunk; got %d entries", residue)
	}
}

// TestSendAckOrChunk_FittingResponsePassesThrough guards the happy path the
// defect-B change must not disturb.
func TestSendAckOrChunk_FittingResponsePassesThrough(t *testing.T) {
	cm := NewChunkManager()
	defer cm.Close()
	b := flatbuffers.NewBuilder(0)

	respBuf := make([]byte, 4096)
	payload := []byte("a fitting response")

	n, mt := SendAckOrChunk(payload, respBuf, shm.MsgType(MsgUserStart), cm, b)
	if int(n) != len(payload) {
		t.Fatalf("size = %d, want %d", n, len(payload))
	}
	if mt != shm.MsgType(MsgUserStart) {
		t.Fatalf("msgType = %d, want %d (caller's type must be preserved)", mt, MsgUserStart)
	}
	if string(respBuf[:len(payload)]) != string(payload) {
		t.Fatalf("respBuf = %q, want %q", respBuf[:len(payload)], payload)
	}
}

// TestRegisterAckPullChunk_SmallRespBufChunks is a regression for the hardcoded
// chunk-size bug in the RETAINED ACK-pull transport: when respBuf is smaller
// than DefaultChunkSize+overhead, the old code built a ~950 KiB chunk that
// overflowed respBuf and bailed via the framing check, returning (0, 0) and
// losing the transfer. With ChunkBudget the first chunk is sized to fit respBuf,
// so the call succeeds and the result fits the buffer.
//
// registerAckPullChunk is no longer reachable from SendAckOrChunk (defect B —
// no C++ MSG_ACK consumer exists), but the transport and this invariant are kept
// for the day one lands.
func TestRegisterAckPullChunk_SmallRespBufChunks(t *testing.T) {
	cm := NewChunkManager()
	defer cm.Close()
	b := flatbuffers.NewBuilder(0)

	respBuf := make([]byte, 4096)
	// Payload far larger than respBuf forces the chunking path.
	payload := make([]byte, 64*1024)
	for i := range payload {
		payload[i] = byte(i)
	}

	n, mt := registerAckPullChunk(payload, respBuf, shm.MsgType(MsgChunk), cm, b)

	if mt != shm.MsgType(MsgChunk) {
		t.Fatalf("expected MsgChunk, got msgType=%d (n=%d) — small respBuf likely hit the framing-overflow bail", mt, n)
	}
	if n <= 0 {
		t.Fatalf("expected a positive chunk length, got %d", n)
	}
	if int(n) > len(respBuf) {
		t.Fatalf("chunk response length %d exceeds respBuf %d", n, len(respBuf))
	}

	// The first chunk must advance Offset by the respBuf-derived budget so the
	// resend path (HandleAck, which also calls ChunkBudget(respBuf)) slices
	// contiguously.
	wantBudget := ChunkBudget(respBuf)
	_, _, totalSize, offset, found := cm.GetNextChunk(firstTransferID(t, cm), wantBudget)
	if !found {
		t.Fatal("expected an outgoing chunk to be registered")
	}
	if totalSize != len(payload) {
		t.Fatalf("totalSize = %d, want %d", totalSize, len(payload))
	}
	// offset returned by GetNextChunk is the offset of THIS (second) chunk,
	// i.e. the end of the first chunk == the first chunk's budget.
	if offset != wantBudget {
		t.Fatalf("second-chunk offset = %d, want first-chunk budget %d", offset, wantBudget)
	}
}

// TestRegisterAckPullChunk_FramingOverflowLeavesNoResidue is a regression for
// the backstop failure path: when the framed first chunk cannot fit respBuf,
// registerAckPullChunk returns (0, 0) but must NOT leave the just-registered
// OutgoingChunk in the map (previously it lingered until the TTL sweep even
// though nothing would ever advance or ACK it).
func TestRegisterAckPullChunk_FramingOverflowLeavesNoResidue(t *testing.T) {
	cm := NewChunkManager()
	defer cm.Close()
	b := flatbuffers.NewBuilder(0)

	// respBuf smaller than the framing overhead: ChunkBudget floors at 1, so a
	// 1-byte chunk still gets framed, and the frame is far larger than respBuf,
	// forcing the len(chunkPayload) > len(respBuf) backstop.
	respBuf := make([]byte, 8)
	payload := make([]byte, 64) // larger than respBuf -> chunking path

	n, mt := registerAckPullChunk(payload, respBuf, shm.MsgType(MsgChunk), cm, b)
	if n != 0 || mt != 0 {
		t.Fatalf("expected framing-overflow backstop to return (0, 0); got (%d, %d)", n, mt)
	}

	cm.outgoingMutex.Lock()
	residue := len(cm.outgoingChunks)
	cm.outgoingMutex.Unlock()
	if residue != 0 {
		t.Fatalf("failed-transfer OutgoingChunk left in map: got %d entries, want 0", residue)
	}
}

// firstTransferID returns the id of the single outgoing chunk registered in cm.
func firstTransferID(t *testing.T, cm *ChunkManager) uint64 {
	t.Helper()
	cm.outgoingMutex.Lock()
	defer cm.outgoingMutex.Unlock()
	if len(cm.outgoingChunks) != 1 {
		t.Fatalf("expected exactly 1 outgoing chunk, got %d", len(cm.outgoingChunks))
	}
	for id := range cm.outgoingChunks {
		return id
	}
	return 0
}

// TestSendAckOrChunk_CalcEndOversizedCannotMisparse is the most dangerous
// instance of defect B, called out separately because the consequence is a wild
// pointer dereference rather than a wrong scalar.
//
// protocol.fbs gives BOTH tables field 0, i.e. the SAME vtable slot:
//
//	table Chunk                     { id: ulong;                    ... }
//	table CalculationEndedResponse  { commands: [CommandWrapper];        }
//
// and the C++ consumer (internal/assets/files/src/xll_events.cpp) does
//
//	auto res = g_host.Send(nullptr, 0, MSG_CALCULATION_ENDED, respBuf, 2000);
//	if (!res.HasError() && res.Value() > 0) {
//	    auto root = flatbuffers::GetRoot<protocol::CalculationEndedResponse>(respBuf.data());
//	    auto commands = root->commands();          // <-- reads Chunk.id as a vector offset
//	    haveCommands = commands && commands->size() > 0;
//	}
//
// with NO check of the response msgType. Had a Chunk frame ever landed in that
// response slot, root->commands() would resolve Chunk.id's low bytes as a
// relative vector offset and commands->size() would read unmapped memory —
// FlatBuffers C++ GetRoot is unchecked. (The Go reader below shows the same
// misread as a slice-bounds panic with an absurd index, which is exactly the
// address C++ would have dereferenced.)
//
// SendAckOrChunk therefore MUST refuse, so res.HasError() is true and the
// GetRoot never runs.
func TestSendAckOrChunk_CalcEndOversizedCannotMisparse(t *testing.T) {
	// First, prove the vtable collision is real rather than assumed: parsing a
	// genuine Chunk frame as a CalculationEndedResponse must NOT yield a clean
	// empty commands vector. Anything else (garbage length or a bounds panic)
	// confirms the misparse hazard this test exists to keep unreachable.
	t.Run("VtableSlotCollisionIsReal", func(t *testing.T) {
		b := flatbuffers.NewBuilder(0)
		frame := chunk.BuildFrame(b, []byte("chunk payload"), 0xDEADBEEFCAFE1234, 999999, 0, uint32(MsgCalculationEnded))
		misread := func() (n int, panicked bool) {
			defer func() {
				if r := recover(); r != nil {
					panicked = true
				}
			}()
			return protocol.GetRootAsCalculationEndedResponse(frame, 0).CommandsLength(), false
		}
		n, panicked := misread()
		if !panicked && n == 0 {
			t.Fatalf("expected a Chunk frame to misparse as CalculationEndedResponse (shared vtable slot 4); got a clean empty commands vector — has protocol.fbs field ordering changed?")
		}
		t.Logf("Chunk frame misparsed as CalculationEndedResponse: commandsLength=%d panicked=%v (C++ GetRoot is unchecked and would deref this)", n, panicked)
	})

	// Now the production guarantee: an oversized calc-end response is refused
	// with a system error and no chunk frame reaches the response slot.
	t.Run("OversizedCalcEndIsRefused", func(t *testing.T) {
		cm := NewChunkManager()
		defer cm.Close()
		b := flatbuffers.NewBuilder(0)

		// A real CalculationEndedResponse payload, deliberately larger than the
		// response buffer handed to us.
		payload := buildOversizedCalcEndResponse(t, b, 32*1024)
		respBuf := make([]byte, 4096)
		if len(payload) <= len(respBuf) {
			t.Fatalf("setup: payload %d must exceed respBuf %d", len(payload), len(respBuf))
		}

		n, mt := SendAckOrChunk(payload, respBuf, shm.MsgType(MsgCalculationEnded), cm, b)
		if mt != shm.MsgTypeSystemError {
			t.Fatalf("msgType = %d, want shm.MsgTypeSystemError (%d): a Chunk frame in the calc-end response slot is a wild-pointer hazard", mt, shm.MsgTypeSystemError)
		}
		if n != 0 {
			t.Fatalf("size = %d, want 0", n)
		}
		if i := bytes.Index(respBuf, []byte("XCHN")); i >= 0 {
			t.Fatalf("a Chunk frame was written into the calc-end response slot (\"XCHN\" at byte %d)", i)
		}
		cm.outgoingMutex.Lock()
		residue := len(cm.outgoingChunks)
		cm.outgoingMutex.Unlock()
		if residue != 0 {
			t.Fatalf("refused calc-end response must not register an OutgoingChunk; got %d", residue)
		}
	})
}

// buildOversizedCalcEndResponse serializes a CalculationEndedResponse whose
// encoded size exceeds minBytes, so tests exercise the real wire shape instead
// of an opaque blob.
func buildOversizedCalcEndResponse(t *testing.T, b *flatbuffers.Builder, minBytes int) []byte {
	t.Helper()
	for n := 64; ; n *= 2 {
		b.Reset()
		offs := make([]flatbuffers.UOffsetT, n)
		for i := range offs {
			fmtStr := b.CreateString("#,##0.00_);[Red](#,##0.00) -- padding to grow the payload")
			protocol.FormatCommandStart(b)
			protocol.FormatCommandAddFormat(b, fmtStr)
			cmd := protocol.FormatCommandEnd(b)
			protocol.CommandWrapperStart(b)
			protocol.CommandWrapperAddCmdType(b, protocol.CommandFormatCommand)
			protocol.CommandWrapperAddCmd(b, cmd)
			offs[i] = protocol.CommandWrapperEnd(b)
		}
		protocol.CalculationEndedResponseStartCommandsVector(b, len(offs))
		for i := len(offs) - 1; i >= 0; i-- {
			b.PrependUOffsetT(offs[i])
		}
		vec := b.EndVector(len(offs))
		protocol.CalculationEndedResponseStart(b)
		protocol.CalculationEndedResponseAddCommands(b, vec)
		b.Finish(protocol.CalculationEndedResponseEnd(b))
		if out := b.FinishedBytes(); len(out) > minBytes {
			dup := make([]byte, len(out))
			copy(dup, out)
			return dup
		}
	}
}
