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

// CommandBatcher accumulates Set/Format commands scheduled by user functions
// during a calculation cycle and flushes them into a single response at
// calc-end.
//
// Concurrency: the generated server calls ScheduleSet/ScheduleFormat from async
// UDF worker goroutines, while the calc-boundary handlers call FlushCommands
// (calc-ended) and Clear (calc-canceled) from the SHM dispatch goroutines —
// all genuinely concurrent. A single mutex guards both the per-cell buffer and
// the command queue so that every public method is atomic as a whole. This
// matters because each schedule of a large/non-scalar range first flushes the
// buffer and then enqueues its own command; with separate locks that two-step
// sequence could interleave with a concurrent Flush/Clear, reordering commands
// or leaking a command past a cancellation. The cost is that GreedyMesh runs
// under the lock (FlushCommands still serializes its FlatBuffer build outside
// the lock by swapping the queue to a local first).
//
// Note on async scheduling: a command scheduled after its cycle's calc-ended
// has already flushed lands in the buffer/queue and is emitted on the next
// calc-ended — it is deferred, not lost.
type CommandBatcher struct {
	mu              sync.Mutex
	bufferedSets    map[string]map[algo.Cell]ScalarValue
	bufferedFormats map[string]map[algo.Cell]string
	cmdQueue        []QueuedCommand
}

func NewCommandBatcher() *CommandBatcher {
	return &CommandBatcher{
		bufferedSets:    make(map[string]map[algo.Cell]ScalarValue),
		bufferedFormats: make(map[string]map[algo.Cell]string),
	}
}

func (cb *CommandBatcher) Clear() {
	cb.mu.Lock()
	cb.cmdQueue = nil
	cb.bufferedSets = make(map[string]map[algo.Cell]ScalarValue)
	cb.bufferedFormats = make(map[string]map[algo.Cell]string)
	cb.mu.Unlock()
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
			// Read the range outside the lock; flush+enqueue under it so the
			// "flush buffered cells, then append this command" pair is atomic.
			rects := extractAlgoRects(r)
			sheet := string(r.SheetName())

			cb.mu.Lock()
			cb.flushBuffersLocked()
			cb.cmdQueue = append(cb.cmdQueue, QueuedCommand{
				CmdType:   cmdTypeSet,
				Sheet:     sheet,
				Rects:     rects,
				ScalarVal: scalar,
			})
			cb.mu.Unlock()
			return
		}

		sheet := string(r.SheetName())
		cb.mu.Lock()
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
		cb.mu.Unlock()
		return
	}

	// Non-scalar: pre-serialize outside the lock (no shared state), then
	// flush+enqueue atomically.
	b := flatbuffers.NewBuilder(0)
	rOff := r.DeepCopy(b)
	vOff := v.DeepCopy(b)

	protocol.SetCommandStart(b)
	protocol.SetCommandAddTarget(b, rOff)
	protocol.SetCommandAddValue(b, vOff)
	root := protocol.SetCommandEnd(b)
	b.Finish(root)
	data := b.FinishedBytes()

	cb.mu.Lock()
	cb.flushBuffersLocked()
	cb.cmdQueue = append(cb.cmdQueue, QueuedCommand{CmdType: cmdTypeSet, Data: data})
	cb.mu.Unlock()
}

func (cb *CommandBatcher) ScheduleFormat(r *protocol.Range, fmtStr string) {
	totalCells := calculateTotalCells(r)
	if totalCells > batchingThreshold {
		rects := extractAlgoRects(r)
		sheet := string(r.SheetName())

		cb.mu.Lock()
		cb.flushBuffersLocked()
		cb.cmdQueue = append(cb.cmdQueue, QueuedCommand{
			CmdType:   cmdTypeFormat,
			Sheet:     sheet,
			Rects:     rects,
			FormatStr: fmtStr,
		})
		cb.mu.Unlock()
		return
	}

	sheet := string(r.SheetName())
	cb.mu.Lock()
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
	cb.mu.Unlock()
}
