package server

import (
	"sort"
	"sync"
	"time"

	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/pkg/algo"
	"github.com/xll-gen/xll-gen/pkg/chunk"
	"github.com/xll-gen/xll-gen/pkg/msgid"
)

// AnyValue aliases protocol.AnyValue so consumers in pkg/server can speak in
// FlatBuffers union-tag terms without importing protocol directly.
type AnyValue = protocol.AnyValue

// ScalarValue is a tagged-union representation of an Excel cell scalar — the
// Go-side mirror of protocol.Scalar. Only the field selected by Type is
// meaningful; the other fields are zero. Used by ChunkManager and command
// queue when an XLOPER12 is deserialized into something less stringly.
type ScalarValue struct {
	Type AnyValue
	Num  float64
	Int  int32
	Bool bool
	Str  string
	Err  int16
}

// The message-ID constants live in pkg/msgid (a leaf package with no
// dependency on pkg/server or pkg/rtd, which lets pkg/rtd drop its shadow
// copy of MsgRtdUpdate). They are re-exported here as aliases so all existing
// references through server.Msg* — including generated code — keep compiling
// unchanged. The C++ mirror in internal/assets/files/include/xll_ipc.h and
// the §18.6 mirror discipline are unchanged; pkg/msgid is the Go-side source.
const (
	MsgAck = msgid.MsgAck
	// System error signals are sourced from shm directly — see
	// shm.MsgTypeSystemError (value 127). The local mirror that used to
	// live here was removed in xll-gen v0.3.8 / shm v0.6.0+.
	MsgBatchAsyncResponse  = msgid.MsgBatchAsyncResponse
	MsgChunk               = msgid.MsgChunk
	MsgSetRefCache         = msgid.MsgSetRefCache
	MsgCalculationEnded    = msgid.MsgCalculationEnded
	MsgCalculationCanceled = msgid.MsgCalculationCanceled
	// RTD Messages (133-136)
	MsgRtdConnect    = msgid.MsgRtdConnect
	MsgRtdDisconnect = msgid.MsgRtdDisconnect
	MsgRtdUpdate     = msgid.MsgRtdUpdate
	MsgRtdHeartbeat  = msgid.MsgRtdHeartbeat

	// Command (ribbon/macro) invocation — must stay in sync with
	// MSG_COMMAND_INVOKE in internal/assets/files/include/xll_ipc.h.
	MsgCommandInvoke = msgid.MsgCommandInvoke

	// RTD-once grid result (guest->host one-shot grid delivery) — must stay in
	// sync with MSG_RTD_ONCE_GRID in internal/assets/files/include/xll_ipc.h.
	MsgRtdOnceGrid = msgid.MsgRtdOnceGrid

	// User Messages Start
	MsgUserStart = msgid.MsgUserStart
)

// DefaultChunkSize is the per-chunk payload byte budget for chunked
// transfers. It is the UPPER BOUND on a chunk; the actual budget is derived
// per-call from the real buffer size via ChunkBudget so a slot payload
// smaller than the assumed geometry chunks correctly instead of overflowing.
//
// Single source of truth: pkg/chunk.DefaultChunkSize, which spells out the
// full arithmetic — crucially the host's `payloadSize / 2` request/response
// split that the old 950 KiB literal ignored. Aliased here (and formerly
// hand-copied as pkg/rtd.onceGridChunkSize) so all three chunk-framing sites
// split at the same boundary. See AGENTS.md §18.4.
const DefaultChunkSize = chunk.DefaultChunkSize

// ChunkFramingOverhead is a conservative upper bound on the bytes
// BuildChunkResponse adds around the raw chunk payload (FlatBuffers vtable +
// table fields id/total_size/offset/msg_type + the data vector header +
// alignment + root offset + "XCHN" identifier). Alias of
// pkg/chunk.FramingOverhead. The post-build `len(chunkPayload) > len(respBuf)`
// check in SendAckOrChunk remains as a backstop.
const ChunkFramingOverhead = chunk.FramingOverhead

