package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// rtdCompositeCfg builds a config exercising the content-hash payload path:
// one plain-rtd function with a range arg and one rtd-once function with a grid
// arg, both alongside a scalar arg to confirm scalar topics still stringify.
func rtdCompositeCfg() *config.Config {
	return &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{
				Name:   "RangeTick",
				Mode:   "rtd",
				Return: "float",
				Args:   []config.Arg{{Name: "r", Type: "range"}, {Name: "n", Type: "int"}},
			},
			{
				Name:   "SumGridOnce",
				Mode:   "rtd-once",
				Return: "float",
				Args:   []config.Arg{{Name: "g", Type: "grid"}},
			},
		},
		Rtd: config.RtdConfig{
			Enabled:     true,
			ProgID:      "TestProj.Rtd",
			Clsid:       "{11111111-2222-3333-4444-555555555555}",
			Description: "t",
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}
}

// TestGenCpp_RtdComposite_Wrapper: the rtd / rtd-once C++ wrappers emit the
// content-hash machinery (ContentHashToken, SetRefCacheRequest build,
// SendRefCachePayloadOnce) for composite args and NEVER emit the old
// "[Complex]" literal.
func TestGenCpp_RtdComposite_Wrapper(t *testing.T) {
	t.Parallel()
	content := renderCppMain(t, rtdCompositeCfg())

	if strings.Contains(content, "[Complex]") {
		t.Errorf("xll_main.cpp: composite RTD args must no longer serialize to the \"[Complex]\" literal:\n%s", content)
	}

	for _, want := range []string{
		// range arg -> type-tagged ContentHashToken + ConvertRange wrapped in Any::Range.
		"xll::ContentHashToken('r', r)",
		"ConvertRange(r, rcb)",
		"protocol::AnyValue::Range",
		// grid arg -> type-tagged ContentHashToken + ConvertGridArg (ref-coercing,
		// with the coerce-failure out-param so a failed coerce SKIPS the ship)
		// wrapped in Any::Grid.
		"xll::ContentHashToken('g', g)",
		"xll::ConvertGridArg(g, rcb, &rcOk)",
		"protocol::AnyValue::Grid",
		// SetRefCacheRequest build + once-per-cycle ship, gated on rcOk so a
		// degenerate payload is never shipped under a real token.
		"protocol::CreateSetRefCacheRequest(rcb, rcKey, rcAny)",
		"xll::SendRefCachePayloadOnce(",
		"if (!rcOk) {",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("xll_main.cpp (composite RTD args) missing %q", want)
		}
	}

	// Scalar args in the same functions still stringify into topics.
	if !strings.Contains(content, "std::to_wstring(n)") {
		t.Errorf("xll_main.cpp: scalar rtd args must still stringify into topic strings:\n%s", content)
	}
}

// TestGenCpp_RtdOnceComposite_ShipOrdering: the rtd-once wrapper must ship the
// composite payload ONLY on a once-cache miss — i.e. the SendRefCachePayloadOnce
// call sits AFTER the MakeRtdOnceKey/TryGetResult early-return. A memoize/TTL
// hit returns the cached value without xlfRtd, so no ConnectData fires and no
// payload is needed; shipping on a hit would be wasted SHM traffic and would
// blur the content-addressed memoization contract (AGENTS.md §19.3).
func TestGenCpp_RtdOnceComposite_ShipOrdering(t *testing.T) {
	t.Parallel()
	cfg := rtdCompositeCfg()
	for i := range cfg.Functions {
		cfg.Functions[i].Mode = "rtd-once"
		cfg.Functions[i].Return = "float"
	}
	content := renderCppMain(t, cfg)

	keyIdx := strings.Index(content, "xll::MakeRtdOnceKey(topics)")
	hitIdx := strings.Index(content, "TryGetResult(onceKey, &cached)")
	shipIdx := strings.Index(content, "xll::SendRefCachePayloadOnce(")
	if keyIdx < 0 || hitIdx < 0 || shipIdx < 0 {
		t.Fatalf("rtd-once composite render missing markers (key=%d hit=%d ship=%d)", keyIdx, hitIdx, shipIdx)
	}
	if !(keyIdx < hitIdx && hitIdx < shipIdx) {
		t.Errorf("rtd-once must ship composite payloads AFTER the once-cache hit check (key=%d hit=%d ship=%d) — shipping before the TryGetResult early-return sends payloads on memoize hits", keyIdx, hitIdx, shipIdx)
	}
}

