package main

import (
	"context"
	"fmt"
	"smoke_proj/generated"
	types "github.com/xll-gen/types/go/protocol"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
)

// Force usage
var _ = types.Bool{}

// Service implements the XllService interface for regression testing.
type Service struct{}

// EchoInt returns the input integer.
func (s *Service) EchoInt(ctx context.Context, val int32) (int32, error) { return val, nil }

// EchoFloat returns the input float.
func (s *Service) EchoFloat(ctx context.Context, val float64) (float64, error) { return val, nil }

// EchoString returns the input string.
func (s *Service) EchoString(ctx context.Context, val string) (string, error) { return val, nil }

// EchoBool returns the input boolean.
func (s *Service) EchoBool(ctx context.Context, val bool) (bool, error) { return val, nil }

// TimeoutFunc waits for 500ms, which exceeds the configured timeout, returning -1 on cancellation.
func (s *Service) TimeoutFunc(ctx context.Context, val int32) (int32, error) {
	select {
	case <-time.After(500 * time.Millisecond):
		return val, nil
	case <-ctx.Done():
		// Return fallback value on timeout
		return -1, nil
	}
}

// AsyncEchoInt returns the input integer asynchronously.
func (s *Service) AsyncEchoInt(ctx context.Context, val int32) (int32, error) {
	fmt.Printf("SERVER: AsyncEchoInt val=%d\n", val)
	return val, nil
}

// CheckAny inspects the generic Any value and returns a string description of the type and content.
func (s *Service) CheckAny(ctx context.Context, val *types.Any) (string, error) {
	if val == nil {
		return "Nil", nil
	}
	var tbl flatbuffers.Table
	if !val.Val(&tbl) {
		return "Unknown", nil
	}

	switch val.ValType() {
	case types.AnyValueInt:
		var t types.Int
		t.Init(tbl.Bytes, tbl.Pos)
		return fmt.Sprintf("Int:%d", t.Val()), nil
	case types.AnyValueNum:
		var t types.Num
		t.Init(tbl.Bytes, tbl.Pos)
		return fmt.Sprintf("Num:%.1f", t.Val()), nil
	case types.AnyValueStr:
		var t types.Str
		t.Init(tbl.Bytes, tbl.Pos)
		return fmt.Sprintf("Str:%s", string(t.Val())), nil
	case types.AnyValueBool:
		var t types.Bool
		t.Init(tbl.Bytes, tbl.Pos)
		return fmt.Sprintf("Bool:%v", t.Val()), nil
	case types.AnyValueErr:
		var t types.Err
		t.Init(tbl.Bytes, tbl.Pos)
		return fmt.Sprintf("Err:%d", t.Val()), nil
	case types.AnyValueNil:
		return "Nil", nil
	case types.AnyValueNumGrid:
		var t types.NumGrid
		t.Init(tbl.Bytes, tbl.Pos)
		return fmt.Sprintf("NumGrid:%dx%d", t.Rows(), t.Cols()), nil
	case types.AnyValueGrid:
		var t types.Grid
		t.Init(tbl.Bytes, tbl.Pos)
		return fmt.Sprintf("Grid:%dx%d", t.Rows(), t.Cols()), nil
	case types.AnyValueRefCache:
		var t types.RefCache
		t.Init(tbl.Bytes, tbl.Pos)
		return fmt.Sprintf("RefCache:%s", string(t.Key())), nil
	}
	return "Unknown", nil
}

// CheckRange inspects the Range value and returns a string description of the sheet and reference.
func (s *Service) CheckRange(ctx context.Context, val *types.Range) (string, error) {
	if val == nil {
		return "Nil", nil
	}
	sheet := string(val.SheetName())
	if val.RefsLength() > 0 {
		var r types.Rect
		if val.Refs(&r, 0) {
			return fmt.Sprintf("Range:%s!%d:%d:%d:%d", sheet, r.RowFirst(), r.RowLast(), r.ColFirst(), r.ColLast()), nil
		}
	}
	return "RangeEmpty", nil
}

