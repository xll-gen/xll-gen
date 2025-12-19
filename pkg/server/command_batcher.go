package server

import (
	"sync"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/xll-gen/pkg/algo"
	"github.com/xll-gen/types/go/protocol"
)

const (
	batchingThreshold = 1024
	cmdTypeSet        = 0
	cmdTypeFormat     = 1
)

type CommandBatcher struct {
	bufferedSets    map[string]map[algo.Cell]ScalarValue
	bufferedFormats map[string]map[algo.Cell]string
	bufferLock      sync.Mutex

	cmdQueue     []QueuedCommand
	cmdQueueLock sync.Mutex
}

func NewCommandBatcher() *CommandBatcher {
	return &CommandBatcher{
		bufferedSets:    make(map[string]map[algo.Cell]ScalarValue),
		bufferedFormats: make(map[string]map[algo.Cell]string),
	}
}

func (cb *CommandBatcher) Clear() {
	cb.cmdQueueLock.Lock()
	cb.cmdQueue = nil
	cb.cmdQueueLock.Unlock()

	cb.bufferLock.Lock()
	cb.bufferedSets = make(map[string]map[algo.Cell]ScalarValue)
	cb.bufferedFormats = make(map[string]map[algo.Cell]string)
	cb.bufferLock.Unlock()
}

func calculateTotalCells(r *protocol.Range) int64 {
	var totalCells int64
	l := r.RefsLength()
	for i := 0; i < l; i++ {
		var rect protocol.Rect
		if r.Refs(&rect, i) {
			rows := int64(rect.RowLast()) - int64(rect.RowFirst()) + 1
			cols := int64(rect.ColLast()) - int64(rect.ColFirst()) + 1
			totalCells += rows * cols
		}
	}
	return totalCells
}

func extractAlgoRects(r *protocol.Range) []algo.Rect {
	var rects []algo.Rect
	l := r.RefsLength()
	for i := 0; i < l; i++ {
		var rProto protocol.Rect
		if r.Refs(&rProto, i) {
			rects = append(rects, algo.Rect{
				RowFirst: rProto.RowFirst(),
				RowLast:  rProto.RowLast(),
				ColFirst: rProto.ColFirst(),
				ColLast:  rProto.ColLast(),
			})
		}
	}
	return rects
}

func (cb *CommandBatcher) ScheduleSet(r *protocol.Range, v *protocol.Any) {
	scalar, ok := ToScalar(v)
	if ok {
		// Optimization: If range is large, bypass buffer to avoid O(Cells) decomposition
		totalCells := calculateTotalCells(r)
		if totalCells > batchingThreshold {
			cb.flushBuffers()

			rects := extractAlgoRects(r)

			cb.cmdQueueLock.Lock()
			cb.cmdQueue = append(cb.cmdQueue, QueuedCommand{
				CmdType:   cmdTypeSet,
				Sheet:     string(r.SheetName()),
				Rects:     rects,
				ScalarVal: scalar,
			})
			cb.cmdQueueLock.Unlock()
			return
		}

		sheet := string(r.SheetName())
		cb.bufferLock.Lock()
		if cb.bufferedSets[sheet] == nil {
			cb.bufferedSets[sheet] = make(map[algo.Cell]ScalarValue)
		}

		l := r.RefsLength()
		for i := 0; i < l; i++ {
			var rect protocol.Rect
			if r.Refs(&rect, i) {
				for row := rect.RowFirst(); row <= rect.RowLast(); row++ {
					for col := rect.ColFirst(); col <= rect.ColLast(); col++ {
						cb.bufferedSets[sheet][algo.Cell{Row: row, Col: col}] = scalar
					}
				}
			}
		}
		cb.bufferLock.Unlock()
		return
	}

	cb.flushBuffers()

	b := flatbuffers.NewBuilder(0)
	rOff := r.DeepCopy(b)
	vOff := v.DeepCopy(b)

	protocol.SetCommandStart(b)
	protocol.SetCommandAddTarget(b, rOff)
	protocol.SetCommandAddValue(b, vOff)
	root := protocol.SetCommandEnd(b)
	b.Finish(root)

	cb.cmdQueueLock.Lock()
	cb.cmdQueue = append(cb.cmdQueue, QueuedCommand{CmdType: cmdTypeSet, Data: b.FinishedBytes()})
	cb.cmdQueueLock.Unlock()
}

func (cb *CommandBatcher) ScheduleFormat(r *protocol.Range, fmtStr string) {
	totalCells := calculateTotalCells(r)
	if totalCells > batchingThreshold {
		cb.flushBuffers()

		rects := extractAlgoRects(r)

		cb.cmdQueueLock.Lock()
		cb.cmdQueue = append(cb.cmdQueue, QueuedCommand{
			CmdType:   cmdTypeFormat,
			Sheet:     string(r.SheetName()),
			Rects:     rects,
			FormatStr: fmtStr,
		})
		cb.cmdQueueLock.Unlock()
		return
	}

	sheet := string(r.SheetName())
	cb.bufferLock.Lock()
	if cb.bufferedFormats[sheet] == nil {
		cb.bufferedFormats[sheet] = make(map[algo.Cell]string)
	}

	l := r.RefsLength()
	for i := 0; i < l; i++ {
		var rect protocol.Rect
		if r.Refs(&rect, i) {
			for row := rect.RowFirst(); row <= rect.RowLast(); row++ {
				for col := rect.ColFirst(); col <= rect.ColLast(); col++ {
					cb.bufferedFormats[sheet][algo.Cell{Row: row, Col: col}] = fmtStr
				}
			}
		}
	}
	cb.bufferLock.Unlock()
}
