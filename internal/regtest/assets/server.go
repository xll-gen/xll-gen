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

type Service struct{}

func (s *Service) EchoInt(ctx context.Context, val int32) (int32, error) { return val, nil }
func (s *Service) EchoFloat(ctx context.Context, val float64) (float64, error) { return val, nil }
func (s *Service) EchoString(ctx context.Context, val string) (string, error) { return val, nil }
func (s *Service) EchoBool(ctx context.Context, val bool) (bool, error) { return val, nil }

func (s *Service) EchoIntOpt(ctx context.Context, val *int32) (*int32, error) { return val, nil }
func (s *Service) EchoFloatOpt(ctx context.Context, val *float64) (*float64, error) { return val, nil }
func (s *Service) EchoBoolOpt(ctx context.Context, val *bool) (*bool, error) { return val, nil }

func (s *Service) AsyncEchoInt(ctx context.Context, val int32) (int32, error) {
    time.Sleep(10 * time.Millisecond)
    return val, nil
}

func (s *Service) TimeoutFunc(ctx context.Context, val int32) (int32, error) {
    select {
    case <-time.After(500 * time.Millisecond):
        return val, nil
    case <-ctx.Done():
        // Return fallback value on timeout
        return -1, nil
    }
}

func (s *Service) CheckAny(ctx context.Context, val *types.Any) (string, error) {
    if val == nil { return "Nil", nil }
    var tbl flatbuffers.Table
    if !val.Val(&tbl) { return "Unknown", nil }

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

func (s *Service) CheckRange(ctx context.Context, val *types.Range) (string, error) {
    if val == nil { return "Nil", nil }
    sheet := string(val.SheetName())
    if val.RefsLength() > 0 {
        var r types.Rect
        if val.Refs(&r, 0) {
             return fmt.Sprintf("Range:%s!%d:%d:%d:%d", sheet, r.RowFirst(), r.RowLast(), r.ColFirst(), r.ColLast()), nil
        }
    }
    return "RangeEmpty", nil
}


func main() {
    generated.Serve(&Service{})
}
