package server

import (
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
)

// buildSetRefCacheGrid builds a SetRefCacheRequest{key, val=Any(Grid)} payload
// — the exact bytes the C++ rtd/rtd-once wrapper ships over MSG_SETREFCACHE —
// from a [][]any grid, and returns the finished FlatBuffer.
func buildSetRefCacheGrid(t *testing.T, key string, grid [][]any) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(256)
	gridOff, err := BuildGridFromGo(b, grid)
	if err != nil {
		t.Fatalf("BuildGridFromGo: %v", err)
	}
	protocol.AnyStart(b)
	protocol.AnyAddValType(b, protocol.AnyValueGrid)
	protocol.AnyAddVal(b, gridOff)
	anyOff := protocol.AnyEnd(b)

	keyOff := b.CreateString(key)
	protocol.SetRefCacheRequestStart(b)
	protocol.SetRefCacheRequestAddKey(b, keyOff)
	protocol.SetRefCacheRequestAddVal(b, anyOff)
	b.Finish(protocol.SetRefCacheRequestEnd(b))
	return b.FinishedBytes()
}

// TestResolveGridArg_HandlerReceivesCorrectView verifies the dispatch-level
// resolution: a populated RefCache + a content-hash token yields the correct
// *protocol.Grid read view (rows/cols match what the wrapper serialized).
func TestResolveGridArg_HandlerReceivesCorrectView(t *testing.T) {
	rc := NewRefCache()
	const token = "h:0011223344556677"
	rc.Set(token, buildSetRefCacheGrid(t, token, [][]any{
		{int32(1), int32(2), int32(3)},
		{int32(4), int32(5), int32(6)},
	}))

	g, err := ResolveGridArg(rc, token)
	if err != nil {
		t.Fatalf("ResolveGridArg: %v", err)
	}
	if g.Rows() != 2 || g.Cols() != 3 {
		t.Fatalf("grid dims = %dx%d, want 2x3", g.Rows(), g.Cols())
	}
}

// TestResolveGridArg_MissingTokenErrors verifies a token absent from the cache
// (e.g. cleared before the RTD connect ran) is reported as an error so the
// dispatch can push a clear value instead of hanging at #GETTING_DATA.
func TestResolveGridArg_MissingTokenErrors(t *testing.T) {
	rc := NewRefCache()
	if _, err := ResolveGridArg(rc, "h:deadbeef"); err == nil {
		t.Fatal("ResolveGridArg with missing token must return an error")
	}
}

// TestResolveGridArg_TypeMismatchErrors verifies a payload whose union is not a
// Grid is rejected (e.g. an "any" scalar shipped where a grid arg is expected).
func TestResolveGridArg_TypeMismatchErrors(t *testing.T) {
	rc := NewRefCache()
	const token = "h:aaaa"
	// Build a SetRefCacheRequest carrying an Int (not a Grid).
	b := flatbuffers.NewBuilder(64)
	protocol.IntStart(b)
	protocol.IntAddVal(b, 42)
	intOff := protocol.IntEnd(b)
	protocol.AnyStart(b)
	protocol.AnyAddValType(b, protocol.AnyValueInt)
	protocol.AnyAddVal(b, intOff)
	anyOff := protocol.AnyEnd(b)
	keyOff := b.CreateString(token)
	protocol.SetRefCacheRequestStart(b)
	protocol.SetRefCacheRequestAddKey(b, keyOff)
	protocol.SetRefCacheRequestAddVal(b, anyOff)
	b.Finish(protocol.SetRefCacheRequestEnd(b))
	rc.Set(token, b.FinishedBytes())

	if _, err := ResolveGridArg(rc, token); err == nil {
		t.Fatal("ResolveGridArg with non-Grid payload must return an error")
	}
}

// TestResolveGridArg_SurvivesClear is the copy-safety regression: a view
// resolved BEFORE RefCache.Clear must remain readable AFTER Clear, because
// RefCache.Get returns an independent copy. This is what makes resolving inside
// HandleRtdConnect's detached goroutine safe against the calc-end clear.
func TestResolveGridArg_SurvivesClear(t *testing.T) {
	rc := NewRefCache()
	const token = "h:cafebabe"
	rc.Set(token, buildSetRefCacheGrid(t, token, [][]any{{int32(7), int32(8)}}))

	g, err := ResolveGridArg(rc, token)
	if err != nil {
		t.Fatalf("ResolveGridArg: %v", err)
	}

	// Simulate the calc-end clear racing a late connect goroutine.
	rc.Clear()

	// The view must still be valid (it aliases the copy, not the cache).
	if g.Rows() != 1 || g.Cols() != 2 {
		t.Fatalf("grid dims after Clear = %dx%d, want 1x2", g.Rows(), g.Cols())
	}
	// And a fresh lookup now misses.
	if _, err := ResolveGridArg(rc, token); err == nil {
		t.Fatal("ResolveGridArg after Clear must miss")
	}
}
