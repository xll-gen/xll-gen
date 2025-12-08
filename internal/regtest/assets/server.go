package main

import (
	"context"
	"fmt"
	"smoke_proj/generated"
	"smoke_proj/generated/ipc/types"
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

// EchoIntOpt returns the input optional integer.
func (s *Service) EchoIntOpt(ctx context.Context, val *int32) (*int32, error) { return val, nil }

// EchoFloatOpt returns the input optional float.
func (s *Service) EchoFloatOpt(ctx context.Context, val *float64) (*float64, error) { return val, nil }

// EchoBoolOpt returns the input optional boolean.
func (s *Service) EchoBoolOpt(ctx context.Context, val *bool) (*bool, error) { return val, nil }

// AsyncEchoInt waits briefly and returns the input integer, simulating async work.
func (s *Service) AsyncEchoInt(ctx context.Context, val int32) (int32, error) {
	time.Sleep(10 * time.Millisecond)
	return val, nil
}

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

// CheckAny inspects the generic Any value and returns a string description of its type and content.
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

func (s *Service) OnCalculationEnded(ctx context.Context) error {
    return nil
}

func main() {
	  generated.Serve(&Service{})
}
