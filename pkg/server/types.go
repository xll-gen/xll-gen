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
	MsgAck                 = 2
	MsgBatchAsyncResponse  = 128
	MsgChunk               = 129
	MsgSetRefCache         = 130
	MsgCalculationEnded    = 131
	MsgCalculationCanceled = 132
	MsgUserStart           = 133
)

type ChunkBuffer struct {
	Data       []byte
	TotalSize  int
	Received   int
	Mutex      sync.Mutex
	LastAccess time.Time
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
