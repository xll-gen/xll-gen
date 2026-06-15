// Package fbany is the single canonical builder for protocol.Any FlatBuffers
// unions. It exists as a leaf package (depending only on flatbuffers and the
// external types/go/protocol module) because the scalar→Any building logic
// was previously triplicated across pkg/server/converters.go,
// pkg/server/async_batcher.go and pkg/rtd/manager.go, and pkg/rtd cannot
// import pkg/server (pkg/server/handlers.go imports pkg/rtd — an import
// cycle). Long-term this builder is a candidate for migration to the
// xll-gen/types repo (see IMPROVEMENT_BACKLOG.md R1); keeping it internal
// makes that migration non-breaking.
package fbany

import (
	"fmt"
	"math"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/pkg/xldate"
)

// Build constructs the union member table selected by tag from val and wraps
// it in a protocol.Any table, returning the Any offset.
//
// Expected dynamic types per tag (a mismatch panics, matching the historical
// inline type assertions in the async batcher):
//
//	AnyValueInt     → int32
//	AnyValueNum     → float64
//	AnyValueBool    → bool
//	AnyValueStr     → string
//	AnyValueErr     → int16
//	AnyValueNil     → val ignored
//	AnyValueGrid    → [][]any      (row-major; cells nil/bool/string/int*/float*)
//	AnyValueNumGrid → [][]float64  (row-major, rectangular)
//
// Any other tag (including AnyValueNONE) produces a well-formed Any whose
// val_type is forced to AnyValueNONE and whose union member is empty (offset
// 0). The legacy inline switches left uOff at zero for unhandled tags but kept
// the original (non-scalar) tag; that produced an Any advertising a union kind
// with no backing table — a corrupt union the C++ reader would dereference.
// Forcing NONE here is the deliberate "no value" outcome (see
// IMPROVEMENT_BACKLOG.md §2). For the six handled tags the output is unchanged.
func Build(b *flatbuffers.Builder, tag protocol.AnyValue, val any) flatbuffers.UOffsetT {
	var uOff flatbuffers.UOffsetT
	switch tag {
	case protocol.AnyValueInt:
		protocol.IntStart(b)
		protocol.IntAddVal(b, val.(int32))
		uOff = protocol.IntEnd(b)
	case protocol.AnyValueNum:
		protocol.NumStart(b)
		protocol.NumAddVal(b, val.(float64))
		uOff = protocol.NumEnd(b)
	case protocol.AnyValueBool:
		protocol.BoolStart(b)
		protocol.BoolAddVal(b, val.(bool))
		uOff = protocol.BoolEnd(b)
	case protocol.AnyValueStr:
		sOff := b.CreateString(val.(string))
		protocol.StrStart(b)
		protocol.StrAddVal(b, sOff)
		uOff = protocol.StrEnd(b)
	case protocol.AnyValueErr:
		protocol.ErrStart(b)
		protocol.ErrAddVal(b, protocol.XlError(val.(int16)))
		uOff = protocol.ErrEnd(b)
	case protocol.AnyValueNil:
		protocol.NilStart(b)
		uOff = protocol.NilEnd(b)
	case protocol.AnyValueGrid:
		// val is a [][]any. The grid is validated at queue time (sync:
		// server.BuildGridFromGo; async: server.ValidateGrid before
		// QueueResult), so a build error / wrong-typed val here means a
		// programming error in the generated code; fall back to NONE rather
		// than corrupt the union (or panic on a bad type assertion).
		grid, ok := val.([][]any)
		if !ok {
			tag = protocol.AnyValueNONE
			break
		}
		off, err := BuildGrid(b, grid)
		if err != nil {
			tag = protocol.AnyValueNONE
			break
		}
		uOff = off
	case protocol.AnyValueNumGrid:
		ng, ok := val.([][]float64)
		if !ok {
			tag = protocol.AnyValueNONE
			break
		}
		off, err := BuildNumGrid(b, ng)
		if err != nil {
			tag = protocol.AnyValueNONE
			break
		}
		uOff = off
	case protocol.AnyValueDate:
		t, ok := val.(time.Time)
		if !ok {
			tag = protocol.AnyValueNONE
			break
		}
		protocol.DateStart(b)
		protocol.DateAddSerial(b, xldate.ToSerial(t))
		uOff = protocol.DateEnd(b)
	default:
		// Unknown/unhandled tag (including AnyValueNONE). We deliberately do
		// NOT attempt to build a union member for an unrecognized tag — doing
		// so would require an arbitrary type assertion on val and could panic
		// or, worse, serialize a union whose val_type does not match the
		// table it points at (a corrupt union the C++ side would misread).
		// Instead emit val_type=NONE with an empty (offset 0) member. This is
		// a well-formed Any the reader treats as "no value", not corruption.
		// See IMPROVEMENT_BACKLOG.md §2 / §3 (default branch on the ValType
		// switch). uOff stays 0; tag is forced to NONE below.
		tag = protocol.AnyValueNONE
	}

	protocol.AnyStart(b)
	protocol.AnyAddValType(b, tag)
	protocol.AnyAddVal(b, uOff)
	return protocol.AnyEnd(b)
}

