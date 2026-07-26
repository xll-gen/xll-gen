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
	"errors"
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

	// MaxTransferBytes is the largest TOTAL payload one chunked guest->host
	// transfer may carry — the SENDER-side twin of the receiver's per-transfer
	// cap. A payload over it is refused BEFORE the first frame goes out
	// (Sender.Send returns ErrTransferTooLarge).
	//
	// WHY A HARD-CODED CONSTANT, AND WHY THIS NUMBER. The receiver in this
	// direction is the C++ XLL's HandleChunk
	// (internal/assets/files/src/xll_worker.cpp), which refuses any frame whose
	// declared total_size exceeds its compile-time kMaxChunkTotalSize = 256 MiB.
	// The sender CANNOT LEARN that value at runtime: kMaxChunkTotalSize has no
	// template or YAML wiring (AGENTS.md §18.6.1 "Tunability is one-sided"), and
	// the shm handshake carries slot geometry only — nothing negotiates a
	// reassembly cap. So the only options are "guess conservatively" and "guess
	// the same number the receiver compiles in"; this is the latter, pinned by
	// TestMaxTransferBytesMatchesReceiverCap and by the C++-side
	// `kMaxChunkTotalSize` marker test in internal/assets.
	//
	// NOT the same knob as pkg/server.DefaultMaxChunkBufferBytes. That constant
	// is the default for the GO reassembler, which runs in the OPPOSITE
	// direction (host->guest) and IS tunable per project via xll.yaml
	// `server.chunk.max_buffer_bytes`. The two numbers are equal today by hand,
	// not by construction — do not describe them as one value in "lockstep", and
	// do not derive one from the other.
	//
	// Without this guard an oversized transfer was pushed frame by frame, every
	// frame refused by the host with SYSTEM_ERROR, every refusal absorbed by the
	// AsyncRetry ladder, and the transfer abandoned with nothing but a log line —
	// while the Excel cells waiting on it stayed at #GETTING_DATA forever.
	MaxTransferBytes = 256 << 20
)

// ErrTransferTooLarge is returned by Sender.Send for a payload larger than the
// effective per-transfer cap (Sender.MaxTotalBytes, default MaxTransferBytes).
// It is a SENDER-side refusal: no frame is emitted, so the receiver never sees
// the transfer at all. Callers must turn it into a diagnosable failure for
// whatever is waiting on the payload (an error result for an async handle, a
// failed RTD one-shot) — retrying an unchanged payload can never succeed.
var ErrTransferTooLarge = errors.New("chunk: transfer exceeds the receiver's maximum total size")

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

// MaxRequestSizer is the shm surface needed to learn the REAL guest->host
// request-buffer capacity. *shm.Client and *shm.DirectGuest satisfy it
// (shm >= v0.8.15); test stubs deliberately do not, so they fall back to the
// conservative default.
//
// It lives here, in the leaf, rather than in pkg/server or pkg/rtd because BOTH
// of those need it and pkg/server imports pkg/rtd (NewSystemHandler) — the same
// cycle that already forced DefaultChunkSize, transferid.New and BuildFrame down
// into leaves. See the package doc and AGENTS.md §18.4.
type MaxRequestSizer interface {
	// MaxRequestSize reports the byte capacity of one slot's request buffer,
	// or 0 when it is unknown (nil / not connected).
	MaxRequestSize() int
}

// GuestBudget returns the per-chunk / single-slot payload budget for a
// guest->host send over client, derived from the ACTUAL request-buffer capacity
// shm published for this SHM segment (slot payload / 2, see the geometry note
// above).
//
// It is a PURE READ of geometry fixed at connect time: shm's MaxRequestSize
// claims no slot and writes no shared memory. It replaced a probe that had to
// AcquireGuestSlot()/Release() just to measure len(slot.RequestBuffer()); the
// value is identical by construction, because shm derives both from the same
// respOffset-reqOffset (shm/go/direct.go NewDirectGuest).
//
// FRAMING IS DEDUCTED EXACTLY ONCE. MaxRequestSize is the RAW capacity — shm
// subtracts nothing, including its own 24-byte streaming ChunkHeader (that one
// is StreamSender's business, and xll-gen does not use shm streams). The only
// header on this wire is our own protocol.Chunk frame, so Budget's single
// FramingOverhead subtraction is the whole deduction.
//
// The parameter is `any` so a caller holding a narrower interface (pkg/rtd's
// rtdClient, which only needs SendGuestCallWithTimeout) can pass it without a
// local type assertion. A client that does not implement MaxRequestSizer, and a
// capacity of 0 ("unknown" — nil or not-connected, per shm's docstring), both
// fall back to DefaultChunkSize, which is correct for the 1 MiB slot payload the
// generated host configures. Calling this with a typed-nil *shm.Client is safe:
// shm's accessor is nil-receiver safe and answers 0.
func GuestBudget(client any) int {
	sizer, ok := client.(MaxRequestSizer)
	if !ok {
		return DefaultChunkSize
	}
	capacity := sizer.MaxRequestSize()
	if capacity <= 0 {
		return DefaultChunkSize
	}
	return Budget(capacity)
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
	// MaxTotalBytes is the largest payload this Sender will transmit. Zero
	// means MaxTransferBytes. It exists as a field ONLY so tests can exercise
	// the refusal without materializing a 256 MiB payload; production callers
	// leave it zero, because the real bound is a property of the receiver, not
	// of the call site.
	MaxTotalBytes int
}

// chunkSize returns the effective per-chunk budget.
func (s *Sender) chunkSize() int {
	if s.ChunkSize > 0 {
		return s.ChunkSize
	}
	return DefaultChunkSize
}

// maxTotalBytes returns the effective per-transfer cap.
func (s *Sender) maxTotalBytes() int {
	if s.MaxTotalBytes > 0 {
		return s.MaxTotalBytes
	}
	return MaxTransferBytes
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
//
// A payload larger than the effective per-transfer cap (MaxTotalBytes, default
// MaxTransferBytes) is refused UP FRONT with ErrTransferTooLarge and NO frame
// is sent. That refusal is not an optimization: the receiver rejects the first
// frame on its declared total_size, the retry ladder absorbs every rejection,
// and the transfer dies with only a log line while whatever is waiting on the
// payload waits forever. Splitting cannot fix a single payload that is over the
// cap — the cap is on ONE transfer's total_size, not on one frame — so the
// caller must convert this error into a visible failure.
func (s *Sender) Send(payload []byte, transferID uint64, msgType uint32, send SendFunc, policy RetryPolicy) error {
	if len(payload) == 0 {
		return nil
	}
	if max := s.maxTotalBytes(); len(payload) > max {
		return fmt.Errorf("%w: %d bytes > %d (id %#x)", ErrTransferTooLarge, len(payload), max, transferID)
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
