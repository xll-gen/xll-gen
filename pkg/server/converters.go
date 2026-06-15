package server

import (
	"fmt"
	"strconv"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/internal/fbany"
	"github.com/xll-gen/xll-gen/pkg/log"
	"github.com/xll-gen/xll-gen/pkg/xldate"
)

// SerialToTime converts an Excel date serial (wall-clock) to a time.Time. Used
// by generated code to decode `type: date` arguments.
func SerialToTime(serial float64) time.Time {
	return xldate.FromSerial(serial)
}

func ToScalar(v *protocol.Any) (ScalarValue, bool) {
	if v == nil {
		return ScalarValue{}, false
	}
	var tbl flatbuffers.Table
	if !v.Val(&tbl) {
		return ScalarValue{}, false
	}

	switch v.ValType() {
	case protocol.AnyValueInt:
		var t protocol.Int
		t.Init(tbl.Bytes, tbl.Pos)
		return ScalarValue{Type: protocol.AnyValueInt, Int: t.Val()}, true
	case protocol.AnyValueNum:
		var t protocol.Num
		t.Init(tbl.Bytes, tbl.Pos)
		return ScalarValue{Type: protocol.AnyValueNum, Num: t.Val()}, true
	case protocol.AnyValueBool:
		var t protocol.Bool
		t.Init(tbl.Bytes, tbl.Pos)
		return ScalarValue{Type: protocol.AnyValueBool, Bool: t.Val()}, true
	case protocol.AnyValueStr:
		var t protocol.Str
		t.Init(tbl.Bytes, tbl.Pos)
		return ScalarValue{Type: protocol.AnyValueStr, Str: string(t.Val())}, true
	case protocol.AnyValueErr:
		var t protocol.Err
		t.Init(tbl.Bytes, tbl.Pos)
		return ScalarValue{Type: protocol.AnyValueErr, Err: int16(t.Val())}, true
	case protocol.AnyValueDate:
		var t protocol.Date
		t.Init(tbl.Bytes, tbl.Pos)
		return ScalarValue{Type: protocol.AnyValueNum, Num: t.Serial()}, true
	}
	return ScalarValue{}, false
}

// ParseInt parses an RTD topic argument as an int32. On a malformed value it
// returns 0 and logs a warning rather than silently swallowing the error (the
// old fmt.Sscanf path discarded the error, so "abc" became 0 with no trace).
// Signature is preserved (callers in the RTD path expect a bare int32).
func ParseInt(s string) int32 {
	v, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		log.Warn("RTD arg: invalid integer, defaulting to 0", "value", s, "error", err)
		return 0
	}
	return int32(v)
}

// ParseFloat parses an RTD topic argument as a float64. On a malformed value
// it returns 0 and logs a warning (see ParseInt). Signature is preserved.
func ParseFloat(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		log.Warn("RTD arg: invalid float, defaulting to 0", "value", s, "error", err)
		return 0
	}
	return v
}

func ParseBool(s string) bool {
	return s == "TRUE" || s == "1" || s == "true"
}

// MapAnyValue maps an arbitrary Go value onto a protocol.Any union tag plus
// the payload the Any builder expects for that tag (nil → Nil, string → Str,
// int32 → Int, other ints/floats → Num, bool → Bool, time.Time → RFC3339 Str,
// everything else → fmt-formatted Str). Used by generated code for
// `return: "any"` async results, where the tag is needed at queue time.
func MapAnyValue(v any) (AnyValue, any) {
	return fbany.MapGo(v)
}

// BuildAnyFromGo serializes an arbitrary Go value into a protocol.Any table
// using the MapAnyValue mapping, returning the Any offset. Used by generated
// code for `return: "any"` sync responses; the same mapping backs RTD
// updates, so a value renders identically in a cell from either path.
func BuildAnyFromGo(b *flatbuffers.Builder, v any) flatbuffers.UOffsetT {
	return fbany.BuildGo(b, v)
}

// BuildGridFromGo deep-copies a row-major Go [][]any into a protocol.Grid
// table, returning the Grid offset (NOT wrapped in Any — the per-function
// sync Response carries a Grid directly). The grid must be rectangular and
// non-empty; a malformed grid returns an error so the generated server can
// route it through the error path (the cell then shows the message instead of
// garbage). Cells accept nil/bool/string/int*/float* (see fbany.BuildGrid).
//
// On Excel 2021+/365 the resulting xltypeMulti (the C++ wrapper's
// GridToXLOPER12 output) spills natively; on pre-dynamic-array Excel the user
// sees the top-left cell (or enters the formula as a legacy CSE array).
func BuildGridFromGo(b *flatbuffers.Builder, v [][]any) (flatbuffers.UOffsetT, error) {
	return fbany.BuildGrid(b, v)
}