// TestGenCpp_RtdOnceComposite_BuildAfterCacheLookup pins the stronger property
// TestGenCpp_RtdOnceComposite_ShipOrdering only half-covers: not just the SHIP
// but the whole payload BUILD (the converter call + the SetRefCacheRequest
// serialization) must sit after the once-cache early-return.
//
// Why it matters: serializing a composite argument is O(payload) — protocol::Grid
// is one union table PER CELL, so a 10k-cell grid argument costs hundreds of
// microseconds to milliseconds — and on a memoize/TTL hit the wrapper returns
// the cached value WITHOUT calling xlfRtd, so all of it was thrown away. Only
// the content-hash TOKEN is genuinely needed before the lookup (it is what makes
// the once-key content-addressed). A single rtd-once function is rendered here
// so the marker indices are unambiguous.
func TestGenCpp_RtdOnceComposite_BuildAfterCacheLookup(t *testing.T) {
	t.Parallel()
	cfg := rtdCompositeCfg()
	cfg.Functions = []config.Function{{
		Name:   "SumGridOnce",
		Mode:   "rtd-once",
		Return: "float",
		Args:   []config.Arg{{Name: "g", Type: "grid"}},
	}}
	content := renderCppMain(t, cfg)

	tokIdx := strings.Index(content, "xll::ContentHashToken('g', g)")
	hitIdx := strings.Index(content, "TryGetResult(onceKey, &cached)")
	buildIdx := strings.Index(content, "xll::ConvertGridArg(g, rcb, &rcOk)")
	reqIdx := strings.Index(content, "protocol::CreateSetRefCacheRequest(rcb, rcKey, rcAny)")
	shipIdx := strings.Index(content, "xll::SendRefCachePayloadOnce(")
	if tokIdx < 0 || hitIdx < 0 || buildIdx < 0 || reqIdx < 0 || shipIdx < 0 {
		t.Fatalf("rtd-once composite render missing markers (tok=%d hit=%d build=%d req=%d ship=%d)",
			tokIdx, hitIdx, buildIdx, reqIdx, shipIdx)
	}
	// The token must still be computed BEFORE the lookup (it feeds the key).
	if !(tokIdx < hitIdx) {
		t.Errorf("the content-hash token must be computed before the once-cache lookup (tok=%d hit=%d) — the once-key is built from the topic strings", tokIdx, hitIdx)
	}
	// The payload build must be AFTER it.
	for _, m := range []struct {
		name string
		idx  int
	}{{"ConvertGridArg", buildIdx}, {"CreateSetRefCacheRequest", reqIdx}, {"SendRefCachePayloadOnce", shipIdx}} {
		if m.idx < hitIdx {
			t.Errorf("%s (idx=%d) must be emitted AFTER the once-cache TryGetResult early-return (idx=%d) — an eager build is fully discarded on a memoize/TTL hit", m.name, m.idx, hitIdx)
		}
	}
	// And no refPayload staging vector should survive: the build now happens at
	// the ship site, so the whole-buffer copy into a std::vector is gone.
	if strings.Contains(content, "refPayload") {
		t.Errorf("rtd-once must no longer stage the payload in a refPayload vector — build directly at the ship site:\n%s", content)
	}
}

