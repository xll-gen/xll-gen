package server

import (
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"
	shm "github.com/xll-gen/shm/go"
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

// TestSendAckOrChunk_SmallRespBufChunks is a regression for the hardcoded
// chunk-size bug: when respBuf is smaller than DefaultChunkSize+overhead, the
// old code built a ~950 KiB chunk that overflowed respBuf and bailed via the
// framing check, returning (0, 0) and losing the transfer. With ChunkBudget
// the first chunk is sized to fit respBuf, so the call succeeds and the result
// fits the buffer.
func TestSendAckOrChunk_SmallRespBufChunks(t *testing.T) {
	cm := NewChunkManager()
	defer cm.Close()
	b := flatbuffers.NewBuilder(0)

	respBuf := make([]byte, 4096)
	// Payload far larger than respBuf forces the chunking path.
	payload := make([]byte, 64*1024)
	for i := range payload {
		payload[i] = byte(i)
	}

	n, mt := SendAckOrChunk(payload, respBuf, shm.MsgType(MsgChunk), cm, b)

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
