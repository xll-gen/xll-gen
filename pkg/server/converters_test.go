package server

import (
	"bytes"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/internal/fbany"
)

// legacyCreateScalarAny reproduces, verbatim, the pre-refactor
// CreateScalarAny so the fbany-based version can be checked for byte
// identity.
func legacyCreateScalarAny(b *flatbuffers.Builder, val ScalarValue) flatbuffers.UOffsetT {
	var uOff flatbuffers.UOffsetT
	switch val.Type {
	case protocol.AnyValueInt:
		protocol.IntStart(b)
		protocol.IntAddVal(b, val.Int)
		uOff = protocol.IntEnd(b)
	case protocol.AnyValueNum:
		protocol.NumStart(b)
		protocol.NumAddVal(b, val.Num)
		uOff = protocol.NumEnd(b)
	case protocol.AnyValueBool:
		protocol.BoolStart(b)
		protocol.BoolAddVal(b, val.Bool)
		uOff = protocol.BoolEnd(b)
	case protocol.AnyValueStr:
		sOff := b.CreateString(val.Str)
		protocol.StrStart(b)
		protocol.StrAddVal(b, sOff)
		uOff = protocol.StrEnd(b)
	case protocol.AnyValueErr:
		protocol.ErrStart(b)
		protocol.ErrAddVal(b, protocol.XlError(val.Err))
		uOff = protocol.ErrEnd(b)
	}

	protocol.AnyStart(b)
	protocol.AnyAddValType(b, val.Type)
	protocol.AnyAddVal(b, uOff)
	return protocol.AnyEnd(b)
}

// legacyAsyncAny reproduces, verbatim, the pre-refactor inline Any-building
// switch from FlushAsyncBatch (async_batcher.go).
func legacyAsyncAny(b *flatbuffers.Builder, valType protocol.AnyValue, val any) flatbuffers.UOffsetT {
	var uOff flatbuffers.UOffsetT
	switch valType {
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
	case protocol.AnyValueNil:
		protocol.NilStart(b)
		uOff = protocol.NilEnd(b)
	}

	protocol.AnyStart(b)
	protocol.AnyAddValType(b, valType)
	protocol.AnyAddVal(b, uOff)
	return protocol.AnyEnd(b)
}

func finishedAny(t *testing.T, build func(b *flatbuffers.Builder) flatbuffers.UOffsetT) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(1024)
	off := build(b)
	b.Finish(off)
	return append([]byte(nil), b.FinishedBytes()...)
}