func (s *Service) ScheduleCmd(ctx context.Context) (int32, error) {
    fmt.Println("SERVER: ScheduleCmd executing")
    // Schedule Set Sheet1!0:0:0:0 = 100
    b := flatbuffers.NewBuilder(0)

    // Create Range
    sOff := b.CreateString("Sheet1")
    types.RangeStartRefsVector(b, 1)
    types.CreateRect(b, 0, 0, 0, 0)
    refsOff := b.EndVector(1)
    types.RangeStart(b)
    types.RangeAddSheetName(b, sOff)
    types.RangeAddRefs(b, refsOff)
    rOff := types.RangeEnd(b)
    b.Finish(rOff)
    r := types.GetRootAsRange(b.FinishedBytes(), 0)

    // Create Value
    b2 := flatbuffers.NewBuilder(0)
    types.IntStart(b2)
    types.IntAddVal(b2, 100)
    iOff := types.IntEnd(b2)
    types.AnyStart(b2)
    types.AnyAddValType(b2, types.AnyValueInt)
    types.AnyAddVal(b2, iOff)
    aOff := types.AnyEnd(b2)
    b2.Finish(aOff)
    v := types.GetRootAsAny(b2.FinishedBytes(), 0)

    generated.ScheduleSet(r, v)
    return 1, nil
}

func (s *Service) ScheduleFormatCmd(ctx context.Context) (int32, error) {
    // Schedule Format Sheet1!1:1:1:1 = "General"
    b := flatbuffers.NewBuilder(0)

    sOff := b.CreateString("Sheet1")
    types.RangeStartRefsVector(b, 1)
    types.CreateRect(b, 1, 1, 1, 1)
    refsOff := b.EndVector(1)
    types.RangeStart(b)
    types.RangeAddSheetName(b, sOff)
    types.RangeAddRefs(b, refsOff)
    rOff := types.RangeEnd(b)
    b.Finish(rOff)
    r := types.GetRootAsRange(b.FinishedBytes(), 0)

    generated.ScheduleFormat(r, "General")
    return 1, nil
}

func (s *Service) ScheduleMultiCmd(ctx context.Context) (int32, error) {
    // 1. Set 200
    {
        b := flatbuffers.NewBuilder(0)
        sOff := b.CreateString("Sheet1")
        types.RangeStartRefsVector(b, 1)
        types.CreateRect(b, 0, 0, 0, 0)
        refsOff := b.EndVector(1)
        types.RangeStart(b)
        types.RangeAddSheetName(b, sOff)
        types.RangeAddRefs(b, refsOff)
        rOff := types.RangeEnd(b)
        b.Finish(rOff)
        r := types.GetRootAsRange(b.FinishedBytes(), 0)

        b2 := flatbuffers.NewBuilder(0)
        types.IntStart(b2)
        types.IntAddVal(b2, 200)
        iOff := types.IntEnd(b2)
        types.AnyStart(b2)
        types.AnyAddValType(b2, types.AnyValueInt)
        types.AnyAddVal(b2, iOff)
        aOff := types.AnyEnd(b2)
        b2.Finish(aOff)
        v := types.GetRootAsAny(b2.FinishedBytes(), 0)
        generated.ScheduleSet(r, v)
    }

    // 2. Format "Number"
    {
        b := flatbuffers.NewBuilder(0)
        sOff := b.CreateString("Sheet1")
        types.RangeStartRefsVector(b, 1)
        types.CreateRect(b, 0, 0, 0, 0)
        refsOff := b.EndVector(1)
        types.RangeStart(b)
        types.RangeAddSheetName(b, sOff)
        types.RangeAddRefs(b, refsOff)
        rOff := types.RangeEnd(b)
        b.Finish(rOff)
        r := types.GetRootAsRange(b.FinishedBytes(), 0)
        generated.ScheduleFormat(r, "Number")
    }

    return 2, nil
}