// MapGo maps an arbitrary Go value onto a protocol.Any union tag plus the
// payload Build expects for that tag. This is the canonical Go-value→Any
// mapping shared by the RTD update path and the generated sync/async
// `return: "any"` paths, so a value renders identically in a cell no matter
// which route delivered it:
//
//	nil          → AnyValueNil  (empty cell)
//	string       → AnyValueStr
//	int32        → AnyValueInt
//	int, int64   → AnyValueNum  (Go ints can exceed 32 bits; double keeps 53)
//	float64/32   → AnyValueNum
//	bool         → AnyValueBool
//	time.Time    → AnyValueDate (Excel serial, wall-clock)
//	anything else → AnyValueStr via fmt.Sprintf("%v", v)
func MapGo(value any) (protocol.AnyValue, any) {
	switch v := value.(type) {
	case nil:
		return protocol.AnyValueNil, nil
	case string:
		return protocol.AnyValueStr, v
	case int:
		// Go int can be 64-bit, so send as double to prevent truncation
		return protocol.AnyValueNum, float64(v)
	case int32:
		return protocol.AnyValueInt, v
	case int64:
		// Protocol only supports 32-bit int, so we send as double to preserve value (up to 53 bits)
		return protocol.AnyValueNum, float64(v)
	case float64:
		return protocol.AnyValueNum, v
	case float32:
		return protocol.AnyValueNum, float64(v)
	case bool:
		return protocol.AnyValueBool, v
	case time.Time:
		return protocol.AnyValueDate, v
	default:
		return protocol.AnyValueStr, fmt.Sprintf("%v", v)
	}
}

// BuildGo maps value through MapGo and serializes it with Build, returning
// the protocol.Any offset. Convenience for callers that don't need the tag.
func BuildGo(b *flatbuffers.Builder, value any) flatbuffers.UOffsetT {
	tag, payload := MapGo(value)
	return Build(b, tag, payload)
}

// BuildEmpty wraps an empty union member (offset 0) in a protocol.Any table
// carrying the given tag. This preserves the legacy behavior of
// server.CreateScalarAny for tags outside the five scalar kinds — notably
// AnyValueNil, for which Build would emit a real Nil table instead.
func BuildEmpty(b *flatbuffers.Builder, tag protocol.AnyValue) flatbuffers.UOffsetT {
	protocol.AnyStart(b)
	protocol.AnyAddValType(b, tag)
	protocol.AnyAddVal(b, 0)
	return protocol.AnyEnd(b)
}

