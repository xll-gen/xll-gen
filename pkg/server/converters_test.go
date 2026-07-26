package server

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/internal/fbany"
)

func TestSerialToTime(t *testing.T) {
	got := SerialToTime(46188) // 2026-06-15
	want := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("SerialToTime(46188) = %v, want %v", got, want)
	}
}

func TestToScalar_Date(t *testing.T) {
	b := flatbuffers.NewBuilder(0)
	protocol.DateStart(b)
	protocol.DateAddSerial(b, 46188.5)
	dOff := protocol.DateEnd(b)
	protocol.AnyStart(b)
	protocol.AnyAddValType(b, protocol.AnyValueDate)
	protocol.AnyAddVal(b, dOff)
	b.Finish(protocol.AnyEnd(b))
	a := protocol.GetRootAsAny(b.FinishedBytes(), 0)

	sv, ok := ToScalar(a)
	if !ok || sv.Type != protocol.AnyValueNum || sv.Num != 46188.5 {
		t.Fatalf("ToScalar(Date) = %+v ok=%v, want Num 46188.5", sv, ok)
	}
}

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

// TestBuildRtdOnceGridResult_GridRoundTrip serializes a [][]any grid into an
// RtdOnceGridResult buffer (the guest->host one-shot grid wire form) and reads
// the key + Grid union back, asserting the Any union member is Grid and the
// cells survive.
func TestBuildRtdOnceGridResult_GridRoundTrip(t *testing.T) {
	const wantKey = "BDH\x1f AAPL \x1f 30"
	in := [][]any{
		{int32(10), "hi"},
		{false, 2.5},
	}

	buf, err := BuildRtdOnceGridResult(wantKey, in)
	if err != nil {
		t.Fatalf("BuildRtdOnceGridResult(grid): %v", err)
	}

	got := protocol.GetRootAsRtdOnceGridResult(buf, 0)
	if string(got.Key()) != wantKey {
		t.Fatalf("key = %q, want %q", string(got.Key()), wantKey)
	}
	any := new(protocol.Any)
	if got.Value(any) == nil {
		t.Fatal("Value() returned nil Any")
	}
	if any.ValType() != protocol.AnyValueGrid {
		t.Fatalf("union tag = %d, want AnyValueGrid (%d)", any.ValType(), protocol.AnyValueGrid)
	}
	var gtbl flatbuffers.Table
	if !any.Val(&gtbl) {
		t.Fatal("failed to read Grid from Any union")
	}
	g := new(protocol.Grid)
	g.Init(gtbl.Bytes, gtbl.Pos)
	if g.Rows() != 2 || g.Cols() != 2 || g.DataLength() != 4 {
		t.Fatalf("grid = %dx%d len=%d, want 2x2 len=4", g.Rows(), g.Cols(), g.DataLength())
	}
	var c0 protocol.Scalar
	g.Data(&c0, 0)
	if c0.ValType() != protocol.ScalarValueInt {
		t.Errorf("cell0 tag = %v, want Int", c0.ValType())
	}
}

// TestBuildRtdOnceGridResult_NumGridRoundTrip does the same for a [][]float64
// numgrid: the Any union member must be NumGrid and the dense doubles survive.
func TestBuildRtdOnceGridResult_NumGridRoundTrip(t *testing.T) {
	const wantKey = "BDS\x1f IBM "
	in := [][]float64{{1, 2}, {3, 4}, {5, 6}}

	buf, err := BuildRtdOnceGridResult(wantKey, in)
	if err != nil {
		t.Fatalf("BuildRtdOnceGridResult(numgrid): %v", err)
	}

	got := protocol.GetRootAsRtdOnceGridResult(buf, 0)
	if string(got.Key()) != wantKey {
		t.Fatalf("key = %q, want %q", string(got.Key()), wantKey)
	}
	any := new(protocol.Any)
	if got.Value(any) == nil {
		t.Fatal("Value() returned nil Any")
	}
	if any.ValType() != protocol.AnyValueNumGrid {
		t.Fatalf("union tag = %d, want AnyValueNumGrid (%d)", any.ValType(), protocol.AnyValueNumGrid)
	}
	var ngtbl flatbuffers.Table
	if !any.Val(&ngtbl) {
		t.Fatal("failed to read NumGrid from Any union")
	}
	ng := new(protocol.NumGrid)
	ng.Init(ngtbl.Bytes, ngtbl.Pos)
	if ng.Rows() != 3 || ng.Cols() != 2 {
		t.Fatalf("numgrid = %dx%d, want 3x2", ng.Rows(), ng.Cols())
	}
	for i, w := range []float64{1, 2, 3, 4, 5, 6} {
		if ng.Data(i) != w {
			t.Errorf("data[%d] = %v, want %v", i, ng.Data(i), w)
		}
	}
}

