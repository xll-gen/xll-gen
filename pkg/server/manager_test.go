package server

import (
	"bytes"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	shm "github.com/xll-gen/shm/go"
	"github.com/xll-gen/types/go/protocol"
)

// mustGetChunkBuffer is a test helper: GetChunkBuffer now returns an error
// when the wire-supplied total is non-positive or exceeds
// MaxChunkBufferBytes (DoS guard). For tests using legitimate sizes, the
// error is impossible — fail the test loudly if it ever fires.
func mustGetChunkBuffer(t *testing.T, cm *ChunkManager, id uint64, total int) *ChunkBuffer {
	t.Helper()
	buf, err := cm.GetChunkBuffer(id, total)
	if err != nil {
		t.Fatalf("GetChunkBuffer(id=%#x, total=%d) unexpected error: %v", id, total, err)
	}
	return buf
}

// buildChunkRequest builds a flatbuffer-encoded Chunk request payload, the
// same shape that HandleChunk consumes off the wire.
func buildChunkRequest(t *testing.T, id uint64, totalSize uint32, offset uint32, data []byte, msgType uint32) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(1024)
	dataOff := b.CreateByteVector(data)
	protocol.ChunkStart(b)
	protocol.ChunkAddId(b, id)
	protocol.ChunkAddTotalSize(b, totalSize)
	protocol.ChunkAddOffset(b, offset)
	protocol.ChunkAddData(b, dataOff)
	protocol.ChunkAddMsgType(b, msgType)
	root := protocol.ChunkEnd(b)
	b.Finish(root)
	out := make([]byte, len(b.FinishedBytes()))
	copy(out, b.FinishedBytes())
	return out
}

