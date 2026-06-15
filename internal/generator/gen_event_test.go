package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// serverDataWithEvents builds the template input for server.go.tmpl with the
// given events, mirroring the anonymous struct generateServer passes.
func serverDataWithEvents(events []config.Event) interface{} {
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
		ProjectName: "TestProj",
		Events:      events,
		Version:     "test",
		Logging:     config.LoggingConfig{Level: "info", Dir: "logs"},
	}
}

// TestGenServer_BuiltinEventHandlerName verifies the built-in calculation
// events dispatch to the configured custom handler name.
func TestGenServer_BuiltinEventHandlerName(t *testing.T) {
	t.Parallel()

	srv := renderTemplate(t, "server.go.tmpl", serverDataWithEvents([]config.Event{
		{Type: "CalculationEnded", Handler: "OnRecalc"},
	}))
	assertParses(t, "server.go", srv)

	if !strings.Contains(srv, "sysHandler.HandleCalculationEnded(respBuf, builder, handler.OnRecalc)") {
		t.Errorf("server.go: CalculationEnded must dispatch to the configured handler:\n%s", srv)
	}
}

// TestGenCpp_UserCalculationEndedDrainsSystemWork is the regression for the
// showcase YDH "raw serials instead of dates" bug.
//
// When a project declares a user CalculationEnded event handler (e.g. renamed
// to OnRecalc), the generator emitted ONLY a named-event stub that logged
// "Event CalculationEnded triggered" and did NOT call HandleCalculationEnded().
// The built-in CalculationEnded() macro that DOES call HandleCalculationEnded()
// is suppressed (`{{if hasEvent "CalculationEnded"}}`) whenever a user handler
// exists. Net effect: the date auto-format drain (DrainAndApplyDateFormats),
// the RefCache clear, the rtd-once ClearNonMemoized, AND the MSG_CALCULATION_ENDED
// round-trip that invokes the user's Go handler all silently stop running.
// That is exactly why the minimal smoketest (NO user CalculationEnded event)
// formats dates correctly while the full showcase (HAS one) leaves raw serials.
//
// The fix: the named-event handler, when its type is CalculationEnded, must
// route through HandleCalculationEnded() like the built-in macro does.
func TestGenCpp_UserCalculationEndedDrainsSystemWork(t *testing.T) {
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

	// The user-renamed handler must be the registered CalculationEnded callback.
	if !strings.Contains(cpp, `xll::CallExcel(xlEventRegister, nullptr, L"OnRecalc", xleventCalculationEnded);`) {
		t.Errorf("render must register the user handler OnRecalc as the CalculationEnded callback:\n%s", cpp)
	}
	// And that handler MUST call HandleCalculationEnded() — otherwise the
	// date-format drain (and all other calc-end work) never runs.
	if !strings.Contains(cpp, "HandleCalculationEnded();") {
		t.Errorf("user CalculationEnded handler (OnRecalc) must call HandleCalculationEnded() so the date-format drain runs:\n%s", cpp)
	}
	// Belt-and-suspenders: the named-event stub must NOT be a log-only no-op for
	// the CalculationEnded type. Find the OnRecalc handler body and assert it
	// contains the drain call rather than only the placeholder log.
	const sig = "void __stdcall OnRecalc()"
	start := strings.Index(cpp, sig)
	if start < 0 {
		t.Fatalf("OnRecalc handler not emitted:\n%s", cpp)
	}
	body := cpp[start:]
	if end := strings.Index(body, "\n}"); end >= 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "HandleCalculationEnded();") {
		t.Errorf("OnRecalc body must call HandleCalculationEnded(), got:\n%s", body)
	}
}

// TestGenServer_NonBuiltinEventUsesHandlerField is the regression for the
// template referencing `handler.{{.Name}}` in the non-builtin event dispatch
// block: config.Event has no Name field (only Type/Handler), so rendering a
// config with any event type other than CalculationEnded/CalculationCanceled
// failed with "can't evaluate field Name". The dispatch must use .Handler.
//
// Note: config.Validate rejects unknown event types at config-load time, so
// this path is only reachable if the supported-event whitelist grows — this
// test pins the template/schema consistency for that day.
func TestGenServer_NonBuiltinEventUsesHandlerField(t *testing.T) {
	t.Parallel()

	srv := renderTemplate(t, "server.go.tmpl", serverDataWithEvents([]config.Event{
		{Type: "SheetActivated", Handler: "OnSheetActivated"},
	}))
	assertParses(t, "server.go", srv)

	if !strings.Contains(srv, "handler.OnSheetActivated(ctx)") {
		t.Errorf("server.go: non-builtin event must dispatch to .Handler:\n%s", srv)
	}
}
