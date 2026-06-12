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
		// missing-hash pushes a clear value (never hangs at #GETTING_DATA).
		"rtd.GlobalRtd.SendUpdate(topicID, rerr_r.Error())",
		"rtd.GlobalRtd.SendUpdate(topicID, rerr_g.Error())",
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