// TestChunkManager exercises the four standard concurrency edge cases for
// chunk reassembly per AGENTS.md §23.3: timeout, partial chunk arrival,
// duplicate chunk, oversized payload.
func TestChunkManager(t *testing.T) {
	t.Run("Timeout_StaleBufferEvictedAndLateChunkStartsFresh", func(t *testing.T) {
		cm := NewChunkManager()

		const id uint64 = 0xDEADBEEF
		const total = 16
		buf := mustGetChunkBuffer(t, cm, id, total)
		// Write the first half.
		buf.Mutex.Lock()
		copy(buf.Data[0:8], []byte("AAAAAAAA"))
		buf.Received += 8
		// Force LastAccess into the past so the next cleanup sweep evicts it.
		buf.LastAccess = time.Now().Add(-2 * time.Minute)
		buf.Mutex.Unlock()

		// Manually re-stamp the map entry's LastAccess by re-locking the chunk
		// mutex: in production this would have been written under chunkMutex
		// in GetChunkBuffer; we already mutated the struct above and the
		// pointer is shared, so the map entry sees the stale time.
		cm.chunkMutex.Lock()
		cm.chunkCache[id].LastAccess = time.Now().Add(-2 * time.Minute)
		cm.chunkMutex.Unlock()

		// One synchronous cleanup pass with a 60s TTL == production behavior.
		cm.runCleanupOnce(time.Now(), 60*time.Second)

		cm.chunkMutex.Lock()
		_, stillThere := cm.chunkCache[id]
		cm.chunkMutex.Unlock()
		if stillThere {
			t.Fatalf("expected stale buffer for id=%#x to be evicted after TTL, but it remained in chunkCache", id)
		}

		// A late-arriving chunk for the same id must NOT see the old partial
		// data: GetChunkBuffer must allocate a fresh buffer.
		buf2 := mustGetChunkBuffer(t, cm, id, total)
		if buf2 == buf {
			t.Fatalf("expected fresh ChunkBuffer pointer after eviction; got same pointer back")
		}
		if buf2.Received != 0 {
			t.Fatalf("fresh buffer must have Received=0; got %d", buf2.Received)
		}
		if !bytes.Equal(buf2.Data, make([]byte, total)) {
			t.Fatalf("fresh buffer must be zeroed; got %v", buf2.Data)
		}
	})

	t.Run("Timeout_OutgoingChunkEvicted", func(t *testing.T) {
		cm := NewChunkManager()
		const id uint64 = 0xABCD1234
		cm.AddOutgoingChunk(id, &OutgoingChunk{
			Data:       []byte("hello world"),
			Id:         id,
			MsgType:    MsgChunk,
			LastAccess: time.Now().Add(-2 * time.Minute),
		})

		cm.runCleanupOnce(time.Now(), 60*time.Second)

		_, _, _, _, found := cm.GetNextChunk(id, 1024)
		if found {
			t.Fatalf("expected outgoing chunk for id=%#x to be evicted after TTL", id)
		}
	})

	t.Run("PartialChunkArrival_NoCompletionFires", func(t *testing.T) {
		// Chunks 1, 2, 4 arrive (chunk 3 is skipped). Completion must NOT
		// fire (Received < TotalSize). The defensive offset+len bounds check
		// is load-bearing and remains intact (see AGENTS.md §23, Cache
		// Visibility Discipline).
		cm := NewChunkManager()
		const id uint64 = 1
		const total = 40 // four 10-byte chunks
		buf := mustGetChunkBuffer(t, cm, id, total)

		// Helper that mirrors what HandleChunk does, minus the dispatch:
		// copy under buf.Mutex, advance Received by the byte count, then
		// check completion.
		write := func(offset int, payload []byte) (complete bool) {
			buf.Mutex.Lock()
			defer buf.Mutex.Unlock()
			if offset+len(payload) <= len(buf.Data) {
				copy(buf.Data[offset:], payload)
				buf.Received += len(payload)
			}
			return buf.Received >= buf.TotalSize
		}

		cases := []struct {
			name     string
			offset   int
			payload  []byte
			complete bool
		}{
			{"chunk1", 0, bytes.Repeat([]byte{0x01}, 10), false},
			{"chunk2", 10, bytes.Repeat([]byte{0x02}, 10), false},
			// chunk3 skipped intentionally
			{"chunk4", 30, bytes.Repeat([]byte{0x04}, 10), false},
		}
		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				got := write(tc.offset, tc.payload)
				if got != tc.complete {
					t.Fatalf("complete=%v want %v after writing %s", got, tc.complete, tc.name)
				}
			})
		}

		// Received tracks bytes, not chunks: 30 bytes written, TotalSize 40.
		buf.Mutex.Lock()
		recv := buf.Received
		buf.Mutex.Unlock()
		if recv >= total {
			t.Fatalf("partial set must NOT report complete: Received=%d TotalSize=%d", recv, total)
		}

		// Buffer must still be in the cache awaiting the missing chunk.
		cm.chunkMutex.Lock()
		_, stillThere := cm.chunkCache[id]
		cm.chunkMutex.Unlock()
		if !stillThere {
			t.Fatalf("partial buffer must remain in chunkCache until completion or TTL")
		}

		// Now the actual chunk 3 arrives at offset 20.
		complete := write(20, bytes.Repeat([]byte{0x03}, 10))
		if !complete {
			t.Fatalf("expected completion after final chunk; Received=%d TotalSize=%d", buf.Received, total)
		}

		// Validate reassembled payload integrity.
		want := append(append(append(bytes.Repeat([]byte{0x01}, 10),
			bytes.Repeat([]byte{0x02}, 10)...),
			bytes.Repeat([]byte{0x03}, 10)...),
			bytes.Repeat([]byte{0x04}, 10)...)
		if !bytes.Equal(buf.Data, want) {
			t.Fatalf("reassembled payload mismatch.\n got %v\nwant %v", buf.Data, want)
		}
	})

	t.Run("DuplicateChunk_IdempotentReceive", func(t *testing.T) {
		// Regression for the data-corruption bug previously documented
		// as DuplicateChunk_ReceivedCounterDoubleCountedFinding: a
		// retransmit of chunk1 in a 2-chunk message must NOT advance
		// Received (which would push it past TotalSize and trigger
		// premature completion with chunk2 still unwritten).
		//
		// Invariants after the dedup fix (AGENTS.md §23.3):
		//   (a) duplicate arrival must NOT trigger completion;
		//   (b) buf.Received tracks unique-chunk bytes only;
		//   (c) the buffer for the full {chunk1, dup-chunk1, chunk2}
		//       sequence must be byte-identical to {chunk1, chunk2}.
		cm := NewChunkManager()
		h := &SystemHandler{ChunkManager: cm}

		const id uint64 = 2
		const totalU32 uint32 = 20 // two 10-byte chunks

		first := bytes.Repeat([]byte{0xAA}, 10)
		second := bytes.Repeat([]byte{0xBB}, 10)

		respBuf := make([]byte, 4096)
		b := flatbuffers.NewBuilder(1024)

		var dispatched bool
		var dispatchedData []byte
		dispatch := func(data []byte, _ []byte, _ shm.MsgType) (int32, shm.MsgType) {
			dispatched = true
			dispatchedData = append([]byte(nil), data...)
			return 0, 0
		}

		// First chunk1 arrival.
		chunk1Bytes := buildChunkRequest(t, id, totalU32, 0, first, 0)
		_, _ = h.HandleChunk(chunk1Bytes, respBuf, b, dispatch)
		if dispatched {
			t.Fatalf("dispatch fired after only chunk1; want completion only after both chunks")
		}

		// Duplicate of chunk1.
		dupBytes := buildChunkRequest(t, id, totalU32, 0, first, 0)
		_, _ = h.HandleChunk(dupBytes, respBuf, b, dispatch)
		if dispatched {
			t.Fatalf("duplicate chunk1 triggered premature completion — dedup-by-offset broken")
		}

		// Inspect manager state: Received must reflect ONE chunk, not two.
		cm.chunkMutex.Lock()
		buf := cm.chunkCache[id]
		cm.chunkMutex.Unlock()
		if buf == nil {
			t.Fatalf("buffer for id=%#x evicted unexpectedly after duplicate", id)
		}
		buf.Mutex.Lock()
		recvAfterDup := buf.Received
		buf.Mutex.Unlock()
		if recvAfterDup != 10 {
			t.Fatalf("after duplicate, Received must equal one chunk (10); got %d", recvAfterDup)
		}

		// Now chunk2 arrives → completion.
		chunk2Bytes := buildChunkRequest(t, id, totalU32, 10, second, 0)
		_, _ = h.HandleChunk(chunk2Bytes, respBuf, b, dispatch)
		if !dispatched {
			t.Fatalf("expected dispatch after final chunk; not fired")
		}

		// Verify byte-identical reassembly.
		want := append(append([]byte{}, first...), second...)
		if !bytes.Equal(dispatchedData, want) {
			t.Fatalf("reassembled payload mismatch after duplicate replay.\n got %v\nwant %v", dispatchedData, want)
		}
	})

	t.Run("OversizedTotal_AllocationRejected", func(t *testing.T) {
		// DoS guard: wire-supplied total is attacker-controllable.
		// GetChunkBuffer must refuse to allocate when total exceeds
		// MaxChunkBufferBytes, MUST NOT insert anything into chunkCache,
		// and the handler path must return MsgTypeSystemError.
		cm := NewChunkManager()

		const id uint64 = 0xBADCAFE
		const oversized = int(1 << 40) // 1 TiB request

		// Direct API check.
		buf, err := cm.GetChunkBuffer(id, oversized)
		if err == nil {
			t.Fatalf("GetChunkBuffer(total=%d) must refuse; got buf=%v", oversized, buf)
		}
		if buf != nil {
			t.Fatalf("on refusal, buffer pointer must be nil; got %p", buf)
		}
		cm.chunkMutex.Lock()
		_, inserted := cm.chunkCache[id]
		cm.chunkMutex.Unlock()
		if inserted {
			t.Fatalf("refused allocation must NOT leave a buffer in chunkCache (DoS would still land)")
		}

		// Wire-path check via HandleChunk: returned (size, msgType) must
		// signal SystemError, and still nothing inserted.
		h := &SystemHandler{ChunkManager: cm}
		// Use a small data payload but a total declared at 2 GiB which
		// also exceeds the default 256 MiB cap. The maximum the wire
		// allows is uint32 (~4 GiB).
		const wireTotal uint32 = 2 * 1024 * 1024 * 1024
		oversizedChunk := buildChunkRequest(t, id, wireTotal, 0, []byte{0x01, 0x02}, 0)
		respBuf := make([]byte, 4096)
		b := flatbuffers.NewBuilder(1024)
		dispatch := func([]byte, []byte, shm.MsgType) (int32, shm.MsgType) {
			t.Fatalf("dispatch must not fire when allocation is refused")
			return 0, 0
		}
		size, mt := h.HandleChunk(oversizedChunk, respBuf, b, dispatch)
		if size != 0 || mt != shm.MsgTypeSystemError {
			t.Fatalf("HandleChunk must return (0, MsgTypeSystemError) on oversized total; got (%d, %d)", size, mt)
		}

		cm.chunkMutex.Lock()
		_, inserted2 := cm.chunkCache[id]
		cm.chunkMutex.Unlock()
		if inserted2 {
			t.Fatalf("HandleChunk refusal path must NOT leave a buffer in chunkCache")
		}
	})

	t.Run("OversizedTotal_CustomLimitHonored", func(t *testing.T) {
		// A custom-tuned limit must be respected: requests at or below
		// the cap succeed; requests above are refused.
		cm := NewChunkManagerWithMax(1024)
		const id uint64 = 0xC0FFEE

		if _, err := cm.GetChunkBuffer(id, 1024); err != nil {
			t.Fatalf("total=1024 within cap=1024 must succeed; got %v", err)
		}
		// Use a different id so the second call is fresh.
		if _, err := cm.GetChunkBuffer(id+1, 1025); err == nil {
			t.Fatalf("total=1025 over cap=1024 must be refused")
		}
		cm.chunkMutex.Lock()
		_, inserted := cm.chunkCache[id+1]
		cm.chunkMutex.Unlock()
		if inserted {
			t.Fatalf("refused allocation must not leak a chunkCache entry")
		}
	})

	t.Run("AckPullChunk_OffsetPublishedBeforeMapInsert", func(t *testing.T) {
		// NOTE (2026-07-25, defect B): this exercises registerAckPullChunk, the
		// RETAINED host->guest ACK-pull transport. SendAckOrChunk no longer
		// calls it — an oversized response is now refused with
		// shm.MsgTypeSystemError because the C++ host has no MSG_ACK sender, so
		// a chunk frame in a Response slot would be misparsed. The invariant
		// below still guards the transport for the day a C++ consumer lands.
		//
		// Regression for the registerAckPullChunk publication-order bug
		// (AGENTS.md §23.3): if `out.Offset = currentSize` happens
		// AFTER cm.AddOutgoingChunk, a concurrent HandleAck →
		// GetNextChunk can observe Offset==0 and resend the first
		// slice, double-delivering bytes [0,chunkSize) to the consumer.
		//
		// Two assertions hold under the fix:
		//   (a) Post-registerAckPullChunk steady state: the map entry's
		//       Offset must already be chunkSize (the fix sets
		//       Offset on the struct BEFORE publishing the pointer).
		//   (b) Under stress: a tight loop of registerAckPullChunk +
		//       concurrent GetNextChunk must never observe an offset
		//       of 0 on the returned chunk. Under the buggy ordering,
		//       a sufficiently timed race delivers a chunk with
		//       offset=0 even though registerAckPullChunk already returned.
		//
		// chunkSize is derived from respBuf via ChunkBudget — exactly what
		// registerAckPullChunk does — instead of a hardcoded literal that
		// silently diverges when the budget arithmetic changes.
		respBuf := make([]byte, DefaultChunkSize+ChunkFramingOverhead+100*1024)
		chunkSize := ChunkBudget(respBuf)
		payload := bytes.Repeat([]byte{0x5A}, 2*chunkSize+1024)
		if len(payload) <= len(respBuf) {
			t.Fatalf("test setup invariant violated: payload=%d must exceed respBuf=%d to force chunking", len(payload), len(respBuf))
		}

		// (a) Steady-state offset check.
		{
			cm := NewChunkManager()
			b := flatbuffers.NewBuilder(1024)

			size, mt := registerAckPullChunk(payload, respBuf, MsgChunk, cm, b)
			if size == 0 {
				t.Fatalf("registerAckPullChunk returned size=0; want chunked response written")
			}
			if mt != MsgChunk {
				t.Fatalf("expected first response to be MsgChunk; got %d", mt)
			}

			cm.outgoingMutex.Lock()
			var foundOff, n int
			for _, oc := range cm.outgoingChunks {
				foundOff = oc.Offset
				n++
			}
			cm.outgoingMutex.Unlock()
			if n != 1 {
				t.Fatalf("expected exactly one outgoing chunk in flight; got %d", n)
			}
			if foundOff != chunkSize {
				t.Fatalf("after registerAckPullChunk, Offset must already equal chunkSize=%d on the map entry; got %d (Bug 1 — Offset write deferred past publication)", chunkSize, foundOff)
			}
		}

		// (b) Stress: concurrent GetNextChunk racing the publication.
		// 200 iterations is enough on race-instrumented Go to catch
		// the original ordering bug on every architecture we ship to.
		const iters = 200
		for i := 0; i < iters; i++ {
			cm := NewChunkManager()
			localRespBuf := make([]byte, len(respBuf))
			localB := flatbuffers.NewBuilder(1024)

			// Pre-generate a candidate ID list by watching the map.
			// We launch a probe goroutine that, the moment a new
			// outgoing chunk shows up, immediately calls
			// GetNextChunk on it and captures the offset it sees.
			seenOffset := make(chan int, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					cm.outgoingMutex.Lock()
					var foundID uint64
					var have bool
					for id := range cm.outgoingChunks {
						foundID = id
						have = true
						break
					}
					cm.outgoingMutex.Unlock()
					if have {
						_, _, _, off, ok := cm.GetNextChunk(foundID, chunkSize)
						if ok {
							select {
							case seenOffset <- off:
							default:
							}
						}
						return
					}
				}
			}()

			registerAckPullChunk(payload, localRespBuf, MsgChunk, cm, localB)
			<-done
			select {
			case off := <-seenOffset:
				if off == 0 {
					t.Fatalf("iter %d: concurrent GetNextChunk observed Offset=0 — Bug 1 (publication-order) reproduced", i)
				}
			default:
				// Probe didn't catch a chunk in time; that's fine,
				// the steady-state assertion (a) already covers
				// the happy path.
			}
		}
	})

	t.Run("OversizedPayload_BoundsCheckRejectsOOBWrite", func(t *testing.T) {
		// The wire claims TotalSize=N but a chunk arrives with offset+len > N.
		// HandleChunk's defensive `offset+dataLen <= len(buf.Data)` (load-
		// bearing per AGENTS.md cache-visibility policy) drops the OOB write
		// silently. We replicate that semantic here and assert nothing was
		// written and Received was not advanced.
		cm := NewChunkManager()
		const id uint64 = 3
		const total = 32
		buf := mustGetChunkBuffer(t, cm, id, total)

		write := func(offset int, payload []byte) (accepted bool) {
			buf.Mutex.Lock()
			defer buf.Mutex.Unlock()
			if offset+len(payload) <= len(buf.Data) {
				copy(buf.Data[offset:], payload)
				buf.Received += len(payload)
				return true
			}
			return false
		}

		// Oversized: offset 16 with 32 bytes payload → 16+32=48 > 32.
		oversized := bytes.Repeat([]byte{0xEE}, 32)
		if write(16, oversized) {
			t.Fatalf("oversized write at offset=16 len=32 (buf=32) must be rejected by bounds check")
		}

		buf.Mutex.Lock()
		recv := buf.Received
		dataCopy := append([]byte(nil), buf.Data...)
		buf.Mutex.Unlock()

		if recv != 0 {
			t.Fatalf("Received must remain 0 after rejected oversized write; got %d", recv)
		}
		if !bytes.Equal(dataCopy, make([]byte, total)) {
			t.Fatalf("buf.Data must remain zeroed after rejected oversized write; got %v", dataCopy)
		}
	})

	t.Run("OversizedPayload_HandleChunkWirePath", func(t *testing.T) {
		// Same edge case but exercised through HandleChunk so that the
		// FlatBuffers Chunk decode path is covered. A chunk claiming
		// offset+len > TotalSize is a protocol violation: the transfer can
		// never be completed correctly, so it must be REFUSED with
		// MsgTypeSystemError and the partial buffer dropped.
		//
		// CONTRACT CHANGE (chunk hardening): this used to assert the buffer was
		// "retained for retry" and the chunk silently dropped. There is no
		// legitimate retry for an out-of-bounds range — the producer would be
		// acked as if all were well and keep pushing until the 60s TTL — and
		// the C++ mirror (xll_worker.cpp) already erased the partial message on
		// the same condition. Both sides now drop + report, matching shm
		// SPECIFICATION.md §3.3.4.
		cm := NewChunkManager()
		h := &SystemHandler{ChunkManager: cm}

		const id uint64 = 4
		const total uint32 = 16

		// First seed a buffer so GetChunkBuffer doesn't allocate based on a
		// later malicious total.
		_ = mustGetChunkBuffer(t, cm, id, int(total))

		oversized := buildChunkRequest(t, id, total, 8, bytes.Repeat([]byte{0xCD}, 32), 0)
		respBuf := make([]byte, 4096)
		b := flatbuffers.NewBuilder(1024)
		dispatched := false
		dispatch := func([]byte, []byte, shm.MsgType) (int32, shm.MsgType) {
			dispatched = true
			return 0, 0
		}
		size, mt := h.HandleChunk(oversized, respBuf, b, dispatch)
		if dispatched {
			t.Fatalf("oversized chunk must not trigger completion dispatch")
		}
		if size != 0 || mt != shm.MsgTypeSystemError {
			t.Fatalf("HandleChunk must return (0, MsgTypeSystemError) for an out-of-bounds chunk; got (%d, %d)", size, mt)
		}

		cm.chunkMutex.Lock()
		_, stillThere := cm.chunkCache[id]
		cm.chunkMutex.Unlock()
		if stillThere {
			t.Fatalf("out-of-bounds chunk must drop the transfer; buffer for id=%#x still in chunkCache", id)
		}
	})

	t.Run("OverlappingChunks_TransferRejected", func(t *testing.T) {
		// THE zero-fill hole. total=100 with (off=0,len=60) then (off=50,len=40):
		// two DISTINCT offsets, so the old offset-only dedup accepted both, and
		// Received reached 60+40 = 100 >= TotalSize → COMPLETE. But [90,100)
		// was never written by anyone, so the consumer would have read 10 bytes
		// of zero-fill as if they were payload.
		//
		// Under the coverage contract the second chunk overlaps [50,60) and is
		// refused with MsgTypeSystemError, and the whole transfer is dropped.
		cm := NewChunkManager()
		h := &SystemHandler{ChunkManager: cm}

		const id uint64 = 0x0FF5E7
		const total uint32 = 100

		respBuf := make([]byte, 4096)
		b := flatbuffers.NewBuilder(1024)
		dispatch := func([]byte, []byte, shm.MsgType) (int32, shm.MsgType) {
			t.Fatalf("overlapping chunks must never reach completion dispatch (the zero-fill hole)")
			return 0, 0
		}

		first := buildChunkRequest(t, id, total, 0, bytes.Repeat([]byte{0xA1}, 60), 0)
		if size, mt := h.HandleChunk(first, respBuf, b, dispatch); mt == shm.MsgTypeSystemError {
			t.Fatalf("first (well-formed) chunk must be accepted; got (%d, %d)", size, mt)
		}

		overlapping := buildChunkRequest(t, id, total, 50, bytes.Repeat([]byte{0xA2}, 40), 0)
		size, mt := h.HandleChunk(overlapping, respBuf, b, dispatch)
		if size != 0 || mt != shm.MsgTypeSystemError {
			t.Fatalf("overlapping chunk must return (0, MsgTypeSystemError); got (%d, %d)", size, mt)
		}

		cm.chunkMutex.Lock()
		_, stillThere := cm.chunkCache[id]
		cm.chunkMutex.Unlock()
		if stillThere {
			t.Fatalf("overlap must drop the whole transfer; buffer for id=%#x still in chunkCache", id)
		}
	})

	t.Run("SameOffsetDifferentLength_TransferRejected", func(t *testing.T) {
		// A producer that re-chunks mid-transfer resends the same START offset
		// with a different length. That is NOT the benign retransmit the dedup
		// path exists for: which bytes end up covered changes. Refuse it.
		cm := NewChunkManager()
		h := &SystemHandler{ChunkManager: cm}

		const id uint64 = 0x5A3E
		const total uint32 = 40

		respBuf := make([]byte, 4096)
		b := flatbuffers.NewBuilder(1024)
		dispatch := func([]byte, []byte, shm.MsgType) (int32, shm.MsgType) {
			t.Fatalf("re-chunked transfer must not dispatch")
			return 0, 0
		}

		_, _ = h.HandleChunk(buildChunkRequest(t, id, total, 0, bytes.Repeat([]byte{0x01}, 10), 0), respBuf, b, dispatch)
		size, mt := h.HandleChunk(buildChunkRequest(t, id, total, 0, bytes.Repeat([]byte{0x02}, 20), 0), respBuf, b, dispatch)
		if size != 0 || mt != shm.MsgTypeSystemError {
			t.Fatalf("same offset with a different length must return (0, MsgTypeSystemError); got (%d, %d)", size, mt)
		}
	})

	t.Run("AdjacentChunks_OutOfOrder_Complete", func(t *testing.T) {
		// Control for the two rejection cases above: exactly-abutting ranges
		// delivered out of order are NOT overlaps and must complete normally,
		// with Received == TotalSize reached exactly.
		cm := NewChunkManager()
		h := &SystemHandler{ChunkManager: cm}

		const id uint64 = 0xAD3ACE
		const total uint32 = 30

		respBuf := make([]byte, 4096)
		b := flatbuffers.NewBuilder(1024)
		var got []byte
		dispatch := func(data []byte, _ []byte, _ shm.MsgType) (int32, shm.MsgType) {
			got = append([]byte(nil), data...)
			return 0, 0
		}

		// Arrive 20, 0, 10.
		_, _ = h.HandleChunk(buildChunkRequest(t, id, total, 20, bytes.Repeat([]byte{0x03}, 10), 0), respBuf, b, dispatch)
		_, _ = h.HandleChunk(buildChunkRequest(t, id, total, 0, bytes.Repeat([]byte{0x01}, 10), 0), respBuf, b, dispatch)
		if got != nil {
			t.Fatalf("dispatch fired before full coverage")
		}
		_, _ = h.HandleChunk(buildChunkRequest(t, id, total, 10, bytes.Repeat([]byte{0x02}, 10), 0), respBuf, b, dispatch)

		want := append(append(bytes.Repeat([]byte{0x01}, 10), bytes.Repeat([]byte{0x02}, 10)...), bytes.Repeat([]byte{0x03}, 10)...)
		if !bytes.Equal(got, want) {
			t.Fatalf("out-of-order reassembly mismatch.\n got %v\nwant %v", got, want)
		}
	})

	t.Run("MaxConcurrentTransfers_PruneThenRefuse", func(t *testing.T) {
		// Aggregate resource bound: MaxChunkBufferBytes caps ONE transfer, so
		// without a count bound N transfers x 256 MiB is unbounded. At the cap
		// the manager prunes stale buffers and, if that frees nothing, refuses
		// the NEW transfer (nothing inserted) so the wire sees SystemError.
		const cap = 8
		cm := NewChunkManagerFromConfig(ChunkManagerConfig{MaxConcurrentTransfers: cap})
		defer cm.Close()

		for i := 0; i < cap; i++ {
			if _, err := cm.GetChunkBuffer(uint64(0x1000+i), 64); err != nil {
				t.Fatalf("transfer %d within cap=%d must be admitted; got %v", i, cap, err)
			}
		}
		if _, err := cm.GetChunkBuffer(0x2000, 64); err == nil {
			t.Fatalf("transfer %d must be refused at cap=%d", cap+1, cap)
		}
		cm.chunkMutex.Lock()
		_, leaked := cm.chunkCache[0x2000]
		n := len(cm.chunkCache)
		cm.chunkMutex.Unlock()
		if leaked {
			t.Fatalf("refused transfer must not leave a chunkCache entry")
		}
		if n != cap {
			t.Fatalf("chunkCache grew past the cap: got %d want %d", n, cap)
		}

		// An EXISTING transfer must still be servable at the cap — the bound is
		// on opening new ones, not on making progress.
		if _, err := cm.GetChunkBuffer(0x1000, 64); err != nil {
			t.Fatalf("in-flight transfer must remain servable at the cap; got %v", err)
		}
		// And a total-mismatch reset of an existing id replaces in place rather
		// than growing the map, so it must not be refused either.
		if _, err := cm.GetChunkBuffer(0x1000, 128); err != nil {
			t.Fatalf("total-mismatch reset of an existing id must not be refused at the cap; got %v", err)
		}

		// Make one entry stale: the next new transfer prunes it and is admitted.
		cm.chunkMutex.Lock()
		cm.chunkCache[0x1001].LastAccess = time.Now().Add(-2 * DefaultChunkBufferTTL)
		cm.chunkMutex.Unlock()
		if _, err := cm.GetChunkBuffer(0x3000, 64); err != nil {
			t.Fatalf("prune-then-admit failed at the cap; got %v", err)
		}
		cm.chunkMutex.Lock()
		_, prunedGone := cm.chunkCache[0x1001]
		cm.chunkMutex.Unlock()
		if prunedGone {
			t.Fatalf("stale buffer should have been pruned to make room")
		}
	})

	t.Run("MaxConcurrentTransfers_WirePathReturnsSystemError", func(t *testing.T) {
		const cap = 4
		cm := NewChunkManagerFromConfig(ChunkManagerConfig{MaxConcurrentTransfers: cap})
		defer cm.Close()
		h := &SystemHandler{ChunkManager: cm}

		respBuf := make([]byte, 4096)
		b := flatbuffers.NewBuilder(1024)
		dispatch := func([]byte, []byte, shm.MsgType) (int32, shm.MsgType) {
			t.Fatalf("no transfer in this case is complete; dispatch must not fire")
			return 0, 0
		}

		// Each transfer declares 64 bytes and delivers only the first 32, so
		// none completes and all stay resident.
		for i := 0; i < cap; i++ {
			frame := buildChunkRequest(t, uint64(0x7000+i), 64, 0, bytes.Repeat([]byte{0x11}, 32), 0)
			if _, mt := h.HandleChunk(frame, respBuf, b, dispatch); mt == shm.MsgTypeSystemError {
				t.Fatalf("transfer %d within cap=%d must be accepted", i, cap)
			}
		}
		frame := buildChunkRequest(t, 0x8000, 64, 0, bytes.Repeat([]byte{0x11}, 32), 0)
		size, mt := h.HandleChunk(frame, respBuf, b, dispatch)
		if size != 0 || mt != shm.MsgTypeSystemError {
			t.Fatalf("transfer past the cap must return (0, MsgTypeSystemError); got (%d, %d)", size, mt)
		}
	})
}

