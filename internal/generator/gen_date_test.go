package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/assets"
	"github.com/xll-gen/xll-gen/internal/config"
)

// dateArgFunctions returns a single function with a date arg (and a date
// return) in the normalized shape ApplyDefaults produces.
func dateArgFunctions() []config.Function {
	return []config.Function{
		{
			Name:   "DateEcho",
			Mode:   "sync",
			Return: "date",
			Args:   []config.Arg{{Name: "d", Type: "date"}},
		},
	}
}

// TestGen_DateArgDecode asserts that a function with a date arg emits the
// server.SerialToTime decode line rather than falling through to the raw
// request accessor. A date arg rides the existing double request path and is
// decoded back to a time.Time in the generated server.
func TestGen_DateArgDecode(t *testing.T) {
	t.Parallel()
	data := struct {
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
		ProjectName: "TestProj",
		Functions:   dateArgFunctions(),
		Version:     "test",
		Logging:     config.LoggingConfig{Level: "info", Dir: "logs"},
	}

	srv := renderTemplate(t, "server.go.tmpl", data)
	assertParses(t, "server.go", srv)

	want := "arg_d := server.SerialToTime(request.D())"
	if !strings.Contains(srv, want) {
		t.Errorf("server.go missing %q:\n%s", want, srv)
	}
}

// TestGenCpp_DateFormatWiring pins Plan B / Task 4: the date auto-format
// producers (ScheduleDateFormatsForCaller) are wired at the sync any/grid
// return sites AND the grid-once spill, the calc-end drain
// (DrainAndApplyDateFormats lives in HandleCalculationEnded), the asset header
// is included UNGATED, and CalculationEnded is registered unconditionally so
// the drain runs even in a plain no-RTD build.
func TestGenCpp_DateFormatWiring(t *testing.T) {
	t.Parallel()

	// (a) Plain sync, NO rtd/ribbon/cache: still wires the producer at the
	// any+grid returns AND registers CalculationEnded unconditionally so the
	// drain runs. generateCppMain bypasses config.Validate, so composite
	// returns are renderable here (see TestGenCpp_ComplexReturnTypes).
	syncCfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "MakeDate", Return: "any", Args: []config.Arg{}},
			{Name: "MakeDateGrid", Return: "grid", Args: []config.Arg{}},
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}
	sync := renderCppMain(t, syncCfg)

	if !strings.Contains(sync, `#include "xll_date_format.h"`) {
		t.Errorf("sync render missing ungated #include \"xll_date_format.h\"")
	}
	// Producer at both the any and grid sync return sites. There are two sync
	// return blocks (cached-completion + fresh), so each return type yields two
	// occurrences => at least 4 producer calls total, but assert >= 2 to stay
	// robust to template restructuring.
	if n := strings.Count(sync, "xll::ScheduleDateFormatsForCaller("); n < 2 {
		t.Errorf("sync render: expected >=2 ScheduleDateFormatsForCaller calls (any+grid returns), got %d:\n%s", n, sync)
	}
	// CalculationEnded must be registered unconditionally (drain must run with
	// no rtd/ribbon/cache). The {{else}} auto-handler branch sets
	// needCalcEnded=true so the xlEventRegister fires.
	if !strings.Contains(sync, `xll::CallExcel(xlEventRegister, nullptr, L"CalculationEnded", xleventCalculationEnded);`) {
		t.Errorf("sync render must register CalculationEnded (drain runs in HandleCalculationEnded)")
	}
	if !strings.Contains(sync, "bool needCalcEnded = true;") {
		t.Errorf("sync render must force needCalcEnded=true so CalculationEnded is always registered")
	}
	// Producer must NOT be wired on the numgrid/FP12 return (NumGrid has no
	// dates) — keep that path clean.
	if strings.Contains(sync, "xll::ScheduleDateFormatsForCaller(resp->result());\n        return NumGridToFP12") ||
		strings.Contains(sync, "xll::ScheduleDateFormatsForCaller(resp->result());\n    return NumGridToFP12") {
		t.Errorf("numgrid return must NOT schedule date formats (NumGrid carries no dates)")
	}

	// (b) rtd-once-grid spill: the grid-once cache-hit spill schedules the
	// parsed Any before materializing. Reuse the rtd-once-grid config harness.
	gridOnce := renderCppMain(t, rtdOnceGridCfg())
	if !strings.Contains(gridOnce, "xll::ScheduleDateFormatsForCaller(any);") {
		t.Errorf("grid-once spill must schedule date formats on the parsed Any:\n%s", gridOnce)
	}
	if !strings.Contains(gridOnce, `#include "xll_date_format.h"`) {
		t.Errorf("grid-once render missing ungated #include \"xll_date_format.h\"")
	}

	// (c) The calc-end date-format drain lives in the always-compiled assets.
	// As of the 2026-06-15 reentrancy fix it no longer runs INLINE inside the
	// xleventCalculationEnded callback (HandleCalculationEnded): applying a
	// number format via xlcSelect/xlcFormatNumber inside the event re-enters
	// Excel's calc/RTD machinery during an rtd-once teardown window and crashes
	// Excel (0xc0000005). The drain now runs in the DEFERRED runner
	// (src/xll_deferred_commands.cpp, RunDeferredCalcEndCommands), scheduled via
	// xlcOnTime so it executes OUTSIDE the event. Assert:
	//   1. HandleCalculationEnded does NOT call the drain inline (it routes
	//      through DeferCalcEndCommands), and
	//   2. the deferred runner DOES call DrainAndApplyDateFormats, UNGATED.
	am, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	events := am["src/xll_events.cpp"]
	if events == "" {
		t.Fatalf("asset src/xll_events.cpp not found")
	}
	if start := strings.Index(events, "void HandleCalculationEnded()"); start >= 0 {
		if strings.Contains(events[start:], "xll::DrainAndApplyDateFormats();") {
			t.Errorf("HandleCalculationEnded must NOT call DrainAndApplyDateFormats() inline " +
				"(in-event cell mutation -> rtd-once reentrancy crash); it must defer it")
		}
	}
	if !strings.Contains(events, "xll::DeferCalcEndCommands(") {
		t.Errorf("HandleCalculationEnded must route calc-end work through xll::DeferCalcEndCommands")
	}

	deferred := am["src/xll_deferred_commands.cpp"]
	if deferred == "" {
		t.Fatalf("asset src/xll_deferred_commands.cpp not found")
	}
	if !strings.Contains(deferred, "xll::DrainAndApplyDateFormats();") {
		t.Errorf("deferred runner must call xll::DrainAndApplyDateFormats()")
	}
	// The deferred runner TU has no XLL_RTD_ENABLED gate; the drain runs in
	// non-RTD builds too. Guard against a future gate slipping in.
	if strings.Contains(deferred, "#ifdef XLL_RTD_ENABLED") {
		t.Errorf("deferred runner must stay UNGATED (no XLL_RTD_ENABLED) so date formatting works in non-RTD builds")
	}
}