// ValidateGridDims validates that v is a non-empty rectangular grid (all rows
// the same length, rows*cols > 0) and returns its dimensions. It is the single
// rectangularity check shared by the grid builders (mixed-cell and numeric).
// An empty grid (zero rows, or a row of zero cols) is rejected: a spilling
// function must return at least one cell; callers wanting "no value" should
// return an error (which renders as the error text in the cell) instead.
func ValidateGridDims[T any](v [][]T) (rows, cols int, err error) {
	rows = len(v)
	if rows == 0 {
		return 0, 0, fmt.Errorf("grid must have at least one row")
	}
	cols = len(v[0])
	if cols == 0 {
		return 0, 0, fmt.Errorf("grid row 0 has no columns")
	}
	for i, row := range v {
		if len(row) != cols {
			return 0, 0, fmt.Errorf("grid is not rectangular: row %d has %d columns, want %d", i, len(row), cols)
		}
	}
	return rows, cols, nil
}

// buildScalarCell serializes one mixed-typed cell into a protocol.Scalar table,
// mirroring the C++ ConvertScalar arg-direction conventions (types/src/
// converters.cpp). Cell dynamic types accepted:
//
//	nil                                  → Nil
//	bool                                 → Bool
//	string                               → Str
//	int / int8 / int16 / int32 / int64   → Int (int32) when the value fits;
//	uint*                                  otherwise Num (float64, 53-bit) —
//	                                       mirrors MapGo's no-silent-truncation
//	                                       rule for the scalar/any return path
//	float32 / float64                    → Num
//
// Anything else is stringified via fmt.Sprintf("%v", c) into a Str cell so a
// stray type renders visibly rather than dropping the whole grid. Excel cells
// are doubles either way (xltypeInt and xltypeNum both render as numbers), so
// the Int→Num widening is invisible to the sheet.
func buildScalarCell(b *flatbuffers.Builder, c any) flatbuffers.UOffsetT {
	var valType protocol.ScalarValue
	var uOff flatbuffers.UOffsetT

	switch v := c.(type) {
	case nil:
		protocol.NilStart(b)
		uOff = protocol.NilEnd(b)
		valType = protocol.ScalarValueNil
	case bool:
		protocol.BoolStart(b)
		protocol.BoolAddVal(b, v)
		uOff = protocol.BoolEnd(b)
		valType = protocol.ScalarValueBool
	case string:
		sOff := b.CreateString(v)
		protocol.StrStart(b)
		protocol.StrAddVal(b, sOff)
		uOff = protocol.StrEnd(b)
		valType = protocol.ScalarValueStr
	case int:
		uOff, valType = buildIntOrNumCell(b, int64(v))
	case int8:
		uOff = buildIntCell(b, int32(v))
		valType = protocol.ScalarValueInt
	case int16:
		uOff = buildIntCell(b, int32(v))
		valType = protocol.ScalarValueInt
	case int32:
		uOff = buildIntCell(b, v)
		valType = protocol.ScalarValueInt
	case int64:
		uOff, valType = buildIntOrNumCell(b, v)
	case uint:
		uOff, valType = buildUintOrNumCell(b, uint64(v))
	case uint8:
		uOff = buildIntCell(b, int32(v))
		valType = protocol.ScalarValueInt
	case uint16:
		uOff = buildIntCell(b, int32(v))
		valType = protocol.ScalarValueInt
	case uint32:
		uOff, valType = buildUintOrNumCell(b, uint64(v))
	case uint64:
		uOff, valType = buildUintOrNumCell(b, v)
	case float32:
		uOff = buildNumCell(b, float64(v))
		valType = protocol.ScalarValueNum
	case float64:
		uOff = buildNumCell(b, v)
		valType = protocol.ScalarValueNum
	case time.Time:
		protocol.DateStart(b)
		protocol.DateAddSerial(b, xldate.ToSerial(v))
		uOff = protocol.DateEnd(b)
		valType = protocol.ScalarValueDate
	default:
		sOff := b.CreateString(fmt.Sprintf("%v", v))
		protocol.StrStart(b)
		protocol.StrAddVal(b, sOff)
		uOff = protocol.StrEnd(b)
		valType = protocol.ScalarValueStr
	}

	protocol.ScalarStart(b)
	protocol.ScalarAddValType(b, valType)
	protocol.ScalarAddVal(b, uOff)
	return protocol.ScalarEnd(b)
}

func buildIntCell(b *flatbuffers.Builder, v int32) flatbuffers.UOffsetT {
	protocol.IntStart(b)
	protocol.IntAddVal(b, v)
	return protocol.IntEnd(b)
}

