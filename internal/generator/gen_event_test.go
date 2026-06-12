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