// TestBuildRtdOnceGridResult_Errors: an unsupported type and a malformed grid
// both surface an error (so RunOnceGrid routes to the cell's error text rather
// than shipping a corrupt/empty spill).
func TestBuildRtdOnceGridResult_Errors(t *testing.T) {
	if _, err := BuildRtdOnceGridResult("k", "not a grid"); err == nil {
		t.Error("unsupported type: want error")
	}
	if _, err := BuildRtdOnceGridResult("k", [][]any{{1}, {2, 3}}); err == nil {
		t.Error("jagged grid: want error")
	}
	if _, err := BuildRtdOnceGridResult("k", [][]float64{}); err == nil {
		t.Error("empty numgrid: want error")
	}
}

// legacyBuildRtdOnceGridResult is a verbatim copy of BuildRtdOnceGridResult as
// it stood before the builder was pre-sized (a fixed 1 KiB initial buffer). It
// is the byte-identity oracle for TestBuildRtdOnceGridResult_PresizedBytesIdentical:
// the FlatBuffers builder's initial capacity must be a pure allocation hint —
// the encoded bytes may not depend on it.
func legacyBuildRtdOnceGridResult(key string, v any) ([]byte, error) {
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

// benchAnyGrid builds a rows x cols [][]any with a mix of cell kinds (the
// realistic rtd-once grid shape: numbers with occasional labels/flags).
func benchAnyGrid(rows, cols int) [][]any {
	g := make([][]any, rows)
	for i := range g {
		g[i] = make([]any, cols)
		for j := range g[i] {
			switch (i + j) % 4 {
			case 0:
				g[i][j] = float64(i*cols + j)
			case 1:
				g[i][j] = int32(i*cols + j)
			case 2:
				g[i][j] = "cell"
			default:
				g[i][j] = (i+j)%8 == 3
			}
		}
	}
	return g
}

func benchNumGrid(rows, cols int) [][]float64 {
	g := make([][]float64, rows)
	for i := range g {
		g[i] = make([]float64, cols)
		for j := range g[i] {
			g[i][j] = float64(i*cols + j)
		}
	}
	return g
}

// TestBuildRtdOnceGridResult_PresizedBytesIdentical pins the load-bearing
// property of the pre-sizing change: the builder's initial capacity is an
// allocation hint only, so the FINISHED bytes must be identical to what the old
// fixed-1-KiB builder produced. If this ever fails, the pre-size is changing the
// wire form and the C++ RtdOnceGridRegistry consumer is at risk.
func TestBuildRtdOnceGridResult_PresizedBytesIdentical(t *testing.T) {
	cases := []struct {
		name string
		key  string
		val  any
	}{
		{"grid_1x1", "K", [][]any{{1.0}}},
		{"grid_mixed_2x2", "BDH\x1f AAPL \x1f 30", [][]any{{int32(10), "hi"}, {false, 2.5}}},
		{"grid_7x13", "k7x13", benchAnyGrid(7, 13)},
		{"grid_100x100", "k100", benchAnyGrid(100, 100)},
		{"numgrid_1x1", "K", [][]float64{{1}}},
		{"numgrid_3x2", "BDS\x1f IBM ", [][]float64{{1, 2}, {3, 4}, {5, 6}}},
		{"numgrid_100x100", "k100", benchNumGrid(100, 100)},
		{"numgrid_257x63", "k257", benchNumGrid(257, 63)},
		{"empty_key", "", benchAnyGrid(3, 3)},
		{"long_key", strings.Repeat("topic\x1f", 200), benchNumGrid(4, 4)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := legacyBuildRtdOnceGridResult(tc.key, tc.val)
			if err != nil {
				t.Fatalf("legacy build: %v", err)
			}
			got, err := BuildRtdOnceGridResult(tc.key, tc.val)
			if err != nil {
				t.Fatalf("BuildRtdOnceGridResult: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("pre-sized output differs from the fixed-1KiB build: len %d vs %d", len(got), len(want))
			}
		})
	}
}

func BenchmarkBuildRtdOnceGridResult(b *testing.B) {
	sizes := []struct {
		name string
		n    int
	}{{"10x10", 10}, {"100x100", 100}, {"1000x1000", 1000}}
	for _, s := range sizes {
		g := benchAnyGrid(s.n, s.n)
		b.Run("any_"+s.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := BuildRtdOnceGridResult("key", g); err != nil {
					b.Fatal(err)
				}
			}
		})
		ng := benchNumGrid(s.n, s.n)
		b.Run("num_"+s.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := BuildRtdOnceGridResult("key", ng); err != nil {
					b.Fatal(err)
				}
			}
		})
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
