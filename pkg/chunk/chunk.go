// Package chunk is the single source of truth for the SHM chunk wire frame and
// the chunk-split loop shared by every guest->host chunked transfer.
//
// Before R24 the chunk-split loop and frame build were copy-pasted across three
// sites with three different transport models:
//
//   - pkg/server.SendAckOrChunk      (host->guest, ACK-pull via ChunkManager)
//   - pkg/server.sendChunkedAsync    (guest->host, push + retry)
//   - pkg/rtd.SendOnceGrid           (guest->host, push, synchronous, no retry)
//
// The TRANSPORT models are intentionally different and stay different. What was
// duplicated — and is unified here — is purely (a) the frame byte layout, (b)
// the offset-advancing split loop, and (c) the chunk-size budget arithmetic
// (formerly DefaultChunkSize in pkg/server AND a hand-copied onceGridChunkSize
// in pkg/rtd).
//
// pkg/chunk is a leaf: it imports only the flatbuffers runtime, the generated
// types/protocol package, and pkg/transferid. That lets BOTH pkg/server and
// pkg/rtd depend on it despite the server->rtd import cycle (pkg/server imports
// pkg/rtd via NewSystemHandler), exactly as pkg/msgid and pkg/transferid already
// do. See AGENTS.md §18.4 (chunking co-change cluster) and §23.3.
package chunk

import (
	"fmt"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
)

// Slot geometry the chunk budget is derived from.
//
// A SHM slot's payload is SPLIT IN HALF by the host: the first half is the
// request region (guest->host), the second half the response region
// (host->guest). Missing that `/2` is what made the pre-fix 950 KiB constant
// 1.86x too large for the real 512 KiB request buffer — EVERY guest->host
// chunk frame was rejected by shm with "data too large". The arithmetic, from
// the authoritative sources:
//
//	internal/templates/xll_main.cpp.tmpl   hostCfg.payloadSize = 1024*1024   (1 MiB)
//	shm/include/shm/DirectHost.h Init()    halfSize   = slotSize/2 rounded down to 64B
//	                                       reqOffset  = 0
//	                                       respOffset = halfSize
//	shm/go/direct.go NewDirectGuest()      maxReq  = respOffset - reqOffset  = 512 KiB
//	                                       maxResp = slotSize   - respOffset = 512 KiB
//	shm/go/direct.go sendGuestCallInternal if len(data) > len(slot.reqBuffer) -> "data too large"
//
// So both the request and the response region are 512 KiB, NOT 1 MiB.
const (
	// SlotPayloadSize is the SHM slot payload size the default budget assumes
	// (hostCfg.payloadSize in xll_main.cpp.tmpl). It is the WHOLE slot, both
	// halves.
	SlotPayloadSize = 1024 * 1024

	// HalfSlotSize is the usable capacity of ONE direction's buffer:
	// SlotPayloadSize/2, i.e. what shm reports as len(reqBuffer) guest-side
	// and maxRespSize host-side. 1 MiB / 2 = 512 KiB = 524288 bytes.
	HalfSlotSize = SlotPayloadSize / 2

	// FramingOverhead is a conservative UPPER BOUND on the bytes BuildFrame
	// adds around the raw chunk payload: the FlatBuffers root offset (4) +
	// "XCHN" file identifier (4) + vtable (~14) + Chunk table (soffset 4 +
	// id u64 8 + total_size u32 4 + offset u32 4 + msg_type u32 4 + data
	// uoffset 4) + the byte-vector length prefix (4) + alignment padding.
	// That is ~60-70 bytes in practice; 256 leaves a wide margin.
	// TestFramingOverheadIsAnUpperBound pins the real value below this.
	FramingOverhead = 256

	// DefaultChunkSize is the per-chunk payload byte budget for chunked
	// transfers, and the UPPER BOUND on any chunk payload:
	//
	//	HalfSlotSize - FramingOverhead = 524288 - 256 = 524032 bytes
	//
	// so that (chunk payload + frame) always fits one direction's buffer.
	//
	// This is the ONE definition. pkg/server.DefaultChunkSize and the former
	// pkg/rtd.onceGridChunkSize are aliases of this value so all three sites
	// chunk at the same boundary and the C++ HandleChunk reassembly contract
	// is unchanged (it keys on id/total_size/offset, never on chunk size).
	//
	// Prefer Budget(bufCap) over this constant wherever the REAL buffer
	// capacity is in hand: the constant can only be correct for the slot
	// geometry hardcoded above, while Budget adapts to a project that
	// configures a different hostCfg.payloadSize.
	DefaultChunkSize = HalfSlotSize - FramingOverhead
)

// Budget returns the per-chunk payload size that is guaranteed to fit a buffer
// of bufCap bytes once framed, capped at DefaultChunkSize and floored at 1.
//
// It is the single source of the budget arithmetic for every chunking site:
// host->guest uses Budget(len(respBuf)) (pkg/server.ChunkBudget), guest->host
// uses Budget(len(client request buffer)). Deriving from the real capacity is
// what keeps a slot geometry other than the SlotPayloadSize assumed above from
// silently breaking every transfer.
func Budget(bufCap int) int {
	budget := bufCap - FramingOverhead
	if budget > DefaultChunkSize {
		budget = DefaultChunkSize
	}
	if budget < 1 {
		budget = 1
	}
	return budget
}

// fileIdentifier is the 4-byte FlatBuffers file identifier on every Chunk
// frame. The C++ host's HandleChunk keys reassembly on it; it MUST stay "XCHN".
var fileIdentifier = []byte("XCHN")