// BuildNumGridFromGo deep-copies a row-major Go [][]float64 into a
// protocol.NumGrid table (dense doubles), returning the NumGrid offset. Same
// rectangularity/non-empty contract and error semantics as BuildGridFromGo.
// numgrid is registered as FP12 (`K%`); the C++ wrapper's NumGridToFP12
// output also spills in dynamic-array Excel.
func BuildNumGridFromGo(b *flatbuffers.Builder, v [][]float64) (flatbuffers.UOffsetT, error) {
	return fbany.BuildNumGrid(b, v)
}

// BuildRtdOnceGridResult serializes a grid-returning rtd-once handler result
// into a complete, finished protocol.RtdOnceGridResult buffer ready to ship
// guest->host (see rtd.RunOnceGrid / rtd.RtdManager.SendOnceGrid). The buffer
// carries:
//
//   - key:   the host-side lookup key — the RTD topic strings joined with
//     U+001F (\x1f), identical to the C++ wrapper's MakeRtdOnceKey(topics).
//     The generated server passes strings.Join(args, "\x1f").
//   - value: an Any whose union member is Grid (when v is a [][]any grid) or
//     NumGrid (when v is a [][]float64 numgrid), matching the C++ wrapper's
//     val_as_Grid()/val_as_NumGrid() spill paths.
//
// The grid-vs-numgrid choice is made by a type switch on the concrete return
// value, so the generated server can call this helper uniformly without
// branching on the declared return type. A nil/empty/malformed grid (or any
// other type) returns an error so RunOnceGrid routes it through the error path
// (the cell shows the message instead of a corrupt/empty spill).
func BuildRtdOnceGridResult(key string, v any) ([]byte, error) {
	b := flatbuffers.NewBuilder(1024)

	var gridOff flatbuffers.UOffsetT
	var tag protocol.AnyValue
	var err error

	switch g := v.(type) {
	case [][]any:
		gridOff, err = fbany.BuildGrid(b, g)
		tag = protocol.AnyValueGrid
	case [][]float64:
		gridOff, err = fbany.BuildNumGrid(b, g)
		tag = protocol.AnyValueNumGrid
	default:
		return nil, fmt.Errorf("server.BuildRtdOnceGridResult: unsupported result type %T (want [][]any or [][]float64)", v)
	}
	if err != nil {
		return nil, fmt.Errorf("server.BuildRtdOnceGridResult: %w", err)
	}

	protocol.AnyStart(b)
	protocol.AnyAddValType(b, tag)
	protocol.AnyAddVal(b, gridOff)
	anyOff := protocol.AnyEnd(b)

	keyOff := b.CreateString(key)

	protocol.RtdOnceGridResultStart(b)
	protocol.RtdOnceGridResultAddKey(b, keyOff)
	protocol.RtdOnceGridResultAddValue(b, anyOff)
	root := protocol.RtdOnceGridResultEnd(b)
	b.Finish(root)

	return b.FinishedBytes(), nil
}

// ValidateGrid reports whether v is a well-formed (rectangular, non-empty)
// mixed-cell grid. The async path validates at queue time — before the batch
// builder exists — so a malformed grid becomes an async error result rather
// than reaching fbany.Build at flush time (where it could only fall back to
// NONE, silently blanking the cell).
func ValidateGrid(v [][]any) error {
	_, _, err := fbany.ValidateGridDims(v)
	return err
}

// ValidateNumGrid reports whether v is a well-formed (rectangular, non-empty)
// numeric grid. See ValidateGrid for why the async path validates eagerly.
func ValidateNumGrid(v [][]float64) error {
	_, _, err := fbany.ValidateGridDims(v)
	return err
}

// CreateScalarAny serializes a ScalarValue into a protocol.Any table via the
// canonical internal/fbany builder, returning the Any offset.
func CreateScalarAny(b *flatbuffers.Builder, val ScalarValue) flatbuffers.UOffsetT {
	switch val.Type {
	case protocol.AnyValueInt:
		return fbany.Build(b, val.Type, val.Int)
	case protocol.AnyValueNum:
		return fbany.Build(b, val.Type, val.Num)
	case protocol.AnyValueBool:
		return fbany.Build(b, val.Type, val.Bool)
	case protocol.AnyValueStr:
		return fbany.Build(b, val.Type, val.Str)
	case protocol.AnyValueErr:
		return fbany.Build(b, val.Type, val.Err)
	}
	// Tags outside the five scalar kinds (incl. the AnyValueNONE zero value)
	// historically produced an Any with an empty union member. ToScalar never
	// constructs such a ScalarValue; preserved for byte-identity.
	return fbany.BuildEmpty(b, val.Type)
}