func (s *Service) ScheduleMassive(ctx context.Context) (int32, error) {
    // 10x10 Checkerboard
    for r := 0; r < 10; r++ {
        for c := 0; c < 10; c++ {
            val := int32(100)
            if (r+c)%2 == 0 {
                val = 200
            }

            b := flatbuffers.NewBuilder(0)
            sOff := b.CreateString("Sheet1")
            types.RangeStartRefsVector(b, 1)
            types.CreateRect(b, int32(10+r), int32(10+r), int32(10+c), int32(10+c))
            refsOff := b.EndVector(1)
            types.RangeStart(b)
            types.RangeAddSheetName(b, sOff)
            types.RangeAddRefs(b, refsOff)
            rOff := types.RangeEnd(b)
            b.Finish(rOff)
            rng := types.GetRootAsRange(b.FinishedBytes(), 0)

            b2 := flatbuffers.NewBuilder(0)
            types.IntStart(b2)
            types.IntAddVal(b2, val)
            iOff := types.IntEnd(b2)
            types.AnyStart(b2)
            types.AnyAddValType(b2, types.AnyValueInt)
            types.AnyAddVal(b2, iOff)
            aOff := types.AnyEnd(b2)
            b2.Finish(aOff)
            v := types.GetRootAsAny(b2.FinishedBytes(), 0)

            generated.ScheduleSet(rng, v)
        }
    }
    return 100, nil
}

func (s *Service) ScheduleGridCmd(ctx context.Context) (int32, error) {
    b := flatbuffers.NewBuilder(0)

    // Create Range
    sOff := b.CreateString("Sheet1")
    types.RangeStartRefsVector(b, 1)
    types.CreateRect(b, 2, 3, 2, 3) // 2x2 area
    refsOff := b.EndVector(1)
    types.RangeStart(b)
    types.RangeAddSheetName(b, sOff)
    types.RangeAddRefs(b, refsOff)
    rOff := types.RangeEnd(b)
    b.Finish(rOff)
    r := types.GetRootAsRange(b.FinishedBytes(), 0)

    // Create Grid
    // [[1, 2], [3, 4]]

    // 1
    types.IntStart(b)
    types.IntAddVal(b, 1)
    v1 := types.IntEnd(b)
    types.ScalarStart(b)
    types.ScalarAddValType(b, types.ScalarValueInt)
    types.ScalarAddVal(b, v1)
    scalar1 := types.ScalarEnd(b)

    // 2
    types.IntStart(b)
    types.IntAddVal(b, 2)
    v2 := types.IntEnd(b)
    types.ScalarStart(b)
    types.ScalarAddValType(b, types.ScalarValueInt)
    types.ScalarAddVal(b, v2)
    scalar2 := types.ScalarEnd(b)

    // 3
    types.IntStart(b)
    types.IntAddVal(b, 3)
    v3 := types.IntEnd(b)
    types.ScalarStart(b)
    types.ScalarAddValType(b, types.ScalarValueInt)
    types.ScalarAddVal(b, v3)
    scalar3 := types.ScalarEnd(b)

    // 4
    types.IntStart(b)
    types.IntAddVal(b, 4)
    v4 := types.IntEnd(b)
    types.ScalarStart(b)
    types.ScalarAddValType(b, types.ScalarValueInt)
    types.ScalarAddVal(b, v4)
    scalar4 := types.ScalarEnd(b)

    types.GridStartDataVector(b, 4)
    b.PrependUOffsetT(scalar4)
    b.PrependUOffsetT(scalar3)
    b.PrependUOffsetT(scalar2)
    b.PrependUOffsetT(scalar1)
    dataOff := b.EndVector(4)

    types.GridStart(b)
    types.GridAddRows(b, 2)
    types.GridAddCols(b, 2)
    types.GridAddData(b, dataOff)
    gOff := types.GridEnd(b)

    // Wrap in Any
    types.AnyStart(b)
    types.AnyAddValType(b, types.AnyValueGrid)
    types.AnyAddVal(b, gOff)
    anyOff := types.AnyEnd(b)
    b.Finish(anyOff)

    v := types.GetRootAsAny(b.FinishedBytes(), 0)

    generated.ScheduleSet(r, v)
    return 1, nil
}

func (s *Service) OnCalculationEnded(ctx context.Context) error {
    return nil
}

func (s *Service) OnCalculationCanceled(ctx context.Context) error {
    return nil
}

func main() {
	  generated.Serve(&Service{})
}