// TestGenCpp_DateArgRequestBuilder pins the C++ request-builder codegen for a
// date ARGUMENT. A date arg rides the double request path: it is declared as a
// `double` C param (ArgCppType) and must be added to the request builder
// DIRECTLY (add_d(d)), exactly like a float/int/bool scalar. The earlier bug:
// date was missing from the scalar branch of the builder loop, so it fell to
// the else branch and emitted `reqBuilder.add_d(arg0)` referencing an
// undeclared `arg0` (no arg<N> is created for scalar types) → C++ compile
// failure. Regression for IMPROVEMENT_BACKLOG §"date 인자 + any 반환".
func TestGenCpp_DateArgRequestBuilder(t *testing.T) {
	t.Parallel()
	// generateCppMain bypasses config.Validate (see TestGenCpp_DateFormatWiring),
	// so we can pair a date arg with several return types to prove the arg-decode
	// path is return-type-independent.
	for _, ret := range []string{"int", "any", "grid"} {
		ret := ret
		t.Run(ret, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
				Functions: []config.Function{
					{Name: "DateArg", Mode: "sync", Return: ret, Args: []config.Arg{{Name: "d", Type: "date"}}},
				},
				Server: config.ServerConfig{
					Timeout: "2s",
					Launch:  &config.LaunchConfig{Enabled: new(bool)},
				},
			}
			content := renderCppMain(t, cfg)

			// The date scalar must be passed DIRECTLY to the builder.
			if !strings.Contains(content, "reqBuilder.add_d(d);") {
				t.Errorf("date arg must be added directly as a scalar (add_d(d)):\n%s", content)
			}
			// And must NOT reference an undeclared arg<N> offset.
			if strings.Contains(content, "reqBuilder.add_d(arg0)") {
				t.Errorf("date arg wrongly added via undeclared offset arg0 (should be add_d(d)):\n%s", content)
			}
		})
	}
}

// TestGen_DateArgInterface verifies the handler interface references time.Time
// for a date arg/return and that the generated interface file imports "time".
func TestGen_DateArgInterface(t *testing.T) {
	t.Parallel()
	data := struct {
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
		Functions: dateArgFunctions(),
		Version:   "test",
	}

	iface := renderTemplate(t, "interface.go.tmpl", data)
	assertParses(t, "interface.go", iface)

	for _, want := range []string{
		"DateEcho(ctx context.Context, d time.Time) (time.Time, error)",
		`"time"`,
	} {
		if !strings.Contains(iface, want) {
			t.Errorf("interface.go missing %q:\n%s", want, iface)
		}
	}
}