// TestChunkBuffer_ClaimSegment covers the overlap arbiter directly, including
// the neighbour cases the wire-path tests only reach indirectly.
func TestChunkBuffer_ClaimSegment(t *testing.T) {
	newBuf := func() *ChunkBuffer { return &ChunkBuffer{Data: make([]byte, 1000), TotalSize: 1000} }

	t.Run("DisjointAscending", func(t *testing.T) {
		b := newBuf()
		for _, off := range []uint32{0, 10, 20, 30} {
			if got := b.ClaimSegment(off, 10); got != ClaimNew {
				t.Fatalf("offset %d: got %v want ClaimNew", off, got)
			}
		}
	})

	t.Run("DisjointDescending_SortedInsert", func(t *testing.T) {
		b := newBuf()
		for _, off := range []uint32{30, 20, 10, 0} {
			if got := b.ClaimSegment(off, 10); got != ClaimNew {
				t.Fatalf("offset %d: got %v want ClaimNew", off, got)
			}
		}
		for i := 1; i < len(b.Segments); i++ {
			if b.Segments[i-1].Offset >= b.Segments[i].Offset {
				t.Fatalf("Segments not kept ascending: %v", b.Segments)
			}
		}
	})

	t.Run("ExactRepeatIsDuplicate", func(t *testing.T) {
		b := newBuf()
		b.ClaimSegment(100, 50)
		if got := b.ClaimSegment(100, 50); got != ClaimDuplicate {
			t.Fatalf("got %v want ClaimDuplicate", got)
		}
	})

	t.Run("SameOffsetOtherLengthIsOverlap", func(t *testing.T) {
		b := newBuf()
		b.ClaimSegment(100, 50)
		if got := b.ClaimSegment(100, 20); got != ClaimOverlap {
			t.Fatalf("got %v want ClaimOverlap", got)
		}
	})

	t.Run("OverlapsPredecessorTail", func(t *testing.T) {
		b := newBuf()
		b.ClaimSegment(0, 60)
		if got := b.ClaimSegment(50, 40); got != ClaimOverlap {
			t.Fatalf("got %v want ClaimOverlap", got)
		}
	})

	t.Run("OverlapsSuccessorHead", func(t *testing.T) {
		b := newBuf()
		b.ClaimSegment(50, 40)
		if got := b.ClaimSegment(0, 60); got != ClaimOverlap {
			t.Fatalf("got %v want ClaimOverlap", got)
		}
	})

	t.Run("FullyContainedIsOverlap", func(t *testing.T) {
		b := newBuf()
		b.ClaimSegment(0, 100)
		if got := b.ClaimSegment(10, 10); got != ClaimOverlap {
			t.Fatalf("got %v want ClaimOverlap", got)
		}
	})

	t.Run("ExactlyAbuttingIsNew", func(t *testing.T) {
		b := newBuf()
		b.ClaimSegment(0, 50)
		b.ClaimSegment(100, 50)
		if got := b.ClaimSegment(50, 50); got != ClaimNew {
			t.Fatalf("a range that exactly fills the gap must be accepted; got %v", got)
		}
	})
}

