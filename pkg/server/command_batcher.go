package server

import (
	"sync"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/xll-gen/pkg/algo"
	"github.com/xll-gen/xll-gen/pkg/protocol"
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
	rOff := CloneRange(b, r)
	vOff := CloneAny(b, v)

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

func (cb *CommandBatcher) flushBuffers() {
	cb.bufferLock.Lock()
	defer cb.bufferLock.Unlock()

	// Process Sets
	for sheet, cells := range cb.bufferedSets {
		byVal := make(map[ScalarValue][]algo.Cell)
		for cell, val := range cells {
			byVal[val] = append(byVal[val], cell)
		}

		for val, cellList := range byVal {
			rects := algo.GreedyMesh(cellList)

			// Chunk by 32
			for i := 0; i < len(rects); i += 32 {
				end := i + 32
				if end > len(rects) {
					end = len(rects)
				}
				batch := rects[i:end]

				cb.cmdQueueLock.Lock()
				cb.cmdQueue = append(cb.cmdQueue, QueuedCommand{
					CmdType:   cmdTypeSet,
					Sheet:     sheet,
					Rects:     batch,
					ScalarVal: val,
				})
				cb.cmdQueueLock.Unlock()
			}
		}
		delete(cb.bufferedSets, sheet)
	}

	// Process Formats
	for sheet, cells := range cb.bufferedFormats {
		byFmt := make(map[string][]algo.Cell)
		for cell, fmt := range cells {
			byFmt[fmt] = append(byFmt[fmt], cell)
		}

		for fmt, cellList := range byFmt {
			rects := algo.GreedyMesh(cellList)

			for i := 0; i < len(rects); i += 32 {
				end := i + 32
				if end > len(rects) {
					end = len(rects)
				}
				batch := rects[i:end]

				cb.cmdQueueLock.Lock()
				cb.cmdQueue = append(cb.cmdQueue, QueuedCommand{
					CmdType:   cmdTypeFormat,
					Sheet:     sheet,
					Rects:     batch,
					FormatStr: fmt,
				})
				cb.cmdQueueLock.Unlock()
			}
		}
		delete(cb.bufferedFormats, sheet)
	}
}
