package generator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// ---------------------------------------------------------------------------
// Static "imported and not used" gate for the generated Go tree.
//
// WHY THIS EXISTS
//
// v0.8.53 shipped a project shape that does not compile: a project whose
// functions are ALL rtd / rtd-once emitted
//
//	generated/server.go:11:2: "runtime/debug" imported and not used
//
// because server.go.tmpl gated the "runtime/debug" import on {{if .Functions}}
// ("the project has any function at all") while debug.Stack() is only emitted
// inside the sync and async handler bodies, which are themselves gated on
// {{if not (isRtdLike .Mode)}}. Every golden/parse test used a shape that
// contains at least one sync function, so nothing ever rendered the failing
// combination.
//
// WHAT THIS GATE CAN CATCH
//
// It renders each Go template over a matrix of project shapes and, for each
// rendered file, reports any import whose package identifier never appears as
// the X of a selector expression (pkg.Sym) anywhere in the file. That is
// exactly the compiler's "imported and not used" rule for the common case, and
// it needs no module resolution, so it is cheap enough for the fast suite.
//
// WHAT IT CANNOT CATCH
//
//   - Any other compile error: unknown identifiers, type mismatches, wrong
//     argument counts, missing methods. Only a real build catches those, and
//     the C++/full-build gate is a separate (serialized) phase.
//   - Unused local variables / unused non-import declarations.
//   - Dot-imports and imports used only through non-selector syntax (neither
//     appears in these templates).
//
// It deliberately does NOT run go/types: type-checking generated/server.go
// requires the whole dependency graph (shm, types, flatbuffers, pkg/*), which
// means a module download and a build cache — too heavy, and it would duplicate
// what the compile phase already does.
// ---------------------------------------------------------------------------

// importShape names one project shape in the matrix.
type importShape struct {
	name string
	cfg  *config.Config
}

func fnSync(name string) config.Function {
	return config.Function{Name: name, Mode: "sync", Return: "float",
		Args: []config.Arg{{Name: "a", Type: "int"}}}
}

func fnAsync(name string) config.Function {
	return config.Function{Name: name, Mode: "async", Async: true, Return: "float",
		Args: []config.Arg{{Name: "a", Type: "int"}}}
}

func fnRtd(name string) config.Function {
	return config.Function{Name: name, Mode: "rtd", Return: "float",
		Args: []config.Arg{{Name: "a", Type: "int"}}}
}

func fnRtdOnce(name string) config.Function {
	return config.Function{Name: name, Mode: "rtd-once", Return: "float",
		Args: []config.Arg{{Name: "a", Type: "int"}}}
}

func importCfg(fns []config.Function, cmds []config.Command, evs []config.Event, rtdOn bool) *config.Config {
	return &config.Config{
		Project:   config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: fns,
		Commands:  cmds,
		Events:    evs,
		Rtd: config.RtdConfig{
			Enabled:     rtdOn,
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

// importShapes is the project-shape matrix: pure-mode projects (the shapes the
// existing golden tests never exercise), mixed, and the with/without
// commands+events axes.
func importShapes() []importShape {
	cmd := []config.Command{{Name: "DoThing", Handler: "OnDoThing"}}
	ev := []config.Event{{Type: "CalculationEnded", Handler: "OnCalculationEnded"}}

	return []importShape{
		{"all-sync", importCfg([]config.Function{fnSync("A"), fnSync("B")}, nil, nil, false)},
		{"all-async", importCfg([]config.Function{fnAsync("A"), fnAsync("B")}, nil, nil, false)},
		{"all-rtd", importCfg([]config.Function{fnRtd("A"), fnRtd("B")}, nil, nil, true)},
		{"all-rtd-once", importCfg([]config.Function{fnRtdOnce("A"), fnRtdOnce("B")}, nil, nil, true)},
		{"all-rtd-like-mixed", importCfg([]config.Function{fnRtd("A"), fnRtdOnce("B")}, nil, nil, true)},
		{"rtd-only-with-commands", importCfg([]config.Function{fnRtd("A")}, cmd, nil, true)},
		{"rtd-only-with-events", importCfg([]config.Function{fnRtdOnce("A")}, nil, ev, true)},
		{"rtd-only-with-commands-and-events", importCfg([]config.Function{fnRtd("A")}, cmd, ev, true)},
		{"mixed-sync-rtd", importCfg([]config.Function{fnSync("A"), fnRtd("B")}, nil, nil, true)},
		{"mixed-async-rtd-once", importCfg([]config.Function{fnAsync("A"), fnRtdOnce("B")}, nil, nil, true)},
		{"no-functions", importCfg(nil, nil, nil, false)},
		{"no-functions-with-commands", importCfg(nil, cmd, ev, false)},
		{"events-only", importCfg(nil, nil, ev, false)},
		{"commands-only-rtd-enabled", importCfg(nil, cmd, nil, true)},
		{"sync-with-timeout", importCfg([]config.Function{withTimeout(fnSync("A"), "3s")}, nil, nil, false)},
		{"sync-with-date", importCfg([]config.Function{withDateArg(fnSync("A"))}, nil, nil, false)},
		{"rtd-only-with-date", importCfg([]config.Function{withDateArg(fnRtd("A"))}, nil, nil, true)},
		{"rtd-once-grid", importCfg([]config.Function{gridReturn(fnRtdOnce("A"))}, nil, nil, true)},
		{"rtd-only-caller", importCfg([]config.Function{withCaller(fnRtd("A"))}, nil, nil, true)},
	}
}

func withTimeout(f config.Function, d string) config.Function {
	f.Timeout = d
	return f
}

func withDateArg(f config.Function) config.Function {
	f.Args = append(append([]config.Arg{}, f.Args...), config.Arg{Name: "d", Type: "date"})
	return f
}

func withCaller(f config.Function) config.Function {
	f.Caller = true
	return f
}

func gridReturn(f config.Function) config.Function {
	f.Return = "numgrid"
	return f
}

// assertNoUnusedImports parses src and fails if any imported package
// identifier is never used as the qualifier of a selector expression.
func assertNoUnusedImports(t *testing.T, label, src string) {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, label, src, parser.AllErrors)
	if err != nil {
		t.Fatalf("%s: rendered source does not parse: %v", label, err)
	}

	// Collect every identifier used as the qualifier of a selector.
	used := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok {
			used[id.Name] = true
		}
		return true
	})

	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatalf("%s: bad import path %s", label, imp.Path.Value)
		}
		name := importLocalName(imp, path)
		switch name {
		case "_", ".":
			continue // blank/dot imports are never "unused"
		}
		if !used[name] {
			t.Errorf("%s: %q imported and not used (local name %q) -- this shape does not compile",
				label, path, name)
		}
	}
}