// TestChunkManager_TotalMismatchResetsBuffer verifies that reusing a transfer
// id with a different declared total resets the buffer to a fresh one sized
// for the new total, instead of silently wedging the transfer until the TTL
// sweep (IMPROVEMENT_BACKLOG.md §3, manager.go GetChunkBuffer).
func TestChunkManager_TotalMismatchResetsBuffer(t *testing.T) {
	cm := NewChunkManager()
	const id uint64 = 0x9001

	// First allocation: total=64, partially fill it.
	buf1 := mustGetChunkBuffer(t, cm, id, 64)
	buf1.Mutex.Lock()
	copy(buf1.Data, bytes.Repeat([]byte{0xAB}, 32))
	buf1.Received = 32
	buf1.Segments = append(buf1.Segments, ChunkSegment{Offset: 0, Length: 32})
	buf1.Mutex.Unlock()

	// Reuse the same id with a different total. Must reset.
	buf2 := mustGetChunkBuffer(t, cm, id, 128)
	if buf2 == buf1 {
		t.Fatal("buffer was not reset on total mismatch; same pointer returned")
	}
	if buf2.TotalSize != 128 {
		t.Fatalf("reset buffer TotalSize=%d, want 128", buf2.TotalSize)
	}
	if len(buf2.Data) != 128 {
		t.Fatalf("reset buffer len(Data)=%d, want 128", len(buf2.Data))
	}
	if buf2.Received != 0 {
		t.Fatalf("reset buffer Received=%d, want 0", buf2.Received)
	}
	if len(buf2.Segments) != 0 {
		t.Fatalf("reset buffer Segments not empty: %v", buf2.Segments)
	}
	if !bytes.Equal(buf2.Data, make([]byte, 128)) {
		t.Fatal("reset buffer Data not zeroed")
	}

	// A subsequent fetch with the SAME (new) total must return buf2, not reset.
	buf3 := mustGetChunkBuffer(t, cm, id, 128)
	if buf3 != buf2 {
		t.Fatal("matching-total fetch unexpectedly reset the buffer")
	}
}