// ChunkBudget returns the per-chunk payload size that is guaranteed to fit in
// respBuf after framing, capped at DefaultChunkSize. Previously the chunk size
// was hardcoded to DefaultChunkSize regardless of the actual response buffer,
// so a slot payload smaller than the constant + overhead made every chunk send
// fail the framing check. Deriving from len(respBuf) makes chunking adapt to
// the real slot geometry; the cap keeps memory bounded for large slots. Both
// the initial send and the resend path (HandleAck) call this with the same
// respBuf, so chunk boundaries stay consistent.
//
// The arithmetic lives in pkg/chunk.Budget so the guest->host sites
// (async_batcher, rtd.SendOnceGrid) derive their budget identically.
func ChunkBudget(respBuf []byte) int {
	return chunk.Budget(len(respBuf))
}

// ChunkSegment is one byte range [Offset, Offset+Length) that has been written
// into a ChunkBuffer. Segments are held ascending by Offset and MUST NOT
// overlap — see ChunkBuffer.Segments and ChunkBuffer.ClaimSegment.
type ChunkSegment struct {
	Offset uint32
	Length uint32
}

// ChunkClaim is the verdict ChunkBuffer.ClaimSegment returns for an arriving
// chunk's byte range.
type ChunkClaim int

const (
	// ClaimNew means the range is disjoint from everything received so far
	// and has been recorded: the caller MUST copy the payload and advance
	// Received.
	ClaimNew ChunkClaim = iota
	// ClaimDuplicate means the exact same (offset, length) range was already
	// received — a benign retransmit (e.g. after a dropped ACK). The caller
	// MUST skip both the copy and the Received advance so the retransmit is
	// idempotent.
	ClaimDuplicate
	// ClaimOverlap means the range partially overlaps an already-received
	// range (including the same offset with a different length). This is a
	// PROTOCOL VIOLATION, not a retransmit: the caller MUST drop the whole
	// transfer and answer shm.MsgTypeSystemError.
	ClaimOverlap
)

// ChunkBuffer accumulates the payload of an inbound chunked message until all
// chunks have arrived. Concurrency: all field reads/writes happen under
// Mutex; the buffer pointer itself is published into ChunkManager.chunkCache
// under ChunkManager.chunkMutex.
//
// # Completion contract (mirrors shm SPECIFICATION.md §3.3.4)
//
// A transfer completes only when EVERY byte of [0, TotalSize) was written
// exactly once. This repo has no wire-level chunk index (protocol.Chunk carries
// only id/total_size/offset/data), so the equivalent is enforced structurally:
//
//  0. a zero-length segment is refused outright (HandleChunk): it advances
//     nothing, so it can never be part of a valid transfer, and recording an
//     (offset, 0) range would make the real chunk at that offset classify as
//     ClaimOverlap and kill the transfer,
//  1. every chunk is bounds-checked into [0, TotalSize) (HandleChunk),
//  2. Segments keeps the received ranges disjoint — an overlapping range is
//     REJECTED, never merged (ClaimSegment),
//  3. Received is the sum of the disjoint segment lengths, and completion
//     requires Received == TotalSize EXACTLY.
//
// Disjoint + in-bounds + Σ == TotalSize ⟹ full coverage, which is precisely
// shm's "every chunk exactly once AND Σ payloadSize == totalSize". The old
// `Received >= TotalSize` test was weaker: total=100 with (off=0,len=60) and
// (off=50,len=40) has two distinct offsets summing to 100, so it reported
// COMPLETE while [90,100) had never been written — the consumer would have
// silently read zero-fill.
//
// Zero-fill note (load-bearing, and why): Data is allocated with make(), so any
// region a producer never writes reads back as zeros rather than as leaked heap
// bytes. That is a real safety property here *only because* the coverage
// contract above is enforced — with it, the zero-fill is unreachable belt-and-
// braces; without it, zero-fill is the ONLY thing standing between a
// gap-leaving producer and an uninitialized-memory disclosure. shm's Go
// reassembler was able to prove zero-fill removable precisely because it
// already had this contract. Do not weaken one without the other. Keep in
// lockstep with the C++ mirror in internal/assets/files/src/xll_worker.cpp
// (CO-CHANGE ANCHOR, AGENTS.md §18.6).
//
// Dispatched guards against a second, concurrent observation of completion.
// When a retransmitted FINAL chunk races the original (e.g. after a dropped
// ACK), both goroutines can observe Received == TotalSize under Mutex. Only
// the goroutine that flips Dispatched from false to true (under Mutex) is
// allowed to call dispatch(); the loser returns without re-running the user
// function. Without this, the user function executes twice (side effects!)
// and two responses are written. See AGENTS.md §23.3.
type ChunkBuffer struct {
	Data      []byte
	TotalSize int
	Received  int
	// Segments are the byte ranges written into Data so far, kept ascending
	// by Offset and pairwise disjoint by ClaimSegment. Guarded by Mutex.
	Segments   []ChunkSegment
	Dispatched bool
	Mutex      sync.Mutex
	LastAccess time.Time
}

