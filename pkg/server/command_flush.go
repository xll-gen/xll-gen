package server

import (
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/xll-gen/pkg/algo"
	"github.com/xll-gen/types/go/protocol"
)

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

func (cb *CommandBatcher) FlushCommands(b *flatbuffers.Builder) []byte {
	cb.flushBuffers()
	cb.cmdQueueLock.Lock()
	defer cb.cmdQueueLock.Unlock()

	if len(cb.cmdQueue) == 0 {
		return nil
	}

	wrappers := make([]flatbuffers.UOffsetT, len(cb.cmdQueue))

	for i, c := range cb.cmdQueue {
		var uOff flatbuffers.UOffsetT
		var uType protocol.Command

		if c.Data == nil {
			// Optimized Path
			sOff := b.CreateString(c.Sheet)

			protocol.RangeStartRefsVector(b, len(c.Rects))
			for j := len(c.Rects) - 1; j >= 0; j-- {
				protocol.CreateRect(b, c.Rects[j].RowFirst, c.Rects[j].RowLast, c.Rects[j].ColFirst, c.Rects[j].ColLast)
			}
			refsOff := b.EndVector(len(c.Rects))

			protocol.RangeStart(b)
			protocol.RangeAddSheetName(b, sOff)
			protocol.RangeAddRefs(b, refsOff)
			rOff := protocol.RangeEnd(b)

			if c.CmdType == cmdTypeSet { // Set
				vOff := CreateScalarAny(b, c.ScalarVal)
				protocol.SetCommandStart(b)
				protocol.SetCommandAddTarget(b, rOff)
				protocol.SetCommandAddValue(b, vOff)
				uOff = protocol.SetCommandEnd(b)
				uType = protocol.CommandSetCommand
			} else { // Format
				fOff := b.CreateString(c.FormatStr)
				protocol.FormatCommandStart(b)
				protocol.FormatCommandAddTarget(b, rOff)
				protocol.FormatCommandAddFormat(b, fOff)
				uOff = protocol.FormatCommandEnd(b)
				uType = protocol.CommandFormatCommand
			}
		} else {
			// Legacy / Complex Path
			if c.CmdType == cmdTypeSet {
				cmd := protocol.GetRootAsSetCommand(c.Data, 0)
				rOff := CloneRange(b, cmd.Target(nil))
				vOff := CloneAny(b, cmd.Value(nil))

				protocol.SetCommandStart(b)
				protocol.SetCommandAddTarget(b, rOff)
				protocol.SetCommandAddValue(b, vOff)
				uOff = protocol.SetCommandEnd(b)
				uType = protocol.CommandSetCommand
			} else {
				cmd := protocol.GetRootAsFormatCommand(c.Data, 0)
				rOff := CloneRange(b, cmd.Target(nil))
				fOff := b.CreateString(string(cmd.Format()))

				protocol.FormatCommandStart(b)
				protocol.FormatCommandAddTarget(b, rOff)
				protocol.FormatCommandAddFormat(b, fOff)
				uOff = protocol.FormatCommandEnd(b)
				uType = protocol.CommandFormatCommand
			}
		}

		protocol.CommandWrapperStart(b)
		protocol.CommandWrapperAddCmdType(b, uType)
		protocol.CommandWrapperAddCmd(b, uOff)
		wrappers[i] = protocol.CommandWrapperEnd(b)
	}

	protocol.CalculationEndedResponseStartCommandsVector(b, len(wrappers))
	for i := len(wrappers) - 1; i >= 0; i-- {
		b.PrependUOffsetT(wrappers[i])
	}
	cmdsOff := b.EndVector(len(wrappers))

	protocol.CalculationEndedResponseStart(b)
	protocol.CalculationEndedResponseAddCommands(b, cmdsOff)
	root := protocol.CalculationEndedResponseEnd(b)
	b.Finish(root)

	cb.cmdQueue = nil
	return b.FinishedBytes()
}