// TestChunkManager_ConcurrentArrivals stresses the ChunkManager under -race
// to catch any cache-visibility / ordering hazards on shared ChunkBuffer
// fields. This is a regression guard for the discipline rules in AGENTS.md
// §23 (Cache Visibility / Memory Ordering Discipline).
func TestChunkManager_ConcurrentArrivals(t *testing.T) {
	cm := NewChunkManager()

	const numTransfers = 32
	const chunksPerTransfer = 8
	const bytesPerChunk = 64
	const total = chunksPerTransfer * bytesPerChunk

	var wg sync.WaitGroup
	wg.Add(numTransfers)
	for tid := 0; tid < numTransfers; tid++ {
		tid := tid
		go func() {
			defer wg.Done()
			id := uint64(0x10000 + tid)
			buf := mustGetChunkBuffer(t, cm, id, total)
			// Send chunks in arbitrary order.
			order := []int{3, 1, 7, 0, 5, 2, 6, 4}
			for _, idx := range order {
				payload := bytes.Repeat([]byte{byte(idx + 1)}, bytesPerChunk)
				offset := idx * bytesPerChunk
				buf.Mutex.Lock()
				if offset+len(payload) <= len(buf.Data) {
					copy(buf.Data[offset:], payload)
					buf.Received += len(payload)
				}
				buf.Mutex.Unlock()
			}
			buf.Mutex.Lock()
			complete := buf.Received >= buf.TotalSize
			buf.Mutex.Unlock()
			if !complete {
				t.Errorf("transfer %d did not complete", tid)
			}
		}()
	}
	wg.Wait()
}

