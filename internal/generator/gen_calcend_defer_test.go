package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/assets"
	"github.com/xll-gen/xll-gen/internal/config"
)

// TestCalcEnd_DeferredRunner_AssetDoesNotMutateCellsInEvent is the structural
// regression for the showcase rtd-once-window reentrancy crash (0xc0000005 in
// EXCEL.EXE, ~7-8s after "Build Showcase Sheet").
//
// ROOT CAUSE (proven by single-variable bisection against a real-Excel repro):
// HandleCalculationEnded() — which runs INSIDE the xleventCalculationEnded
// macro callback — called ExecuteCommands(commands) (xlSet) and
// DrainAndApplyDateFormats() (xlcSelect/xlcFormatNumber) SYNCHRONOUSLY. When
// those cell writes land during an rtd-once materialize/disconnect window they
// re-enter Excel's calc/RTD machinery at a fragile point and Excel faults.
//
// FIX: cell-mutating work must NOT run inside the event callback. It is routed
// through a deferred runner (xll::DeferCalcEndCommands -> xlcOnTime macro ->
// xll::RunDeferredCalcEndCommands) that Excel dispatches OUTSIDE the event.
//
// This test pins the invariant on the shipped asset: HandleCalculationEnded()
// must NOT call ExecuteCommands(...) or DrainAndApplyDateFormats() inline; it
// must hand off to DeferCalcEndCommands(...). It FAILS against the old inline
// code and PASSES after the fix.
func TestCalcEnd_DeferredRunner_AssetDoesNotMutateCellsInEvent(t *testing.T) {
	t.Parallel()

	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["src/xll_events.cpp"]
	if !ok {
		t.Fatalf("embedded src/xll_events.cpp not found in assets")
	}

	// Isolate the HandleCalculationEnded body so unrelated future code in the TU
	// (e.g. a helper that legitimately references ExecuteCommands) can't mask the
	// invariant.
	const sig = "void HandleCalculationEnded()"
	start := strings.Index(src, sig)
	if start < 0 {
		t.Fatalf("HandleCalculationEnded not found in xll_events.cpp:\n%s", src)
	}
	body := src[start:]

	// The cell-mutating calls must be DEFERRED, never invoked inline in the event.
	if strings.Contains(body, "ExecuteCommands(") {
		t.Errorf("HandleCalculationEnded must NOT call ExecuteCommands inline " +
			"(re-enters Excel during rtd-once teardown -> 0xc0000005); route through " +
			"DeferCalcEndCommands instead")
	}
	if strings.Contains(body, "DrainAndApplyDateFormats()") {
		t.Errorf("HandleCalculationEnded must NOT call DrainAndApplyDateFormats() inline " +
			"(same in-event cell-mutation reentrancy class); it must ride the deferred runner")
	}
	// And it MUST hand the returned response off to the deferral mechanism.
	if !strings.Contains(body, "DeferCalcEndCommands(") {
		t.Errorf("HandleCalculationEnded must route calc-end commands through " +
			"xll::DeferCalcEndCommands so they run OUTSIDE the event callback:\n%s", body)
	}
	// The synchronous round-trip stays (IPC blocking is NOT the crash; it invokes
	// the user's Go handler and produces the commands to defer).
	if !strings.Contains(body, "MSG_CALCULATION_ENDED") {
		t.Errorf("HandleCalculationEnded must keep the synchronous MSG_CALCULATION_ENDED round-trip")
	}
}

// TestCalcEnd_DeferredRunner_GeneratedTemplateRegistersAndExports pins the
// template side: the deferred runner macro is registered (so xlcOnTime can
// resolve it by name) and the exported runner proc exists. This must hold in
// BOTH the user-handler and built-in-handler branches, so it is checked with a
// user CalculationEnded handler present (the showcase configuration).
func TestCalcEnd_DeferredRunner_GeneratedTemplateRegistersAndExports(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "MakeDateGrid", Return: "grid", Args: []config.Arg{}},
		},
		Events: []config.Event{
			{Type: "CalculationEnded", Handler: "OnRecalc"},
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}
	cpp := renderCppMain(t, cfg)

	// The runner macro must be registered as a macro (macroType=2) so xlcOnTime
	// can schedule it by name.
	if !strings.Contains(cpp, "xll::DeferredRunnerMacroName()") {
		t.Errorf("template must register the deferred runner macro via DeferredRunnerMacroName():\n%s", cpp)
	}
	// The exported runner proc must exist with the matching symbol name.
	if !strings.Contains(cpp, "__xllgen_RunDeferredCalcEnd()") {
		t.Errorf("template must export the deferred runner proc __xllgen_RunDeferredCalcEnd():\n%s", cpp)
	}
	// The runner proc body must invoke the runtime drain.
	if !strings.Contains(cpp, "xll::RunDeferredCalcEndCommands()") {
		t.Errorf("exported runner proc must call xll::RunDeferredCalcEndCommands():\n%s", cpp)
	}
}

// TestCalcEnd_DeferredRunner_SchedulerUsesOnTime pins the runtime mechanism:
// the deferral schedules via xlcOnTime (the chosen "run outside the event"
// vehicle), not a synchronous Excel call. If someone later swaps the mechanism
// (e.g. to a message window) this test documents the change point.
func TestCalcEnd_DeferredRunner_SchedulerUsesOnTime(t *testing.T) {
	t.Parallel()

	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["src/xll_deferred_commands.cpp"]
	if !ok {
		t.Fatalf("embedded src/xll_deferred_commands.cpp not found in assets")
	}
	if !strings.Contains(src, "xlcOnTime") {
		t.Errorf("deferred runner scheduler must use xlcOnTime to run the macro outside the event:\n%s", src)
	}
	// Unload self-abort guard (§20.2): a leaked macro firing post-unload must not
	// touch Excel.
	if !strings.Contains(src, "g_isUnloading") || !strings.Contains(src, "g_phost == nullptr") {
		t.Errorf("RunDeferredCalcEndCommands must self-abort on unload (g_isUnloading / g_phost == nullptr):\n%s", src)
	}
}