// ClaimSegment classifies the arriving range [offset, offset+length) against
// the ranges already received, recording it when it is new. The caller MUST
// hold b.Mutex.
//
// The range must already have been bounds-checked against len(b.Data) AND have
// a non-zero length (HandleChunk enforces both); this method only arbitrates
// overlap. A zero-length range would be recorded as a legal segment here and
// then collide with the real chunk at the same offset — hence the caller-side
// refusal. Segments is kept sorted so the check is a binary search plus two
// neighbour comparisons rather than a linear scan of every prior chunk.
func (b *ChunkBuffer) ClaimSegment(offset, length uint32) ChunkClaim {
	i := sort.Search(len(b.Segments), func(k int) bool {
		return b.Segments[k].Offset >= offset
	})

	// Same start offset: an exact-length match is the retransmit case; any
	// other length is a producer that re-chunked mid-transfer, which would
	// silently change which bytes are covered. Refuse it.
	if i < len(b.Segments) && b.Segments[i].Offset == offset {
		if b.Segments[i].Length == length {
			return ClaimDuplicate
		}
		return ClaimOverlap
	}
	// Predecessor must end at or before us. uint64 arithmetic: offset+length
	// is uint32-representable on the wire but their sum is not.
	if i > 0 {
		p := b.Segments[i-1]
		if uint64(p.Offset)+uint64(p.Length) > uint64(offset) {
			return ClaimOverlap
		}
	}
	// We must end at or before the successor's start.
	if i < len(b.Segments) && uint64(offset)+uint64(length) > uint64(b.Segments[i].Offset) {
		return ClaimOverlap
	}

	b.Segments = append(b.Segments, ChunkSegment{})
	copy(b.Segments[i+1:], b.Segments[i:])
	b.Segments[i] = ChunkSegment{Offset: offset, Length: length}
	return ClaimNew
}

// OutgoingChunk holds an in-progress outbound chunked message awaiting ACKs.
// Offset is the byte index of the next chunk to send; it MUST be published
// BEFORE the OutgoingChunk pointer becomes reachable through chunkManager's
// outgoing map (see AGENTS.md §23.3 publication-order race). Id is the
// chunked-message correlation key; MsgType is the eventual user-visible
// message type emitted with the final chunk.
type OutgoingChunk struct {
	Data       []byte
	Offset     int
	Id         uint64
	MsgType    uint32
	LastAccess time.Time
}

// QueuedCommand is a single batched command from the Go server to the XLL
// host (Set value, Format cell). CmdType discriminates the kind; the Data
// slice carries the serialized request, while the Optimized fields below
// allow the consumer to avoid re-parsing payloads it already shaped during
// enqueue.
type QueuedCommand struct {
	CmdType int // 0: Set, 1: Format
	Data    []byte

	// Optimized Intermediate Data (avoids pre-serialization)
	Sheet     string
	Rects     []algo.Rect
	ScalarVal ScalarValue
	FormatStr string
}

// PendingAsyncResult is one async return waiting to be flushed by the
// AsyncBatcher. Handle is the XLOPER12 async handle blob Excel hands the XLL
// at call time; Val/ValType carry the user value (or are zero when Err is
// non-empty). The batcher coalesces these into a single MsgBatchAsyncResponse.
type PendingAsyncResult struct {
	Handle  []byte
	Val     any
	ValType AnyValue
	Err     string
}

// CommandContext carries invocation metadata to a user command handler
// (ribbon button click, keyboard shortcut, or typed macro name).
type CommandContext struct {
	// CommandName is the invoked commands[].name from xll.yaml.
	CommandName string
	// ControlID is the clicked ribbon control id ("" for shortcut/Alt+F8).
	ControlID string
	// ExcelPID is the parent Excel process id, for multi-instance COM attach.
	ExcelPID uint32
}