func buildNumCell(b *flatbuffers.Builder, v float64) flatbuffers.UOffsetT {
	protocol.NumStart(b)
	protocol.NumAddVal(b, v)
	return protocol.NumEnd(b)
}

// buildIntOrNumCell emits an Int cell when v fits int32 and widens to a Num
// cell (float64) otherwise, so wide Go ints are never silently truncated —
// the same no-truncation rule MapGo applies on the scalar/any return path.
func buildIntOrNumCell(b *flatbuffers.Builder, v int64) (flatbuffers.UOffsetT, protocol.ScalarValue) {
	if v >= math.MinInt32 && v <= math.MaxInt32 {
		return buildIntCell(b, int32(v)), protocol.ScalarValueInt
	}
	return buildNumCell(b, float64(v)), protocol.ScalarValueNum
}

// buildUintOrNumCell is buildIntOrNumCell for unsigned sources.
func buildUintOrNumCell(b *flatbuffers.Builder, v uint64) (flatbuffers.UOffsetT, protocol.ScalarValue) {
	if v <= math.MaxInt32 {
		return buildIntCell(b, int32(v)), protocol.ScalarValueInt
	}
	return buildNumCell(b, float64(v)), protocol.ScalarValueNum
}

// BuildGrid deep-copies a row-major Go [][]any into a protocol.Grid table and
// returns the Grid offset. The grid must be rectangular and non-empty (see
// ValidateGridDims). Each cell is serialized via buildScalarCell. The data
// vector is row-major (element index = row*cols + col), matching the C++
// GridToXLOPER12 layout (types/src/converters.cpp).
//
// FlatBuffers requires nested objects (the Scalar cells, including their
// CreateString calls) to be built BEFORE the parent vector is started, so the
// cell offsets are materialized first, then prepended in reverse.
func BuildGrid(b *flatbuffers.Builder, v [][]any) (flatbuffers.UOffsetT, error) {
	rows, cols, err := ValidateGridDims(v)
	if err != nil {
		return 0, err
	}

	cellOffsets := make([]flatbuffers.UOffsetT, 0, rows*cols)
	for _, row := range v {
		for _, c := range row {
			cellOffsets = append(cellOffsets, buildScalarCell(b, c))
		}
	}

	protocol.GridStartDataVector(b, len(cellOffsets))
	for i := len(cellOffsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(cellOffsets[i])
	}
	dataVec := b.EndVector(len(cellOffsets))

	protocol.GridStart(b)
	protocol.GridAddRows(b, int32(rows))
	protocol.GridAddCols(b, int32(cols))
	protocol.GridAddData(b, dataVec)
	return protocol.GridEnd(b), nil
}

// BuildNumGrid deep-copies a row-major Go [][]float64 into a protocol.NumGrid
// table (dense doubles, no per-cell union) and returns the NumGrid offset. The
// grid must be rectangular and non-empty (see ValidateGridDims). The data
// vector is row-major, matching ConvertNumGrid / NumGridToFP12 (FP12 array
// layout, types/src/converters.cpp).
func BuildNumGrid(b *flatbuffers.Builder, v [][]float64) (flatbuffers.UOffsetT, error) {
	rows, cols, err := ValidateGridDims(v)
	if err != nil {
		return 0, err
	}

	protocol.NumGridStartDataVector(b, rows*cols)
	// Prepend in reverse row-major order so the final vector reads
	// [r0c0, r0c1, ..., r(rows-1)c(cols-1)].
	for i := rows - 1; i >= 0; i-- {
		row := v[i]
		for j := cols - 1; j >= 0; j-- {
			b.PrependFloat64(row[j])
		}
	}
	dataVec := b.EndVector(rows * cols)

	protocol.NumGridStart(b)
	protocol.NumGridAddRows(b, int32(rows))
	protocol.NumGridAddCols(b, int32(cols))
	protocol.NumGridAddData(b, dataVec)
	return protocol.NumGridEnd(b), nil
}
