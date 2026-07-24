package fbany

import (
	"strings"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
)

// readGrid finishes a Grid offset into bytes and returns the root Grid for
// readback, mirroring the C++ GridToXLOPER12 consumer.
func readGrid(t *testing.T, build func(b *flatbuffers.Builder) (flatbuffers.UOffsetT, error)) *protocol.Grid {
	t.Helper()
	b := flatbuffers.NewBuilder(1024)
	off, err := build(b)
	if err != nil {
		t.Fatalf("build returned error: %v", err)
	}
	b.Finish(off)
	return protocol.GetRootAsGrid(b.FinishedBytes(), 0)
}

func readNumGrid(t *testing.T, build func(b *flatbuffers.Builder) (flatbuffers.UOffsetT, error)) *protocol.NumGrid {
	t.Helper()
	b := flatbuffers.NewBuilder(1024)
	off, err := build(b)
	if err != nil {
		t.Fatalf("build returned error: %v", err)
	}
	b.Finish(off)
	return protocol.GetRootAsNumGrid(b.FinishedBytes(), 0)
}

// TestBuildGrid_RoundTrip builds a mixed-cell [][]any and reads it back through
// the protocol.Grid view, asserting dimensions, row-major ordering, and that
// every cell's union tag + value survive the round trip.
func TestBuildGrid_RoundTrip(t *testing.T) {
	in := [][]any{
		{int32(1), "two", 3.5},
		{true, nil, int(42)},
	}

	g := readGrid(t, func(b *flatbuffers.Builder) (flatbuffers.UOffsetT, error) {
		return BuildGrid(b, in)
	})

	if g.Rows() != 2 || g.Cols() != 3 {
		t.Fatalf("dims = %dx%d, want 2x3", g.Rows(), g.Cols())
	}
	if g.DataLength() != 6 {
		t.Fatalf("data length = %d, want 6", g.DataLength())
	}

	// Expected (tag, extractor-checked value) per row-major cell index.
	type check struct {
		tag protocol.ScalarValue
		chk func(t *testing.T, s *protocol.Scalar)
	}
	checks := []check{
		{protocol.ScalarValueInt, func(t *testing.T, s *protocol.Scalar) {
			var tbl flatbuffers.Table
			s.Val(&tbl)
			var iv protocol.Int
			iv.Init(tbl.Bytes, tbl.Pos)
			if iv.Val() != 1 {
				t.Errorf("cell0 Int = %d, want 1", iv.Val())
			}
		}},
		{protocol.ScalarValueStr, func(t *testing.T, s *protocol.Scalar) {
			var tbl flatbuffers.Table
			s.Val(&tbl)
			var sv protocol.Str
			sv.Init(tbl.Bytes, tbl.Pos)
			if string(sv.Val()) != "two" {
				t.Errorf("cell1 Str = %q, want two", sv.Val())
			}
		}},
		{protocol.ScalarValueNum, func(t *testing.T, s *protocol.Scalar) {
			var tbl flatbuffers.Table
			s.Val(&tbl)
			var nv protocol.Num
			nv.Init(tbl.Bytes, tbl.Pos)
			if nv.Val() != 3.5 {
				t.Errorf("cell2 Num = %v, want 3.5", nv.Val())
			}
		}},
		{protocol.ScalarValueBool, func(t *testing.T, s *protocol.Scalar) {
			var tbl flatbuffers.Table
			s.Val(&tbl)
			var bv protocol.Bool
			bv.Init(tbl.Bytes, tbl.Pos)
			if !bv.Val() {
				t.Errorf("cell3 Bool = false, want true")
			}
		}},
		{protocol.ScalarValueNil, func(t *testing.T, s *protocol.Scalar) {}},
		{protocol.ScalarValueInt, func(t *testing.T, s *protocol.Scalar) {
			var tbl flatbuffers.Table
			s.Val(&tbl)
			var iv protocol.Int
			iv.Init(tbl.Bytes, tbl.Pos)
			if iv.Val() != 42 {
				t.Errorf("cell5 Int = %d, want 42", iv.Val())
			}
		}},
	}

	for i, c := range checks {
		var s protocol.Scalar
		if !g.Data(&s, i) {
			t.Fatalf("cell %d missing", i)
		}
		if s.ValType() != c.tag {
			t.Errorf("cell %d tag = %v, want %v", i, s.ValType(), c.tag)
		}
		c.chk(t, &s)
	}
}

