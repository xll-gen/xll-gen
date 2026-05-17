package server

import (
	"sync"
	"time"

	"github.com/xll-gen/xll-gen/pkg/algo"
	"github.com/xll-gen/types/go/protocol"
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

const (
	MsgAck = 2
	// MsgSystemError signals the producer that the server refused or
	// failed to handle a request at the system level (e.g. an
	// allocation request exceeded ChunkManager.MaxChunkBufferBytes).
	// Value 127 mirrors shm.MsgTypeSystemError in shm@HEAD; we define
	// it locally so this package compiles against the currently
	// pinned shm module version. See pkg/server/handlers.go callers
	// and AGENTS.md §23.3.
	MsgSystemError         = 127
	MsgBatchAsyncResponse  = 128
	MsgChunk               = 129
	MsgSetRefCache         = 130
	MsgCalculationEnded    = 131
	MsgCalculationCanceled = 132
	// RTD Messages (133-139)
	MsgRtdConnect    = 133
	MsgRtdDisconnect = 134
	MsgRtdUpdate     = 135
	MsgRtdHeartbeat  = 136

	// User Messages Start
	MsgUserStart = 140
)

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
type ChunkBuffer struct {
	Data            []byte
	TotalSize       int
	Received        int
	ReceivedOffsets map[uint32]bool
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
