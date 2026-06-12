package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// rtdOnceCfg builds a config with one rtd-once function (scalar args, scalar
// return) plus RTD enabled, in the normalized shape ApplyDefaults produces.
func rtdOnceCfg(memoize bool) *config.Config {
	return &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{
				Name:    "SlowAdd",
				Mode:    "rtd-once",
				Return:  "float",
				Memoize: memoize,
				Args:    []config.Arg{{Name: "a", Type: "int"}, {Name: "b", Type: "float"}},
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

// serverData mirrors the anonymous struct built in generateServer.
func serverDataFor(cfg *config.Config) any {
	return struct {
		Package       string
		ModName       string
		ProjectName   string
		Functions     []config.Function
		Events        []config.Event
		Commands      []config.Command
		ServerTimeout string
		ServerWorkers int
		Version       string
		Logging       config.LoggingConfig
		Rtd           config.RtdConfig
		Chunk         *config.ChunkConfig
	}{
		Package:     "generated",
		ModName:     "testmod",
		ProjectName: cfg.Project.Name,
		Functions:   cfg.Functions,
		Version:     "test",
		Logging:     config.LoggingConfig{Level: "info", Dir: "logs"},
		Rtd:         cfg.Rtd,
	}
}

// interfaceDataFor mirrors the anonymous struct built in generateInterface.
func interfaceDataFor(cfg *config.Config) any {
	return struct {
		Package   string
		ModName   string
		Functions []config.Function
		Events    []config.Event
		Commands  []config.Command
		Version   string
		Rtd       config.RtdConfig
	}{
		Package:   "generated",
		ModName:   "testmod",
		Functions: cfg.Functions,
		Version:   "test",
		Rtd:       cfg.Rtd,
	}
}

// TestGenGo_RtdOnce_Interface: an rtd-once function is declared as a NORMAL
// (sync-shaped) handler — NOT the _RTD push-style signature.
func TestGenGo_RtdOnce_Interface(t *testing.T) {
	t.Parallel()
	iface := renderTemplate(t, "interface.go.tmpl", interfaceDataFor(rtdOnceCfg(false)))
	assertParses(t, "interface.go", iface)

	// Normal handler shape: (ctx, args...) (T, error).
	if !strings.Contains(iface, "SlowAdd(ctx context.Context, a int32, b float64) (float64, error)") {
		t.Errorf("interface.go: rtd-once must declare a normal handler signature:\n%s", iface)
	}
	// Must NOT emit the RTD push-style method.
	if strings.Contains(iface, "SlowAdd_RTD(") {
		t.Errorf("interface.go: rtd-once must NOT emit a _RTD push handler:\n%s", iface)
	}
}

// TestGenGo_RtdOnce_Server: the dispatch routes a connect for an rtd-once
// function into rtd.RunOnce wrapping the normal handler, parses the topic-
// string scalar args, and does NOT generate the sync/async handle function.
func TestGenGo_RtdOnce_Server(t *testing.T) {
	t.Parallel()
	srv := renderTemplate(t, "server.go.tmpl", serverDataFor(rtdOnceCfg(false)))
	assertParses(t, "server.go", srv)

	// RunOnce glue present, wrapping the normal handler call.
	if !strings.Contains(srv, "rtd.RunOnce(ctx, rtd.GlobalRtd, topicID, func(ctx context.Context) (interface{}, error) {") {
		t.Errorf("server.go: missing rtd.RunOnce glue for rtd-once function:\n%s", srv)
	}
	if !strings.Contains(srv, "return handler.SlowAdd(ctx , server.ParseInt(args[1]), server.ParseFloat(args[2]))") {
		t.Errorf("server.go: RunOnce closure must call the normal handler with parsed scalar args:\n%s", srv)
	}
	// The connect case is keyed on the function name.
	if !strings.Contains(srv, `case "SlowAdd":`) {
		t.Errorf("server.go: rtd-once dispatch must switch on the function name:\n%s", srv)
	}
	// No sync/async handle function for an rtd-once function.
	if strings.Contains(srv, "func handleSlowAdd(") {
		t.Errorf("server.go: rtd-once must NOT generate a sync/async handle function:\n%s", srv)
	}
	// No MsgUserStart dispatch case for an rtd-once function (it never receives
	// a user request over the function message path).
	if strings.Contains(srv, "// SlowAdd\n") && strings.Contains(srv, "handleSlowAdd") {
		t.Errorf("server.go: rtd-once must not have a user-message dispatch case")
	}
}

// TestGenCpp_RtdOnce: the C++ wrapper registers like rtd (Q...$), returns
// LPXLOPER12, returns #GETTING_DATA via the registry, consults RtdOnceResults
// before xlfRtd, and the once-function set is installed at xlAutoOpen.
func TestGenCpp_RtdOnce(t *testing.T) {
	t.Parallel()
	content := renderCppMain(t, rtdOnceCfg(false))

	for _, want := range []string{
		// rtd-once support header is included.
		`#include "xll_rtd_once.h"`,
		// Registers the function-name set at xlAutoOpen (memoize set empty here).
		`xll::RtdOnceRegistry::Instance().SetFunctionNames(`,
		`L"SlowAdd", `,
		// LPXLOPER12 return type (same as rtd).
		`extern "C" __declspec(dllexport) LPXLOPER12 __stdcall SlowAdd(`,
		// Cache-hit short-circuit before xlfRtd.
		"xll::RtdOnceRegistry::Instance().TryGetResult(onceKey, &cached)",
		"xll::RtdOnceResultToXLOPER12(cached)",
		// Topic key built from the topic strings.
		"xll::MakeRtdOnceKey(topics)",
		// Still calls xlfRtd on a miss.
		"xll::CallExcel(xlfRtd, &xRes,",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("xll_main.cpp (rtd-once) missing %q", want)
		}
	}

	// Registration type string must be Q (return) + arg chars + $, like rtd.
	// SlowAdd(int a, float b) -> "QJB$".
	if !strings.Contains(content, `std::wstring typeStr = L"QJB$";`) {
		t.Errorf("xll_main.cpp: rtd-once registration type string must be QJB$:\n%s", content)
	}

	// The rtd-once wrapper must NOT go through the sync slot.Send path.
	if strings.Contains(content, "handleSlowAdd") {
		t.Errorf("xll_main.cpp: rtd-once must not emit a sync send path")
	}
}

// TestGenCpp_RtdOnce_Memoize: with memoize:true, the function name appears in
// the memoize subset passed to SetFunctionNames.
func TestGenCpp_RtdOnce_Memoize(t *testing.T) {
	t.Parallel()
	content := renderCppMain(t, rtdOnceCfg(true))

	// SetFunctionNames second argument (memoize set) must include SlowAdd.
	idx := strings.Index(content, "xll::RtdOnceRegistry::Instance().SetFunctionNames(")
	if idx < 0 {
		t.Fatalf("SetFunctionNames call missing:\n%s", content)
	}
	call := content[idx : idx+300]
	// Two brace-lists are emitted: names then memoizeNames. With memoize:true
	// SlowAdd must appear twice in the call region.
	if strings.Count(call, `L"SlowAdd", `) < 2 {
		t.Errorf("memoize:true must add SlowAdd to BOTH the name set and the memoize set:\n%s", call)
	}
}

// rtdOnceTTLCfg builds a config with one rtd-once function declaring a
// memoize_ttl, in the normalized shape ApplyDefaults produces.
func rtdOnceTTLCfg(ttl string) *config.Config {
	return &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{
				Name:       "SlowAdd",
				Mode:       "rtd-once",
				Return:     "float",
				MemoizeTTL: ttl,
				Args:       []config.Arg{{Name: "a", Type: "int"}, {Name: "b", Type: "float"}},
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

// TestGenCpp_RtdOnce_MemoizeTTL: with memoize_ttl set, the SetFunctionNames
// call carries a name->ttl-ms pair in its third argument (the TTL list), with
// the milliseconds computed at generation time from the parsed duration.
func TestGenCpp_RtdOnce_MemoizeTTL(t *testing.T) {
	t.Parallel()
	content := renderCppMain(t, rtdOnceTTLCfg("30s"))

	idx := strings.Index(content, "xll::RtdOnceRegistry::Instance().SetFunctionNames(")
	if idx < 0 {
		t.Fatalf("SetFunctionNames call missing:\n%s", content)
	}
	call := content[idx : idx+400]
	// 30s -> 30000 ms, emitted as a { L"SlowAdd", 30000ULL } pair.
	if !strings.Contains(call, `{ L"SlowAdd", 30000ULL }`) {
		t.Errorf("memoize_ttl:30s must emit the name->ms pair {L\"SlowAdd\", 30000ULL} in SetFunctionNames:\n%s", call)
	}
	// memoize_ttl is NOT memoize:true: SlowAdd must appear exactly once as a
	// name (first arg) and once in the TTL pair, never in the memoize subset.
	// The memoize (second) brace-list must be empty for this function.
}

// TestGenCpp_RtdOnce_NoTTLWhenAbsent: a plain "once" rtd-once function emits no
// TTL pair (the third SetFunctionNames argument stays empty).
func TestGenCpp_RtdOnce_NoTTLWhenAbsent(t *testing.T) {
	t.Parallel()
	content := renderCppMain(t, rtdOnceCfg(false))

	idx := strings.Index(content, "xll::RtdOnceRegistry::Instance().SetFunctionNames(")
	if idx < 0 {
		t.Fatalf("SetFunctionNames call missing:\n%s", content)
	}
	call := content[idx : idx+400]
	if strings.Contains(call, "ULL }") {
		t.Errorf("a non-TTL rtd-once function must not emit any name->ms pair:\n%s", call)
	}
}

// TestGenCpp_RtdOnce_NotPresentWhenAbsent: a project with no rtd-once function
// must not emit the rtd-once machinery (header include / registry call).
func TestGenCpp_RtdOnce_NotPresentWhenAbsent(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "Tick", Return: "any", Mode: "rtd"},
		},
		Rtd: config.RtdConfig{
			Enabled: true, ProgID: "TestProj.Rtd",
			Clsid: "{11111111-2222-3333-4444-555555555555}", Description: "t",
		},
		Server: config.ServerConfig{Timeout: "2s", Launch: &config.LaunchConfig{Enabled: new(bool)}},
	}
	content := renderCppMain(t, cfg)
	for _, bad := range []string{`#include "xll_rtd_once.h"`, "RtdOnceRegistry", "RtdOnceResultToXLOPER12"} {
		if strings.Contains(content, bad) {
			t.Errorf("rtd-only render must not emit %q", bad)
		}
	}
}
