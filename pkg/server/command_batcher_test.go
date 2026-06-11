package server

import (
	"sync"
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

// TestCommandBatcher_ConcurrentConsistency exercises the batcher the way the
// generated server actually drives it: async UDF worker goroutines call
// ScheduleSet/ScheduleFormat while a calc-boundary goroutine calls
// FlushCommands (calc-ended) or Clear (calc-canceled) concurrently. Each
// public method must be atomic as a whole — otherwise the flush-then-enqueue
// sequence interleaves with a concurrent Flush/Clear, reordering or leaking
// commands. Run under -race; the batcher must never panic, corrupt its maps,
// or emit an unparseable response.
func TestCommandBatcher_ConcurrentConsistency(t *testing.T) {
	cb := NewCommandBatcher()

	// Pre-build a small (buffered path) and a large (queued path) range plus a
	// scalar value, all immutable and shareable across goroutines.
	b := flatbuffers.NewBuilder(1024)
	rSmallOff := createRange(b, 0, 0, 0, 0)
	b.Finish(rSmallOff)
	rSmall := protocol.GetRootAsRange(append([]byte(nil), b.FinishedBytes()...), 0)

	b.Reset()
	rLargeOff := createRange(b, 1, 999, 0, 25) // > batchingThreshold
	b.Finish(rLargeOff)
	rLarge := protocol.GetRootAsRange(append([]byte(nil), b.FinishedBytes()...), 0)

	b.Reset()
	vOff := createScalarAny(b, 7)
	b.Finish(vOff)
	val := protocol.GetRootAsAny(append([]byte(nil), b.FinishedBytes()...), 0)

	const iters = 400
	var wg sync.WaitGroup

	// Schedulers (simulate async UDF workers).
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				cb.ScheduleSet(rSmall, val)
				cb.ScheduleSet(rLarge, val)
				cb.ScheduleFormat(rSmall, "0.00")
			}
		}()
	}

	// Calc-ended flusher: each FlushCommands result must be nil or a valid
	// CalculationEndedResponse.
	wg.Add(1)
	go func() {
		defer wg.Done()
		fb := flatbuffers.NewBuilder(1024)
		for i := 0; i < iters; i++ {
			fb.Reset()
			out := cb.FlushCommands(fb)
			if out != nil {
				resp := protocol.GetRootAsCalculationEndedResponse(out, 0)
				// Touching the union forces a parse; a corrupt buffer would
				// panic or yield a nonsensical length.
				if n := resp.CommandsLength(); n < 0 {
					t.Errorf("negative commands length %d", n)
				}
			}
		}
	}()

	// Calc-canceled clearer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			cb.Clear()
		}
	}()

	wg.Wait()
}