// TestBuildNumGrid_RoundTrip builds a dense [][]float64 and reads it back,
// asserting dims and row-major value placement.
func TestBuildNumGrid_RoundTrip(t *testing.T) {
	in := [][]float64{
		{1.0, 2.0, 3.0},
		{4.0, 5.0, 6.0},
	}

	g := readNumGrid(t, func(b *flatbuffers.Builder) (flatbuffers.UOffsetT, error) {
		return BuildNumGrid(b, in)
	})

	if g.Rows() != 2 || g.Cols() != 3 {
		t.Fatalf("dims = %dx%d, want 2x3", g.Rows(), g.Cols())
	}
	if g.DataLength() != 6 {
		t.Fatalf("data length = %d, want 6", g.DataLength())
	}
	want := []float64{1, 2, 3, 4, 5, 6}
	for i, w := range want {
		if got := g.Data(i); got != w {
			t.Errorf("data[%d] = %v, want %v (row-major order broken)", i, got, w)
		}
	}
}

// TestBuildGrid_Errors locks the rectangularity + non-empty contract for both
// builders (and the shared ValidateGridDims).
func TestBuildGrid_Errors(t *testing.T) {
	t.Run("grid empty rows", func(t *testing.T) {
		if _, err := BuildGrid(flatbuffers.NewBuilder(64), [][]any{}); err == nil {
			t.Fatal("empty grid must error")
		}
	})
	t.Run("grid empty cols", func(t *testing.T) {
		if _, err := BuildGrid(flatbuffers.NewBuilder(64), [][]any{{}}); err == nil {
			t.Fatal("zero-column grid must error")
		}
	})
	t.Run("grid jagged", func(t *testing.T) {
		_, err := BuildGrid(flatbuffers.NewBuilder(64), [][]any{{1, 2}, {3}})
		if err == nil {
			t.Fatal("jagged grid must error")
		}
	})
	t.Run("numgrid empty rows", func(t *testing.T) {
		if _, err := BuildNumGrid(flatbuffers.NewBuilder(64), [][]float64{}); err == nil {
			t.Fatal("empty numgrid must error")
		}
	})
	t.Run("numgrid jagged", func(t *testing.T) {
		_, err := BuildNumGrid(flatbuffers.NewBuilder(64), [][]float64{{1, 2, 3}, {4, 5}})
		if err == nil {
			t.Fatal("jagged numgrid must error")
		}
	})
	// rows*cols overflowing int32 must be rejected (symmetry with types'
	// validateDims). Only the declared geometry needs to be oversized: rows =
	// len(v) rows (mostly nil, cheap) and cols = len(v[0]); the product is
	// checked before the rectangularity scan, so we never allocate the full
	// ~2.1e9-cell grid. 46341^2 = 2147488281 > math.MaxInt32 (2147483647).
	t.Run("grid rows*cols overflow", func(t *testing.T) {
		const n = 46341
		v := make([][]any, n)
		v[0] = make([]any, n)
		_, _, err := ValidateGridDims(v)
		if err == nil {
			t.Fatal("rows*cols > MaxInt32 must error")
		}
		// Assert the OVERFLOW branch fired (not the rectangularity scan that
		// follows it). Without the overflow guard the rows[1:] nil rows would
		// trip "not rectangular" instead — this pins the guard, not jaggedness.
		if !strings.Contains(err.Error(), "exceed maximum supported cell count") {
			t.Fatalf("expected overflow error, got: %v", err)
		}
	})
}