// TestParseInt_ErrorPath verifies ParseInt returns 0 on malformed input and
// the correct value on valid input (IMPROVEMENT_BACKLOG.md §3 — strconv with
// explicit zero-on-error instead of a swallowed fmt.Sscanf error).
func TestParseInt_ErrorPath(t *testing.T) {
	cases := []struct {
		in   string
		want int32
	}{
		{"42", 42},
		{"-7", -7},
		{"0", 0},
		{"", 0},
		{"abc", 0},
		{"3.5", 0},                  // not an integer
		{"99999999999999999999", 0}, // overflows int32
	}
	for _, tc := range cases {
		if got := ParseInt(tc.in); got != tc.want {
			t.Errorf("ParseInt(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestParseFloat_ErrorPath verifies ParseFloat returns 0 on malformed input
// and the correct value on valid input.
func TestParseFloat_ErrorPath(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"3.14", 3.14},
		{"-2.5", -2.5},
		{"10", 10},
		{"", 0},
		{"xyz", 0},
		{"1.2.3", 0},
	}
	for _, tc := range cases {
		if got := ParseFloat(tc.in); got != tc.want {
			t.Errorf("ParseFloat(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestCreateScalarAny_ByteIdenticalToLegacy proves the fbany-based
// CreateScalarAny is byte-identical to the pre-refactor inline builder for
// every scalar tag (and for the zero-value/unknown tag fallthrough).
func TestCreateScalarAny_ByteIdenticalToLegacy(t *testing.T) {
	cases := []struct {
		name string
		val  ScalarValue
	}{
		{"int", ScalarValue{Type: protocol.AnyValueInt, Int: -123}},
		{"num", ScalarValue{Type: protocol.AnyValueNum, Num: 6.28}},
		{"bool_true", ScalarValue{Type: protocol.AnyValueBool, Bool: true}},
		{"bool_false", ScalarValue{Type: protocol.AnyValueBool, Bool: false}},
		{"str", ScalarValue{Type: protocol.AnyValueStr, Str: "abc"}},
		{"str_empty", ScalarValue{Type: protocol.AnyValueStr, Str: ""}},
		{"err", ScalarValue{Type: protocol.AnyValueErr, Err: 15}},
		{"zero_value_none", ScalarValue{}},
		{"unhandled_nil_tag", ScalarValue{Type: protocol.AnyValueNil}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := finishedAny(t, func(b *flatbuffers.Builder) flatbuffers.UOffsetT {
				return CreateScalarAny(b, tc.val)
			})
			want := finishedAny(t, func(b *flatbuffers.Builder) flatbuffers.UOffsetT {
				return legacyCreateScalarAny(b, tc.val)
			})
			if !bytes.Equal(got, want) {
				t.Fatalf("serialized bytes differ from legacy\n got: %x\nwant: %x", got, want)
			}
		})
	}
}

// TestAsyncAnyBuild_ByteIdenticalToLegacy proves fbany.Build (now used by
// FlushAsyncBatch) is byte-identical to the pre-refactor inline switch for
// every ValType the generated async code actually queues
// (Int/Num/Bool/Str/Nil).
func TestAsyncAnyBuild_ByteIdenticalToLegacy(t *testing.T) {
	cases := []struct {
		name    string
		valType protocol.AnyValue
		val     any
	}{
		{"int", protocol.AnyValueInt, int32(7)},
		{"num", protocol.AnyValueNum, float64(1.5)},
		{"bool", protocol.AnyValueBool, true},
		{"str", protocol.AnyValueStr, "result"},
		{"nil", protocol.AnyValueNil, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := finishedAny(t, func(b *flatbuffers.Builder) flatbuffers.UOffsetT {
				return fbany.Build(b, tc.valType, tc.val)
			})
			want := finishedAny(t, func(b *flatbuffers.Builder) flatbuffers.UOffsetT {
				return legacyAsyncAny(b, tc.valType, tc.val)
			})
			if !bytes.Equal(got, want) {
				t.Fatalf("serialized bytes differ from legacy\n got: %x\nwant: %x", got, want)
			}
		})
	}
}

// TestBuildGridFromGo_RoundTrip verifies the sync-path wrapper round-trips a
// [][]any through protocol.Grid (the per-function Response carries a Grid
// directly, NOT wrapped in Any), preserving dims, row-major order, and cell
// types.
func TestBuildGridFromGo_RoundTrip(t *testing.T) {
	in := [][]any{
		{int32(10), "hi"},
		{false, 2.5},
	}
	b := flatbuffers.NewBuilder(1024)
	off, err := BuildGridFromGo(b, in)
	if err != nil {
		t.Fatalf("BuildGridFromGo: %v", err)
	}
	b.Finish(off)
	g := protocol.GetRootAsGrid(b.FinishedBytes(), 0)
	if g.Rows() != 2 || g.Cols() != 2 || g.DataLength() != 4 {
		t.Fatalf("grid = %dx%d len=%d, want 2x2 len=4", g.Rows(), g.Cols(), g.DataLength())
	}

	var c0 protocol.Scalar
	g.Data(&c0, 0)
	if c0.ValType() != protocol.ScalarValueInt {
		t.Errorf("cell0 tag = %v, want Int", c0.ValType())
	}
	var c3 protocol.Scalar
	g.Data(&c3, 3)
	if c3.ValType() != protocol.ScalarValueNum {
		t.Errorf("cell3 tag = %v, want Num", c3.ValType())
	}
}

// TestBuildNumGridFromGo_RoundTrip verifies the numeric sync-path wrapper.
func TestBuildNumGridFromGo_RoundTrip(t *testing.T) {
	in := [][]float64{{1, 2}, {3, 4}, {5, 6}}
	b := flatbuffers.NewBuilder(1024)
	off, err := BuildNumGridFromGo(b, in)
	if err != nil {
		t.Fatalf("BuildNumGridFromGo: %v", err)
	}
	b.Finish(off)
	g := protocol.GetRootAsNumGrid(b.FinishedBytes(), 0)
	if g.Rows() != 3 || g.Cols() != 2 {
		t.Fatalf("numgrid = %dx%d, want 3x2", g.Rows(), g.Cols())
	}
	for i, w := range []float64{1, 2, 3, 4, 5, 6} {
		if g.Data(i) != w {
			t.Errorf("data[%d] = %v, want %v", i, g.Data(i), w)
		}
	}
}

// TestGridValidators verifies ValidateGrid / ValidateNumGrid (the async
// queue-time guard) accept rectangular non-empty grids and reject empty/jagged.
func TestGridValidators(t *testing.T) {
	if err := ValidateGrid([][]any{{1, 2}, {3, 4}}); err != nil {
		t.Errorf("ValidateGrid rectangular: unexpected error %v", err)
	}
	if err := ValidateGrid([][]any{{1}, {2, 3}}); err == nil {
		t.Error("ValidateGrid jagged: want error")
	}
	if err := ValidateGrid(nil); err == nil {
		t.Error("ValidateGrid nil: want error")
	}
	if err := ValidateNumGrid([][]float64{{1}, {2}}); err != nil {
		t.Errorf("ValidateNumGrid rectangular: unexpected error %v", err)
	}
	if err := ValidateNumGrid([][]float64{{1, 2}, {3}}); err == nil {
		t.Error("ValidateNumGrid jagged: want error")
	}
}

// TestBuildGridFromGo_ErrorOnMalformed confirms the sync wrapper surfaces a
// build error (so the generated server routes it to the cell's error text)
// instead of silently emitting a zero-size grid.
func TestBuildGridFromGo_ErrorOnMalformed(t *testing.T) {
	if _, err := BuildGridFromGo(flatbuffers.NewBuilder(64), [][]any{{1}, {2, 3}}); err == nil {
		t.Error("BuildGridFromGo jagged: want error")
	}
	if _, err := BuildNumGridFromGo(flatbuffers.NewBuilder(64), [][]float64{}); err == nil {
		t.Error("BuildNumGridFromGo empty: want error")
	}
}

// TestAsyncAnyBuild_UnknownTagYieldsNoneNotCorruptUnion verifies the
// default branch in fbany.Build (IMPROVEMENT_BACKLOG.md §2/§3): an unhandled
// tag must NOT serialize a union advertising a kind with no backing table
// (the pre-fix behavior, which the C++ reader would dereference). Instead it
// must produce a well-formed Any with val_type == NONE and an empty member.
//
// Grid/NumGrid are now HANDLED tags (val [][]any / [][]float64), but passing a
// nil/wrong-typed val for them must still yield NONE — not a corrupt union and
// not a panic — via the comma-ok type-assertion guard in fbany.Build. (The
// generated server never queues a Grid/NumGrid tag with a nil val; it
// validates first and queues an error result instead. This is the
// belt-and-suspenders path.)
func TestAsyncAnyBuild_UnknownTagYieldsNoneNotCorruptUnion(t *testing.T) {
	unhandled := []protocol.AnyValue{
		protocol.AnyValueNONE,
		protocol.AnyValueGrid,    // nil val (not [][]any) → NONE via comma-ok guard
		protocol.AnyValueNumGrid, // nil val (not [][]float64) → NONE
		protocol.AnyValue(250),   // wholly out-of-range tag
	}
	for _, tag := range unhandled {
		raw := finishedAny(t, func(b *flatbuffers.Builder) flatbuffers.UOffsetT {
			return fbany.Build(b, tag, nil)
		})
		got := protocol.GetRootAsAny(raw, 0)
		if got.ValType() != protocol.AnyValueNONE {
			t.Fatalf("tag %v: ValType=%v, want AnyValueNONE", tag, got.ValType())
		}
		var tbl flatbuffers.Table
		if got.Val(&tbl) {
			t.Fatalf("tag %v: union member present, want empty (corrupt-union guard failed)", tag)
		}
	}
}
