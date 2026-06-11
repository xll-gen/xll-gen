package generator

import (
	"bytes"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"text/template"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/templates"
)

// renderTemplate renders a named template with the given data, mirroring
// executeTemplate but returning the rendered string instead of writing a file.
func renderTemplate(t *testing.T, name string, data interface{}) string {
	t.Helper()

	content, err := templates.Get(name)
	if err != nil {
		t.Fatalf("templates.Get(%s): %v", name, err)
	}

	tmpl, err := template.New(name).Funcs(GetCommonFuncMap()).Parse(content)
	if err != nil {
		t.Fatalf("parse template %s: %v", name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("execute template %s: %v", name, err)
	}
	return buf.String()
}

// assertParses ensures the rendered Go source is syntactically valid.
func assertParses(t *testing.T, name, src string) {
	t.Helper()
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, name, src, parser.AllErrors); err != nil {
		t.Fatalf("rendered %s does not parse: %v\n---\n%s", name, err, src)
	}
	// gofmt is applied to generated files in the real pipeline; ensure the
	// rendered source survives a format pass without error.
	if _, err := format.Source([]byte(src)); err != nil {
		t.Fatalf("rendered %s is not gofmt-able: %v\n---\n%s", name, err, src)
	}
}

// interfaceData mirrors the anonymous struct built in generateInterface.
func interfaceData(cmds []config.Command) interface{} {
	return struct {
		Package   string
		ModName   string
		Functions []config.Function
		Events    []config.Event
		Commands  []config.Command
		Version   string
		Rtd       config.RtdConfig
	}{
		Package: "generated",
		ModName: "testmod",
		Functions: []config.Function{
			{Name: "Sum", Return: "int", Args: []config.Arg{{Name: "a", Type: "int"}}},
		},
		Commands: cmds,
		Version:  "test",
	}
}

// serverData mirrors the anonymous struct built in generateServer.
func serverData(cmds []config.Command) interface{} {
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
		Functions: []config.Function{
			{Name: "Sum", Return: "int", Args: []config.Arg{{Name: "a", Type: "int"}}},
		},
		Commands: cmds,
		Version:  "test",
		Logging:  config.LoggingConfig{Level: "info", Dir: "logs"},
	}
}

func TestGenCommands_Present(t *testing.T) {
	t.Parallel()

	cmds := []config.Command{
		{Name: "RunReport", Description: "Runs the report", Handler: "RunReport"},
	}

	iface := renderTemplate(t, "interface.go.tmpl", interfaceData(cmds))
	assertParses(t, "interface.go", iface)

	if !strings.Contains(iface, "RunReport(ctx context.Context, cmd server.CommandContext) error") {
		t.Errorf("interface.go missing command method:\n%s", iface)
	}
	if !strings.Contains(iface, `"github.com/xll-gen/xll-gen/pkg/server"`) {
		t.Errorf("interface.go missing pkg/server import:\n%s", iface)
	}

	srv := renderTemplate(t, "server.go.tmpl", serverData(cmds))
	assertParses(t, "server.go", srv)

	if !strings.Contains(srv, "case server.MsgCommandInvoke:") {
		t.Errorf("server.go missing MsgCommandInvoke case:\n%s", srv)
	}
	if !strings.Contains(srv, "sysHandler.HandleCommandInvoke(") {
		t.Errorf("server.go missing HandleCommandInvoke call:\n%s", srv)
	}
	if !strings.Contains(srv, `case "RunReport":`) {
		t.Errorf("server.go missing command name case:\n%s", srv)
	}
	if !strings.Contains(srv, "return handler.RunReport, true") {
		t.Errorf("server.go missing handler resolve:\n%s", srv)
	}
}

func TestGenCommands_Absent(t *testing.T) {
	t.Parallel()

	iface := renderTemplate(t, "interface.go.tmpl", interfaceData(nil))
	assertParses(t, "interface.go", iface)

	if strings.Contains(iface, "server.CommandContext") {
		t.Errorf("command-less interface.go must not reference server.CommandContext:\n%s", iface)
	}
	if strings.Contains(iface, `"github.com/xll-gen/xll-gen/pkg/server"`) {
		t.Errorf("command-less interface.go must not import pkg/server:\n%s", iface)
	}

	srv := renderTemplate(t, "server.go.tmpl", serverData(nil))
	assertParses(t, "server.go", srv)

	if strings.Contains(srv, "MsgCommandInvoke") {
		t.Errorf("command-less server.go must not reference MsgCommandInvoke:\n%s", srv)
	}
}
