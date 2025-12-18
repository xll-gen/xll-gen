package server

import (
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
)

func createRange(b *flatbuffers.Builder, rFirst, rLast, cFirst, cLast int32) flatbuffers.UOffsetT {
	protocol.RangeStartRefsVector(b, 1)
	protocol.CreateRect(b, rFirst, rLast, cFirst, cLast)
	refs := b.EndVector(1)

	sOff := b.CreateString("Sheet1")
	protocol.RangeStart(b)
	protocol.RangeAddSheetName(b, sOff)
	protocol.RangeAddRefs(b, refs)
	return protocol.RangeEnd(b)
}

func createScalarAny(b *flatbuffers.Builder, val int32) flatbuffers.UOffsetT {
	protocol.IntStart(b)
	protocol.IntAddVal(b, val)
	off := protocol.IntEnd(b)

	protocol.AnyStart(b)
	protocol.AnyAddValType(b, protocol.AnyValueInt)
	protocol.AnyAddVal(b, off)
	return protocol.AnyEnd(b)
}

func BenchmarkScheduleSet_Large(b *testing.B) {
	// 1000x1000 = 1,000,000 cells
	builder := flatbuffers.NewBuilder(1024)

	// Create Range
	rOff := createRange(builder, 0, 999, 0, 999)
	builder.Finish(rOff)
	rBuf := make([]byte, len(builder.FinishedBytes()))
	copy(rBuf, builder.FinishedBytes())
	rng := protocol.GetRootAsRange(rBuf, 0)

	// Create Value
	builder.Reset()
	vOff := createScalarAny(builder, 123)
	builder.Finish(vOff)
	vBuf := make([]byte, len(builder.FinishedBytes()))
	copy(vBuf, builder.FinishedBytes())
	val := protocol.GetRootAsAny(vBuf, 0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cb := NewCommandBatcher()
		cb.ScheduleSet(rng, val)
	}
}

func TestCommandBatcher_Mixed(t *testing.T) {
	cb := NewCommandBatcher()
	builder := flatbuffers.NewBuilder(1024)

	// Small Range (A1)
	builder.Reset()
	rSmallOff := createRange(builder, 0, 0, 0, 0)
	builder.Finish(rSmallOff)
	rSmallBuf := make([]byte, len(builder.FinishedBytes()))
	copy(rSmallBuf, builder.FinishedBytes())
	rSmall := protocol.GetRootAsRange(rSmallBuf, 0)

	// Large Range (A2:Z1000) > 1024 cells
	builder.Reset()
	rLargeOff := createRange(builder, 1, 999, 0, 25) // 999 rows * 26 cols = 25974 cells
	builder.Finish(rLargeOff)
	rLargeBuf := make([]byte, len(builder.FinishedBytes()))
	copy(rLargeBuf, builder.FinishedBytes())
	rLarge := protocol.GetRootAsRange(rLargeBuf, 0)

	// Value
	builder.Reset()
	vOff := createScalarAny(builder, 10)
	builder.Finish(vOff)
	vBuf := make([]byte, len(builder.FinishedBytes()))
	copy(vBuf, builder.FinishedBytes())
	val := protocol.GetRootAsAny(vBuf, 0)

	// Schedule Small (Buffered)
	cb.ScheduleSet(rSmall, val)

	// Schedule Large (Queued, should flush buffer first)
	cb.ScheduleSet(rLarge, val)

	// Flush
	builder.Reset()
	respBytes := cb.FlushCommands(builder)

	resp := protocol.GetRootAsCalculationEndedResponse(respBytes, 0)
	if resp.CommandsLength() != 2 {
		t.Fatalf("Expected 2 commands, got %d", resp.CommandsLength())
	}

	// Verify Order: Small then Large
	// Command 0 (Small)
	var wrapper0 protocol.CommandWrapper
	resp.Commands(&wrapper0, 0)

	var setCmd0 protocol.SetCommand
	unionTable0 := new(flatbuffers.Table)
	if wrapper0.Cmd(unionTable0) {
		setCmd0.Init(unionTable0.Bytes, unionTable0.Pos)
		target := setCmd0.Target(nil)
		var rect protocol.Rect
		if target.Refs(&rect, 0) {
			if rect.RowFirst() != 0 { // A1 is 0,0
				t.Errorf("Expected first command to be A1 (Row 0), got Row %d", rect.RowFirst())
			}
		}
	}

	// Command 1 (Large)
	var wrapper1 protocol.CommandWrapper
	resp.Commands(&wrapper1, 1)

	var setCmd1 protocol.SetCommand
	unionTable1 := new(flatbuffers.Table)
	if wrapper1.Cmd(unionTable1) {
		setCmd1.Init(unionTable1.Bytes, unionTable1.Pos)
		target := setCmd1.Target(nil)
		var rect protocol.Rect
		if target.Refs(&rect, 0) {
			if rect.RowFirst() != 1 { // A2 is Row 1
				t.Errorf("Expected second command to be A2... (Row 1), got Row %d", rect.RowFirst())
			}
		}
	}
}
