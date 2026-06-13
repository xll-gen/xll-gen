package server

import (
	"sync"
	"time"

	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/pkg/algo"
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
// transfers to the host. It is derived from the 1 MiB SHM slot payload
// (hostCfg.payloadSize in xll_main.cpp.tmpl) minus FlatBuffers/chunk-header
// overhead. It is the UPPER BOUND on a chunk; the actual budget is derived
// per-call from the real respBuf size via ChunkBudget so a smaller slot
// payload chunks correctly instead of overflowing.
const DefaultChunkSize = 950 * 1024

// ChunkFramingOverhead is a conservative upper bound on the bytes
// BuildChunkResponse adds around the raw chunk payload (FlatBuffers vtable +
// table fields id/total_size/offset/msg_type + the data vector header +
// alignment + root offset). The real overhead is well under 100 bytes; 256
// leaves margin so a chunk derived from respBuf via ChunkBudget always fits
// after framing. The post-build `len(chunkPayload) > len(respBuf)` check in
// SendAckOrChunk remains as a backstop.
const ChunkFramingOverhead = 256

// ChunkBudget returns the per-chunk payload size that is guaranteed to fit in
// respBuf after framing, capped at DefaultChunkSize. Previously the chunk size
// was hardcoded to DefaultChunkSize regardless of the actual response buffer,
// so a slot payload smaller than ~950 KiB + overhead made every chunk send
// fail the framing check. Deriving from len(respBuf) makes chunking adapt to
// the real slot geometry; the cap keeps memory bounded for large slots and
// preserves the historical 950 KiB behaviour for the default 1 MiB slot. Both
// the initial send (SendAckOrChunk) and the resend path (HandleAck) call this
// with the same respBuf, so chunk boundaries stay consistent.
func ChunkBudget(respBuf []byte) int {
	budget := len(respBuf) - ChunkFramingOverhead
	if budget > DefaultChunkSize {
		budget = DefaultChunkSize
	}
	if budget < 1 {
		budget = 1
	}
	return budget
}

// ChunkBuffer accumulates the payload of an inbound chunked message until all
// chunks have arrived. Concurrency: all field reads/writes happen under
// Mutex; the buffer pointer itself is published into ChunkManager.chunkCache
// under ChunkManager.chunkMutex.
//
// Received tracks the number of payload bytes copied into Data. It MUST be
// bumped at most once per chunk offset (i.e. retransmitted chunks are
// idempotent and do not advance Received). The dedup is enforced by
// ReceivedOffsets: HandleChunk skips the copy + Received++ when the offset
// is already marked received. Without this, a duplicate of the first chunk
// in a multi-chunk message would push Received past TotalSize and trigger
// premature completion with the trailing bytes still zero — a data
// corruption hazard. See AGENTS.md §23.3.
//
// Dispatched guards against a second, concurrent observation of completion.
// When a retransmitted FINAL chunk races the original (e.g. after a dropped
// ACK), both goroutines can observe Received >= TotalSize under Mutex. Only
// the goroutine that flips Dispatched from false to true (under Mutex) is
// allowed to call dispatch(); the loser returns without re-running the user
// function. Without this, the user function executes twice (side effects!)
// and two responses are written. See AGENTS.md §23.3.
type ChunkBuffer struct {
	Data            []byte
	TotalSize       int
	Received        int
	ReceivedOffsets map[uint32]bool
	Dispatched      bool
	Mutex           sync.Mutex
	LastAccess      time.Time
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