// pkgNameOverrides maps import paths whose package name differs from the last
// path segment. Resolving these properly would need the module graph; the set
// is tiny and closed (the templates' import list is fixed), so an explicit
// table keeps the gate dependency-free. A new import whose package name does
// not match its last segment must be added here or the gate will report a false
// "imported and not used".
var pkgNameOverrides = map[string]string{
	"github.com/xll-gen/shm/go": "shm", // package shm, directory "go"
}

// importLocalName returns the identifier an import binds in file scope: the
// explicit alias when present, an override when the package name differs from
// the directory, otherwise the last path segment.
func importLocalName(imp *ast.ImportSpec, path string) string {
	if imp.Name != nil {
		return imp.Name.Name
	}
	if n, ok := pkgNameOverrides[path]; ok {
		return n
	}
	last := path
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			last = path[i+1:]
			break
		}
	}
	return last
}

// TestGeneratedGoHasNoUnusedImports renders the Go templates across the
// project-shape matrix and asserts none of them emits an import the file never
// uses. The "all-rtd" / "all-rtd-once" rows are the v0.8.53 regression:
// "runtime/debug" imported and not used in generated/server.go.
func TestGeneratedGoHasNoUnusedImports(t *testing.T) {
	for _, shape := range importShapes() {
		shape := shape
		t.Run(shape.name, func(t *testing.T) {
			srv := renderTemplate(t, "server.go.tmpl", serverDataForFull(shape.cfg))
			assertNoUnusedImports(t, shape.name+"/server.go", srv)

			iface := renderTemplate(t, "interface.go.tmpl", interfaceDataForFull(shape.cfg))
			assertNoUnusedImports(t, shape.name+"/interface.go", iface)
		})
	}
}

// serverDataForFull mirrors the anonymous struct built in generateServer,
// including the Events/Commands/ServerTimeout fields serverDataFor drops.
func serverDataForFull(cfg *config.Config) any {
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
		Package:       "generated",
		ModName:       "testmod",
		ProjectName:   cfg.Project.Name,
		Functions:     cfg.Functions,
		Events:        cfg.Events,
		Commands:      cfg.Commands,
		ServerTimeout: cfg.Server.Timeout,
		Version:       "test",
		Logging:       config.LoggingConfig{Level: "info", Dir: "logs"},
		Rtd:           cfg.Rtd,
		Chunk:         cfg.Server.Chunk,
	}
}

// interfaceDataForFull mirrors the anonymous struct built in generateInterface.
func interfaceDataForFull(cfg *config.Config) any {
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
		Events:    cfg.Events,
		Commands:  cfg.Commands,
		Version:   "test",
		Rtd:       cfg.Rtd,
	}
}