// TestChunkManager_ConcurrentDuplicateFinalChunk is the regression for the
// HIGH finding (handlers.go:77-98): a retransmitted FINAL chunk processed
// concurrently with the original. Before the fix, both goroutines observed
// Received >= TotalSize under buf.Mutex and BOTH called dispatch() — the user
// function re-executed (side effects!) and two responses were written. After
// the fix, ChunkBuffer.Dispatched is flipped under buf.Mutex by exactly one
// goroutine, so dispatch fires exactly once.
//
// We drive HandleChunk end-to-end. A two-chunk transfer: chunk0 lands first,
// then N copies of the FINAL chunk1 arrive concurrently (simulating ACK loss /
// retransmits). Exactly one dispatch must occur.
func TestChunkManager_ConcurrentDuplicateFinalChunk(t *testing.T) {
	const replays = 64

	for iter := 0; iter < 50; iter++ {
		cm := NewChunkManager()
		h := &SystemHandler{ChunkManager: cm}

		const id uint64 = 0x5151
		const totalU32 uint32 = 20 // two 10-byte chunks
		first := bytes.Repeat([]byte{0xAA}, 10)
		second := bytes.Repeat([]byte{0xBB}, 10)

		// Land the first (non-final) chunk synchronously.
		respBuf0 := make([]byte, 4096)
		b0 := flatbuffers.NewBuilder(1024)
		neverDispatch := func([]byte, []byte, shm.MsgType) (int32, shm.MsgType) {
			t.Fatalf("iter %d: dispatch fired after only chunk0", iter)
			return 0, 0
		}
		h.HandleChunk(buildChunkRequest(t, id, totalU32, 0, first, 0), respBuf0, b0, neverDispatch)

		// Now fire the FINAL chunk concurrently `replays` times. Each
		// goroutine gets its own respBuf and builder (those are not safe to
		// share) but they all hit the same ChunkBuffer.
		var dispatchCount int32
		var dispatchedData atomic.Value
		var startGate sync.WaitGroup
		startGate.Add(1)
		var wg sync.WaitGroup
		wg.Add(replays)
		for r := 0; r < replays; r++ {
			go func() {
				defer wg.Done()
				localResp := make([]byte, 4096)
				localB := flatbuffers.NewBuilder(1024)
				finalChunk := buildChunkRequest(t, id, totalU32, 10, second, 0)
				dispatch := func(data []byte, _ []byte, _ shm.MsgType) (int32, shm.MsgType) {
					atomic.AddInt32(&dispatchCount, 1)
					cp := append([]byte(nil), data...)
					dispatchedData.Store(cp)
					return 0, 0
				}
				startGate.Wait()
				h.HandleChunk(finalChunk, localResp, localB, dispatch)
			}()
		}
		startGate.Done()
		wg.Wait()

		if got := atomic.LoadInt32(&dispatchCount); got != 1 {
			t.Fatalf("iter %d: expected exactly 1 dispatch for concurrent duplicate final chunk; got %d", iter, got)
		}
		if dd, ok := dispatchedData.Load().([]byte); ok {
			want := append(append([]byte{}, first...), second...)
			if !bytes.Equal(dd, want) {
				t.Fatalf("iter %d: reassembled payload mismatch.\n got %v\nwant %v", iter, dd, want)
			}
		}
	}
}