// TestGenCpp_RtdComposite_SkipBuildWhenAlreadyShipped: plain rtd has no
// once-cache to branch on, so the wasted-build guard is a peek at the same
// g_sentRefCache the ship dedups on. Without it, N cells sharing one range each
// serialize the whole grid and N-1 of those buffers are dropped inside
// SendRefCachePayloadOnce.
func TestGenCpp_RtdComposite_SkipBuildWhenAlreadyShipped(t *testing.T) {
	t.Parallel()
	content := renderCppMain(t, rtdCompositeCfg())

	for _, want := range []string{
		"std::lock_guard<std::mutex> rcLock(g_refCacheMutex);",
		"g_sentRefCache.find(tok1) != g_sentRefCache.end();",
		"if (!rcAlreadySent1) {",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("xll_main.cpp (plain rtd composite arg) missing already-shipped guard %q", want)
		}
	}

	// The guard must precede the builder, or it guards nothing.
	guardIdx := strings.Index(content, "rcAlreadySent1 = g_sentRefCache.find(tok1)")
	buildIdx := strings.Index(content, "ConvertRange(r, rcb)")
	if guardIdx < 0 || buildIdx < 0 || guardIdx > buildIdx {
		t.Errorf("the already-shipped peek must precede the payload build (guard=%d build=%d)", guardIdx, buildIdx)
	}
}

// TestGenCpp_RtdOnceComposite_SkipBuildWhenAlreadyShipped is the rtd-once twin
// of the plain-rtd guard above. v0.8.35 added the g_sentRefCache peek to plain
// rtd only; rtd-once kept building unconditionally on a once-cache miss.
//
// Why that matters MORE here, not less: the once-cache is what usually spares
// rtd-once the work, but on the FIRST calc pass it is EMPTY. So N cells sharing
// one grid all miss the once-cache, all N reach the payload build, all N
// serialize the whole grid (protocol::Grid is one union table per cell), and
// SendRefCachePayloadOnce drops N-1 of the buffers on its token dedup. The peek
// is the same `{ lock_guard; find; }` the plain-rtd branch uses, with the same
// safety argument: entries are written only after a successful ack, so PRESENT
// means known-delivered, and a miss just falls through to the ship, which
// re-checks under the same mutex.
func TestGenCpp_RtdOnceComposite_SkipBuildWhenAlreadyShipped(t *testing.T) {
	t.Parallel()
	cfg := rtdCompositeCfg()
	cfg.Functions = []config.Function{{
		Name:   "SumGridOnce",
		Mode:   "rtd-once",
		Return: "float",
		Args:   []config.Arg{{Name: "g", Type: "grid"}},
	}}
	content := renderCppMain(t, cfg)

	for _, want := range []string{
		"std::lock_guard<std::mutex> rcLock(g_refCacheMutex);",
		"g_sentRefCache.find(refTok1) != g_sentRefCache.end();",
		"if (!rcAlreadySent1) {",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("xll_main.cpp (rtd-once composite arg) missing already-shipped guard %q", want)
		}
	}

	// Ordering: cache lookup -> peek -> build -> ship. The peek must sit AFTER
	// the once-cache early-return (a memoize hit must not even peek) and BEFORE
	// the builder (or it guards nothing).
	hitIdx := strings.Index(content, "TryGetResult(onceKey, &cached)")
	peekIdx := strings.Index(content, "rcAlreadySent1 = g_sentRefCache.find(refTok1)")
	buildIdx := strings.Index(content, "xll::ConvertGridArg(g, rcb, &rcOk)")
	shipIdx := strings.Index(content, "xll::SendRefCachePayloadOnce(")
	if hitIdx < 0 || peekIdx < 0 || buildIdx < 0 || shipIdx < 0 {
		t.Fatalf("rtd-once composite render missing markers (hit=%d peek=%d build=%d ship=%d)",
			hitIdx, peekIdx, buildIdx, shipIdx)
	}
	if !(hitIdx < peekIdx && peekIdx < buildIdx && buildIdx < shipIdx) {
		t.Errorf("rtd-once already-shipped peek must sit between the once-cache lookup and the payload build "+
			"(hit=%d peek=%d build=%d ship=%d)", hitIdx, peekIdx, buildIdx, shipIdx)
	}

	// The FlatBufferBuilder itself must be INSIDE the guard: constructing it
	// (1024-byte arena) is part of the work the peek exists to skip.
	guardBodyIdx := strings.Index(content, "if (!rcAlreadySent1) {")
	builderIdx := strings.Index(content, "flatbuffers::FlatBufferBuilder rcb(1024);")
	if builderIdx < guardBodyIdx {
		t.Errorf("the rtd-once payload FlatBufferBuilder (idx=%d) must be constructed inside the "+
			"already-shipped guard (idx=%d), not before it", builderIdx, guardBodyIdx)
	}
}

