package server

import (
	"fmt"
	"strings"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
)

// RefArgTokenPrefix is the marker the C++ rtd/rtd-once wrapper prepends to a
// content-hash topic token for a composite argument (grid/range/numgrid/any).
// It is collision-proof against any token a scalar string argument could
// legitimately produce, so it can be sniffed if needed — though the generated
// dispatch decodes composite positions by the (generator-known) argument type,
// not by this prefix. See AGENTS.md §19.3.
const RefArgTokenPrefix = "h:"

// resolveRefAny looks a composite RTD argument's content-hash token up in the
// per-cycle RefCache and returns the cached *protocol.Any payload view.
//
// Safety against the calc-end Clear (AGENTS.md §19.3): RefCache.Get returns an
// INDEPENDENT COPY of the stored bytes, so the returned *protocol.Any (and any
// typed sub-view derived from it) aliases that copy, NOT the cache's backing
// array. A concurrent RefCache.Clear() therefore cannot invalidate a value we
// have already resolved. The only failure mode is a MISS (the payload was
// cleared before this RTD connect ran, e.g. a server restart mid-cycle), which
// is reported as an error so the caller can push a clear value to the topic
// rather than hanging at #GETTING_DATA.
//
// The token is accepted with or without the RefArgTokenPrefix.
func resolveRefAny(refCache *RefCache, token string) (*protocol.Any, error) {
	if refCache == nil {
		return nil, fmt.Errorf("refcache: nil cache resolving token %q", token)
	}
	key := token
	data, ok := refCache.Get(key)
	if !ok {
		// Be tolerant of an un-prefixed lookup as well (the wrapper always
		// prefixes, but keep this robust).
		if alt, hadPrefix := strings.CutPrefix(token, RefArgTokenPrefix); hadPrefix {
			if data, ok = refCache.Get(alt); ok {
				key = alt
			}
		}
	}
	if !ok {
		return nil, fmt.Errorf("refcache: no payload for composite-arg token %q (the per-cycle cache was cleared before this RTD connect — the server may have restarted mid-cycle)", token)
	}
	// data is a fresh copy; the SetRefCacheRequest root (and the Any inside it)
	// alias `data`, which outlives any cache Clear.
	req := protocol.GetRootAsSetRefCacheRequest(data, 0)
	any := req.Val(nil)
	if any == nil {
		return nil, fmt.Errorf("refcache: payload for token %q has no value", key)
	}
	return any, nil
}

// ResolveGridArg resolves a composite RTD argument token to a *protocol.Grid
// view. Returns an error if the token is missing or the cached payload is not
// a Grid (mirrors how the sync handler glue passes the typed read view to the
// user handler).
func ResolveGridArg(refCache *RefCache, token string) (*protocol.Grid, error) {
	any, err := resolveRefAny(refCache, token)
	if err != nil {
		return nil, err
	}
	if any.ValType() != protocol.AnyValueGrid {
		return nil, fmt.Errorf("refcache: token %q payload is %v, want Grid", token, any.ValType())
	}
	var tbl flatbuffers.Table
	if !any.Val(&tbl) {
		return nil, fmt.Errorf("refcache: token %q has empty Grid union", token)
	}
	g := new(protocol.Grid)
	g.Init(tbl.Bytes, tbl.Pos)
	return g, nil
}

// ResolveNumGridArg resolves a composite RTD argument token to a
// *protocol.NumGrid view.
func ResolveNumGridArg(refCache *RefCache, token string) (*protocol.NumGrid, error) {
	any, err := resolveRefAny(refCache, token)
	if err != nil {
		return nil, err
	}
	if any.ValType() != protocol.AnyValueNumGrid {
		return nil, fmt.Errorf("refcache: token %q payload is %v, want NumGrid", token, any.ValType())
	}
	var tbl flatbuffers.Table
	if !any.Val(&tbl) {
		return nil, fmt.Errorf("refcache: token %q has empty NumGrid union", token)
	}
	ng := new(protocol.NumGrid)
	ng.Init(tbl.Bytes, tbl.Pos)
	return ng, nil
}

// ResolveRangeArg resolves a composite RTD argument token to a *protocol.Range
// view.
func ResolveRangeArg(refCache *RefCache, token string) (*protocol.Range, error) {
	any, err := resolveRefAny(refCache, token)
	if err != nil {
		return nil, err
	}
	if any.ValType() != protocol.AnyValueRange {
		return nil, fmt.Errorf("refcache: token %q payload is %v, want Range", token, any.ValType())
	}
	var tbl flatbuffers.Table
	if !any.Val(&tbl) {
		return nil, fmt.Errorf("refcache: token %q has empty Range union", token)
	}
	r := new(protocol.Range)
	r.Init(tbl.Bytes, tbl.Pos)
	return r, nil
}

// ResolveAnyArg resolves a composite RTD argument token to the *protocol.Any
// payload view (for an "any"-typed argument). The handler receives the Any
// directly, exactly like the sync "any" arg path.
func ResolveAnyArg(refCache *RefCache, token string) (*protocol.Any, error) {
	return resolveRefAny(refCache, token)
}