// TestChunkManager_ZeroLengthSegmentRejected is the regression for the MED
// finding "a zero-length segment poisons a healthy chunk".
//
// A protocol.Chunk whose data vector is PRESENT but EMPTY passed every guard:
// the C++ mirror only tested `!chunk->data()` and the Go side had no length
// test at all. The frame then reached ClaimSegment, which happily recorded an
// (offset, 0) range — and the REAL chunk that later arrived at that same offset
// classified as "same start offset, DIFFERENT length" => ClaimOverlap => the
// whole, otherwise perfectly healthy, transfer was discarded with SystemError.
// One empty frame (a producer bug, a truncated retransmit, a hostile peer)
// killed a good transfer, and the diagnostic pointed at the innocent chunk.
//
// A zero-length segment advances nothing, so it can never be a legitimate part
// of a transfer: both sides now refuse it up front.
func TestChunkManager_ZeroLengthSegmentRejected(t *testing.T) {
	respBuf := make([]byte, 4096)
	noDispatch := func(t *testing.T) func([]byte, []byte, shm.MsgType) (int32, shm.MsgType) {
		return func([]byte, []byte, shm.MsgType) (int32, shm.MsgType) {
			t.Fatalf("a rejected transfer must never reach the completion dispatch")
			return 0, 0
		}
	}

	t.Run("EmptySegmentRefusedNotRecorded", func(t *testing.T) {
		cm := NewChunkManager()
		defer cm.Close()
		h := &SystemHandler{ChunkManager: cm}
		b := flatbuffers.NewBuilder(1024)

		const id uint64 = 0x0E0E0E
		const total uint32 = 32

		// Present-but-empty data vector at offset 0.
		size, mt := h.HandleChunk(buildChunkRequest(t, id, total, 0, []byte{}, 0), respBuf, b, noDispatch(t))
		if size != 0 || mt != shm.MsgTypeSystemError {
			t.Fatalf("a zero-length chunk segment must return (0, MsgTypeSystemError); got (%d, %d) "+
				"— accepting it records an (offset, 0) range that turns the real chunk at that "+
				"offset into a ClaimOverlap and kills the transfer", size, mt)
		}

		cm.chunkMutex.Lock()
		_, stillThere := cm.chunkCache[id]
		cm.chunkMutex.Unlock()
		if stillThere {
			t.Fatalf("a refused zero-length segment must drop the transfer; buffer for id=%#x still resident", id)
		}
	})

	t.Run("EmptySegmentCannotPreemptTheRealChunk", func(t *testing.T) {
		// The harm the refusal prevents, stated as a property: after an empty
		// frame at offset 0, no segment covering offset 0 exists, so nothing an
		// honest producer sends later can be mistaken for an overlap of it.
		cm := NewChunkManager()
		defer cm.Close()
		h := &SystemHandler{ChunkManager: cm}
		b := flatbuffers.NewBuilder(1024)

		const id uint64 = 0x0E0E0F
		const total uint32 = 32

		_, _ = h.HandleChunk(buildChunkRequest(t, id, total, 0, []byte{}, 0), respBuf, b, noDispatch(t))

		cm.chunkMutex.Lock()
		buf, present := cm.chunkCache[id]
		cm.chunkMutex.Unlock()
		if present {
			buf.Mutex.Lock()
			segs := append([]ChunkSegment(nil), buf.Segments...)
			buf.Mutex.Unlock()
			t.Fatalf("the empty frame left a live buffer with segments %v; before the fix the "+
				"recorded (0,0) range made the real chunk at offset 0 a ClaimOverlap", segs)
		}
	})
}