// TestBuild_GridTag_RoundTrip exercises the async path: fbany.Build with the
// Grid/NumGrid tag wraps the grid in a protocol.Any (the union member the C++
// AnyToXLOPER12 consumes for async results).
func TestBuild_GridTag_RoundTrip(t *testing.T) {
	t.Run("grid", func(t *testing.T) {
		b := flatbuffers.NewBuilder(1024)
		off := Build(b, protocol.AnyValueGrid, [][]any{{int32(7), "x"}})
		b.Finish(off)
		any := protocol.GetRootAsAny(b.FinishedBytes(), 0)
		if any.ValType() != protocol.AnyValueGrid {
			t.Fatalf("ValType = %v, want Grid", any.ValType())
		}
		var tbl flatbuffers.Table
		if !any.Val(&tbl) {
			t.Fatal("Any has no union member")
		}
		var g protocol.Grid
		g.Init(tbl.Bytes, tbl.Pos)
		if g.Rows() != 1 || g.Cols() != 2 {
			t.Fatalf("grid dims = %dx%d, want 1x2", g.Rows(), g.Cols())
		}
	})

	t.Run("numgrid", func(t *testing.T) {
		b := flatbuffers.NewBuilder(1024)
		off := Build(b, protocol.AnyValueNumGrid, [][]float64{{1.5, 2.5}})
		b.Finish(off)
		any := protocol.GetRootAsAny(b.FinishedBytes(), 0)
		if any.ValType() != protocol.AnyValueNumGrid {
			t.Fatalf("ValType = %v, want NumGrid", any.ValType())
		}
		var tbl flatbuffers.Table
		if !any.Val(&tbl) {
			t.Fatal("Any has no union member")
		}
		var g protocol.NumGrid
		g.Init(tbl.Bytes, tbl.Pos)
		if g.Rows() != 1 || g.Cols() != 2 || g.Data(1) != 2.5 {
			t.Fatalf("numgrid wrong: %dx%d data[1]=%v", g.Rows(), g.Cols(), g.Data(1))
		}
	})

	// Wrong-typed val under a Grid tag must yield NONE, not a panic/corrupt union.
	t.Run("grid wrong type yields none", func(t *testing.T) {
		b := flatbuffers.NewBuilder(64)
		off := Build(b, protocol.AnyValueGrid, "not a grid")
		b.Finish(off)
		any := protocol.GetRootAsAny(b.FinishedBytes(), 0)
		if any.ValType() != protocol.AnyValueNONE {
			t.Fatalf("ValType = %v, want NONE", any.ValType())
		}
	})
}

// TestBuildGrid_WideIntCellsWidenToNum: int/int64/uint* cells that do not fit
// int32 must widen to a Num cell (float64) instead of silently truncating —
// the same no-truncation rule MapGo applies on the scalar/any return path.
// In-range values keep the Int member.
func TestBuildGrid_WideIntCellsWidenToNum(t *testing.T) {
	const wide = int64(3_000_000_000) // > math.MaxInt32
	in := [][]any{
		{int64(7), wide, uint64(wide), int(-5)},
	}

	g := readGrid(t, func(b *flatbuffers.Builder) (flatbuffers.UOffsetT, error) {
		return BuildGrid(b, in)
	})

	wantTags := []protocol.ScalarValue{
		protocol.ScalarValueInt, // int64(7) fits
		protocol.ScalarValueNum, // wide int64 widens
		protocol.ScalarValueNum, // wide uint64 widens
		protocol.ScalarValueInt, // small int fits
	}
	for i, want := range wantTags {
		var s protocol.Scalar
		if !g.Data(&s, i) {
			t.Fatalf("cell %d missing", i)
		}
		if s.ValType() != want {
			t.Fatalf("cell %d tag = %v, want %v", i, s.ValType(), want)
		}
	}

	// The widened cells must carry the exact value as float64.
	for _, i := range []int{1, 2} {
		var s protocol.Scalar
		g.Data(&s, i)
		var tbl flatbuffers.Table
		s.Val(&tbl)
		var nv protocol.Num
		nv.Init(tbl.Bytes, tbl.Pos)
		if nv.Val() != float64(wide) {
			t.Errorf("cell %d Num = %v, want %v", i, nv.Val(), float64(wide))
		}
	}
}