// BuildFrame constructs a single protocol.Chunk wire frame using the provided
// builder and returns the finished bytes. The output is byte-identical to the
// pre-R24 pkg/server.BuildChunkResponse and pkg/rtd.SendOnceGrid hand-built
// frame: same field set (id, total_size, offset, data, msg_type), same
// CreateByteVector-before-ChunkStart order, same "XCHN" file identifier. Do NOT
// reorder the builder calls or change the identifier — the C++ HandleChunk
// reassembler depends on the exact layout.
func BuildFrame(b *flatbuffers.Builder, chunkData []byte, id uint64, totalSize int, offset int, msgType uint32) []byte {
	b.Reset()
	dataOff := b.CreateByteVector(chunkData)
	protocol.ChunkStart(b)
	protocol.ChunkAddId(b, id)
	protocol.ChunkAddTotalSize(b, uint32(totalSize))
	protocol.ChunkAddOffset(b, uint32(offset))
	protocol.ChunkAddData(b, dataOff)
	protocol.ChunkAddMsgType(b, msgType)
	root := protocol.ChunkEnd(b)
	b.FinishWithFileIdentifier(root, fileIdentifier)
	return b.FinishedBytes()
}

// SendFunc delivers one fully framed Chunk frame to the host. It returns nil on
// success. The split loop calls it once per chunk, in ascending offset order.
//
// Callers adapt their transport here: the push+retry async path wraps
// shm.Client.SendGuestCall; the synchronous rtd-once path wraps the same call
// but without retry; a timeout-bearing transport can close over
// SendGuestCallWithTimeout. The bytes passed in are only valid for the duration
// of the call — the loop reuses the builder buffer — so a SendFunc that retains
// the frame must copy it.
type SendFunc func(frame []byte) error

// RetryPolicy configures the optional retry wrapper applied to each chunk send.
// The zero value means "no retry" (one attempt). A non-zero Attempts enables
// retry with exponential backoff.
type RetryPolicy struct {
	// Attempts is the total number of send attempts per chunk (1 = no retry).
	// Values < 1 are treated as 1.
	Attempts int
	// BaseBackoff is the first inter-attempt sleep; each subsequent sleep
	// doubles it (BaseBackoff, 2*BaseBackoff, 4*BaseBackoff, ...). The sleep
	// after the final attempt is skipped — there is no retry to space out.
	BaseBackoff time.Duration
}

// NoRetry is the policy used by transports that must surface the first send
// error immediately (the synchronous rtd-once grid path).
var NoRetry = RetryPolicy{Attempts: 1}

// AsyncRetry mirrors the historical pkg/server.sendWithRetry policy: 10 attempts
// with 5ms base backoff (5ms, 10ms, ... 1.28s; ~2.56s max total wait across the
// 9 inter-attempt sleeps) to ride out transient buffer fullness.
var AsyncRetry = RetryPolicy{Attempts: 10, BaseBackoff: 5 * time.Millisecond}

// sleepFn is indirected so tests can run the retry path without real sleeps.
var sleepFn = time.Sleep

// sendWithRetry invokes send up to policy.Attempts times with exponential
// backoff, returning nil on the first success or the last error after exhausting
// all attempts. With policy.Attempts <= 1 it makes exactly one attempt and
// never sleeps.
func sendWithRetry(send SendFunc, frame []byte, policy RetryPolicy) error {
	attempts := policy.Attempts
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for i := 0; i < attempts; i++ {
		if err = send(frame); err == nil {
			return nil
		}
		if i == attempts-1 {
			break // no retry follows the last attempt; don't sleep before returning
		}
		if policy.BaseBackoff > 0 {
			sleepFn(policy.BaseBackoff * time.Duration(1<<i))
		}
	}
	return err
}

// Sender owns the chunk-size budget and frame builder for a chunked transfer.
// It is stateless beyond ChunkSize/builder and safe to construct per call; the
// builder it carries is NOT goroutine-safe, so a Sender must not be shared
// across concurrent Send calls.
type Sender struct {
	// ChunkSize is the per-chunk payload byte budget. Zero means DefaultChunkSize.
	ChunkSize int
	// Builder is the FlatBuffers builder used to frame each chunk. If nil, a
	// fresh builder is allocated on first use. Callers that pool builders pass
	// their own to avoid per-transfer allocation.
	Builder *flatbuffers.Builder
}

// chunkSize returns the effective per-chunk budget.
func (s *Sender) chunkSize() int {
	if s.ChunkSize > 0 {
		return s.ChunkSize
	}
	return DefaultChunkSize
}

// Send splits payload into protocol.Chunk frames of at most ChunkSize bytes each
// and delivers them in ascending offset order via send, applying policy's retry
// wrapper to each chunk. Every frame carries the shared transferID, the full
// payload length as total_size, the chunk's byte offset, and msgType (the REAL
// message type the host should dispatch once reassembled — e.g. MsgRtdOnceGrid
// or MsgBatchAsyncResponse; the slot-level message type is MsgChunk and is the
// caller's concern inside send).
//
// It returns nil once the whole payload is delivered, or the first chunk's send
// error (after that chunk's retries are exhausted), aborting the transfer.
//
// An empty payload sends zero frames and returns nil. Callers that treat empty
// as an error must check before calling.
func (s *Sender) Send(payload []byte, transferID uint64, msgType uint32, send SendFunc, policy RetryPolicy) error {
	if len(payload) == 0 {
		return nil
	}
	if s.Builder == nil {
		s.Builder = flatbuffers.NewBuilder(1024)
	}

	cs := s.chunkSize()
	total := len(payload)
	for offset := 0; offset < total; {
		end := offset + cs
		if end > total {
			end = total
		}
		frame := BuildFrame(s.Builder, payload[offset:end], transferID, total, offset, msgType)
		if err := sendWithRetry(send, frame, policy); err != nil {
			return fmt.Errorf("chunk at offset %d (id %#x): %w", offset, transferID, err)
		}
		offset = end
	}
	return nil
}