// TestGenCpp_RtdComposite_FP12Hash: a numgrid arg uses the FP12 overload of the
// content-hash helper and the NumGrid converter.
func TestGenCpp_RtdComposite_FP12Hash(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "NumTick", Mode: "rtd", Return: "float", Args: []config.Arg{{Name: "m", Type: "numgrid"}}},
		},
		Rtd: config.RtdConfig{
			Enabled: true, ProgID: "TestProj.Rtd",
			Clsid: "{11111111-2222-3333-4444-555555555555}", Description: "t",
		},
		Server: config.ServerConfig{Timeout: "2s", Launch: &config.LaunchConfig{Enabled: new(bool)}},
	}
	content := renderCppMain(t, cfg)

	for _, want := range []string{
		"xll::ContentHashTokenFP12(m)",
		"ConvertNumGrid(m, rcb)",
		"protocol::AnyValue::NumGrid",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("xll_main.cpp (numgrid rtd arg) missing %q", want)
		}
	}
}

// TestGenGo_RtdComposite_Dispatch: the generated server dispatch resolves
// composite-arg tokens from the RefCache for both rtd and rtd-once functions
// and passes the typed view to the handler.
func TestGenGo_RtdComposite_Dispatch(t *testing.T) {
	t.Parallel()
	srv := renderTemplate(t, "server.go.tmpl", serverDataFor(rtdCompositeCfg()))
	assertParses(t, "server.go", srv)

	for _, want := range []string{
		// rtd range arg resolution + typed handler call.
		"server.ResolveRangeArg(refCache, args[1])",
		"handler.RangeTick_RTD(ctx, topicID , rarg_r, server.ParseInt(args[2]))",
		// rtd-once grid arg resolution + typed handler call inside RunOnce.
		"server.ResolveGridArg(refCache, args[1])",
		"return handler.SumGridOnce(ctx , rarg_g)",
		// missing-hash pushes a clear ERROR value (never hangs at #GETTING_DATA,
		// and is_error=true so the miss is not memoized as a result).
		"rtd.GlobalRtd.SendErrorUpdate(topicID, rerr_r.Error())",
		"rtd.GlobalRtd.SendErrorUpdate(topicID, rerr_g.Error())",
	} {
		if !strings.Contains(srv, want) {
			t.Errorf("server.go (composite RTD dispatch) missing %q:\n%s", want, srv)
		}
	}
}

// TestGenGo_RtdComposite_Interface: composite args appear as the same typed
// read views the sync handlers receive (*protocol.Range / *protocol.Grid).
func TestGenGo_RtdComposite_Interface(t *testing.T) {
	t.Parallel()
	iface := renderTemplate(t, "interface.go.tmpl", interfaceDataFor(rtdCompositeCfg()))
	assertParses(t, "interface.go", iface)

	if !strings.Contains(iface, "RangeTick_RTD(ctx context.Context, topicID int32, r *protocol.Range, n int32) error") {
		t.Errorf("interface.go: rtd composite arg must be the *protocol.Range view:\n%s", iface)
	}
	if !strings.Contains(iface, "SumGridOnce(ctx context.Context, g *protocol.Grid) (float64, error)") {
		t.Errorf("interface.go: rtd-once composite arg must be the *protocol.Grid view:\n%s", iface)
	}
}
