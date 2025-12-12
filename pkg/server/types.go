package server

import (
	"sync"
	"time"
)

type AnyValue byte

const (
	AnyValueNone        AnyValue = 0
	AnyValueBool        AnyValue = 1
	AnyValueNum         AnyValue = 2
	AnyValueInt         AnyValue = 3
	AnyValueStr         AnyValue = 4
	AnyValueErr         AnyValue = 5
	AnyValueAsyncHandle AnyValue = 6
	AnyValueNil         AnyValue = 7
	AnyValueGrid        AnyValue = 8
	AnyValueNumGrid     AnyValue = 9
	AnyValueRange       AnyValue = 10
	AnyValueRefCache    AnyValue = 11
)

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
}