// TestChunkManager_RejectedTransferIsPoisoned is the regression for the MED
// finding "rejection is not final — the next chunk resurrects the transfer".
//
// Every reject path drops the ChunkBuffer, and nothing remembered the id. The
// producer's NEXT chunk for that id therefore found no buffer, GetChunkBuffer
// allocated a FRESH one, and the chunk was acked as SUCCESS. Two effects, both
// the exact inverse of the fail-fast the refusal was added for:
//
//   - the resurrected buffer is missing every earlier chunk, so it can never
//     complete and squats a MaxConcurrentTransfers slot until the TTL sweep;
//   - pkg/chunk.AsyncRetry (10x exponential backoff) receives a SUCCESS on its
//     first retry, so the producer never aborts and keeps pushing — the async
//     call hangs to its own timeout instead of failing immediately.
//
// The fix records the id in a TTL-bounded poison set; every later chunk on that
// id is refused at the door.
func TestChunkManager_RejectedTransferIsPoisoned(t *testing.T) {
	respBuf := make([]byte, 4096)

	t.Run("OverlapRejectionBlocksTheFollowingChunk", func(t *testing.T) {
		cm := NewChunkManager()
		defer cm.Close()
		h := &SystemHandler{ChunkManager: cm}
		b := flatbuffers.NewBuilder(1024)
		dispatch := func([]byte, []byte, shm.MsgType) (int32, shm.MsgType) {
			t.Fatalf("a poisoned transfer must never dispatch")
			return 0, 0
		}

		const id uint64 = 0xA001
		const total uint32 = 100

		if _, mt := h.HandleChunk(buildChunkRequest(t, id, total, 0, bytes.Repeat([]byte{0xA1}, 60), 0), respBuf, b, dispatch); mt == shm.MsgTypeSystemError {
			t.Fatalf("the first well-formed chunk must be accepted")
		}
		if _, mt := h.HandleChunk(buildChunkRequest(t, id, total, 50, bytes.Repeat([]byte{0xA2}, 40), 0), respBuf, b, dispatch); mt != shm.MsgTypeSystemError {
			t.Fatalf("the overlapping chunk must be refused")
		}

		// THE REGRESSION: the producer's next chunk (offset != 0, so it cannot
		// be mistaken for a new transfer opening at 0) must NOT be acked.
		size, mt := h.HandleChunk(buildChunkRequest(t, id, total, 60, bytes.Repeat([]byte{0xA3}, 40), 0), respBuf, b, dispatch)
		if size != 0 || mt != shm.MsgTypeSystemError {
			t.Fatalf("a chunk arriving after the transfer was rejected must return "+
				"(0, MsgTypeSystemError); got (%d, %d) — an ack here resurrects a buffer that "+
				"can never complete and tells the retry ladder to keep pushing", size, mt)
		}
		cm.chunkMutex.Lock()
		_, resurrected := cm.chunkCache[id]
		cm.chunkMutex.Unlock()
		if resurrected {
			t.Fatalf("the rejected transfer was resurrected: a buffer for id=%#x is resident again "+
				"and will hold a MaxConcurrentTransfers slot until the TTL sweep", id)
		}
	})

	t.Run("OutOfBoundsAndZeroLengthPoisonToo", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			id    uint64
			frame func(t *testing.T) []byte
		}{
			{"OutOfBounds", 0xB0, func(t *testing.T) []byte {
				return buildChunkRequest(t, 0xB0, 16, 8, bytes.Repeat([]byte{0xCD}, 32), 0)
			}},
			{"ZeroLength", 0xB1, func(t *testing.T) []byte {
				return buildChunkRequest(t, 0xB1, 16, 0, []byte{}, 0)
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cm := NewChunkManager()
				defer cm.Close()
				h := &SystemHandler{ChunkManager: cm}
				b := flatbuffers.NewBuilder(1024)
				dispatch := func([]byte, []byte, shm.MsgType) (int32, shm.MsgType) { return 0, 0 }

				if _, mt := h.HandleChunk(tc.frame(t), respBuf, b, dispatch); mt != shm.MsgTypeSystemError {
					t.Fatalf("%s must be refused", tc.name)
				}
				if !cm.IsPoisoned(tc.id) {
					t.Fatalf("%s must poison the transfer id", tc.name)
				}
				// Any follow-up on that id, well-formed or not, is refused.
				size, mt := h.HandleChunk(buildChunkRequest(t, tc.id, 16, 0, bytes.Repeat([]byte{0x01}, 16), 0), respBuf, b, dispatch)
				if size != 0 || mt != shm.MsgTypeSystemError {
					t.Fatalf("a follow-up chunk on the poisoned id must be refused; got (%d, %d)", size, mt)
				}
			})
		}
	})

	t.Run("PoisonIsPerIdNotGlobal", func(t *testing.T) {
		cm := NewChunkManager()
		defer cm.Close()
		h := &SystemHandler{ChunkManager: cm}
		b := flatbuffers.NewBuilder(1024)

		// Poison one id...
		_, _ = h.HandleChunk(buildChunkRequest(t, 0xC0, 16, 0, []byte{}, 0), respBuf, b,
			func([]byte, []byte, shm.MsgType) (int32, shm.MsgType) { return 0, 0 })

		// ...an unrelated transfer must still complete normally.
		var got []byte
		dispatch := func(data []byte, _ []byte, _ shm.MsgType) (int32, shm.MsgType) {
			got = append([]byte(nil), data...)
			return 0, 0
		}
		payload := bytes.Repeat([]byte{0x7E}, 16)
		if _, mt := h.HandleChunk(buildChunkRequest(t, 0xC1, 16, 0, payload, 0), respBuf, b, dispatch); mt == shm.MsgTypeSystemError {
			t.Fatalf("poisoning id 0xC0 must not affect id 0xC1")
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("unrelated transfer did not reassemble: got %v want %v", got, payload)
		}
	})

	t.Run("PoisonExpiresWithTheTTL", func(t *testing.T) {
		// The poison set must never burn an id permanently: a producer that
		// reconnects after the window gets a clean slate, exactly like a stale
		// reassembly buffer.
		cm := NewChunkManagerFromConfig(ChunkManagerConfig{BufferTTL: 20 * time.Millisecond})
		defer cm.Close()
		h := &SystemHandler{ChunkManager: cm}
		b := flatbuffers.NewBuilder(1024)

		const id uint64 = 0xD0
		_, _ = h.HandleChunk(buildChunkRequest(t, id, 16, 0, []byte{}, 0), respBuf, b,
			func([]byte, []byte, shm.MsgType) (int32, shm.MsgType) { return 0, 0 })
		if !cm.IsPoisoned(id) {
			t.Fatalf("id must be poisoned right after the refusal")
		}

		time.Sleep(40 * time.Millisecond)
		if cm.IsPoisoned(id) {
			t.Fatalf("the poison entry must expire with the buffer TTL")
		}

		var got []byte
		payload := bytes.Repeat([]byte{0x5C}, 16)
		if _, mt := h.HandleChunk(buildChunkRequest(t, id, 16, 0, payload, 0), respBuf, b,
			func(data []byte, _ []byte, _ shm.MsgType) (int32, shm.MsgType) {
				got = append([]byte(nil), data...)
				return 0, 0
			}); mt == shm.MsgTypeSystemError {
			t.Fatalf("after the poison expired the id must be reusable from scratch")
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("reused id did not reassemble: got %v want %v", got, payload)
		}
	})

	t.Run("ResourceRefusalDoesNotPoison", func(t *testing.T) {
		// Deliberate asymmetry: hitting MaxConcurrentTransfers is TRANSIENT and
		// inserts no buffer, so there is nothing to resurrect and a later retry
		// on the same id may legitimately succeed. Only protocol violations
		// poison.
		const cap = 2
		cm := NewChunkManagerFromConfig(ChunkManagerConfig{MaxConcurrentTransfers: cap})
		defer cm.Close()
		h := &SystemHandler{ChunkManager: cm}
		b := flatbuffers.NewBuilder(1024)
		dispatch := func([]byte, []byte, shm.MsgType) (int32, shm.MsgType) { return 0, 0 }

		for i := 0; i < cap; i++ {
			frame := buildChunkRequest(t, uint64(0xE000+i), 64, 0, bytes.Repeat([]byte{0x11}, 32), 0)
			if _, mt := h.HandleChunk(frame, respBuf, b, dispatch); mt == shm.MsgTypeSystemError {
				t.Fatalf("transfer %d within cap=%d must be accepted", i, cap)
			}
		}
		const late uint64 = 0xEFFF
		if _, mt := h.HandleChunk(buildChunkRequest(t, late, 64, 0, bytes.Repeat([]byte{0x11}, 32), 0), respBuf, b, dispatch); mt != shm.MsgTypeSystemError {
			t.Fatalf("the transfer past the cap must be refused")
		}
		if cm.IsPoisoned(late) {
			t.Fatalf("a RESOURCE refusal must not poison the id: it is transient and inserts no buffer")
		}
		// Free a slot; the same id must now be admitted.
		cm.RemoveChunkBuffer(0xE000)
		if _, mt := h.HandleChunk(buildChunkRequest(t, late, 64, 0, bytes.Repeat([]byte{0x11}, 32), 0), respBuf, b, dispatch); mt == shm.MsgTypeSystemError {
			t.Fatalf("once a slot frees up, the previously capped id must be admitted")
		}
	})
}

// TestChunkManager_CloseStopsCleanupGoroutine is the regression for the MED
// finding (manager.go:94-111): the cleanupLoop goroutine + ticker could never
// be stopped — there was no Close(). After the fix, Close() (idempotent) signals
// the stop channel and the goroutine exits. We assert via goroutine count: each
// NewChunkManager spawns one cleanup goroutine; Close must let it return.
func TestChunkManager_CloseStopsCleanupGoroutine(t *testing.T) {
	// Settle any goroutines left over from prior subtests.
	settle := func() {
		for i := 0; i < 50; i++ {
			runtime.GC()
			time.Sleep(2 * time.Millisecond)
		}
	}
	settle()
	before := runtime.NumGoroutine()

	const n = 25
	cms := make([]*ChunkManager, n)
	for i := range cms {
		// Tiny interval so the loop is actively selecting on the ticker.
		cms[i] = NewChunkManagerFromConfig(ChunkManagerConfig{CleanupInterval: time.Millisecond})
	}
	// Give the goroutines a moment to actually be scheduled and running.
	time.Sleep(20 * time.Millisecond)
	during := runtime.NumGoroutine()
	if during < before+n {
		t.Fatalf("expected at least %d new cleanup goroutines after spawning %d managers; before=%d during=%d", n, n, before, during)
	}

	for _, cm := range cms {
		cm.Close()
		// Idempotency: a second Close must not panic on a double channel close.
		cm.Close()
	}

	// Wait for the goroutines to drain back to (about) the baseline.
	deadline := time.Now().Add(5 * time.Second)
	var after int
	for {
		settle()
		after = runtime.NumGoroutine()
		if after <= before+2 { // small slack for test-runtime goroutines
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cleanup goroutines did not exit after Close(): before=%d after=%d (leaked ~%d)", before, after, after-before)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
