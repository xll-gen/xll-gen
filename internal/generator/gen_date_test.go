package generator

import (
	"strings"
	"testing"

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
