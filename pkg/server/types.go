package server

import (
	"sync"
	"time"

	"github.com/xll-gen/xll-gen/pkg/algo"
	"github.com/xll-gen/types/go/protocol"
)

type AnyValue = protocol.AnyValue

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

type OutgoingChunk struct {
	Data       []byte
	Offset     int
	Id         uint64
	MsgType    uint32
	LastAccess time.Time
}

type QueuedCommand struct {
	CmdType int // 0: Set, 1: Format
	Data    []byte

	// Optimized Intermediate Data (avoids pre-serialization)
	Sheet     string
	Rects     []algo.Rect
	ScalarVal ScalarValue
	FormatStr string
}

type PendingAsyncResult struct {
	Handle  []byte
	Val     interface{}
	ValType AnyValue
	Err     string
}
