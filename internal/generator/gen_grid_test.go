package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

func TestGenGrid(t *testing.T) {
	t.Parallel()
	// Create a temp dir
	tmpDir, err := os.MkdirTemp("", "repro_grid")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a config with grid and numgrid
	cfg := &config.Config{
		Project: config.ProjectConfig{
			Name:    "TestProject",
			Version: "0.1.0",
		},
		Gen: config.GenConfig{
			Go: config.GoConfig{
				Module: "testmod",
			},
		},
		Server: config.ServerConfig{
			Launch: &config.LaunchConfig{Enabled: new(bool)},
		},
		Build: config.BuildConfig{
			Singlefile: "xll",
			TempDir:    "temp_%PROJECT%",
		},
		Functions: []config.Function{
			{
				Name:        "GridFunc",
				Description: "Tests grid",
				Args: []config.Arg{
					{Name: "g", Type: "grid"},
				},
				Return: "string",
			},
			{
				Name:        "NumGridFunc",
				Description: "Tests numgrid",
				Args: []config.Arg{
					{Name: "ng", Type: "numgrid"},
				},
				Return: "float",
			},
		},
	}

	// Set default launch enabled
	*cfg.Server.Launch.Enabled = true

	// Run Generate
	err = Generate(cfg, tmpDir, "testmod", Options{})

	if err != nil {
		t.Fatalf("Generate failed for grid/numgrid type: %v", err)
	}
}

// gridReturnFunctions returns sync + async grid/numgrid-RETURN functions in the
// normalized shape ApplyDefaults produces.
func gridReturnFunctions() []config.Function {
	return []config.Function{
		{Name: "SyncGrid", Return: "grid", Mode: "sync"},
		{Name: "SyncNumGrid", Return: "numgrid", Mode: "sync"},
		{Name: "AsyncGrid", Return: "grid", Mode: "async", Async: true},
		{Name: "AsyncNumGrid", Return: "numgrid", Mode: "async", Async: true},
	}
}

// TestGenGo_GridReturn_Interface verifies the handler-facing return types: a
// grid handler returns [][]any, a numgrid handler returns [][]float64.
func TestGenGo_GridReturn_Interface(t *testing.T) {
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
		Functions: gridReturnFunctions(),
		Version:   "test",
	}

	iface := renderTemplate(t, "interface.go.tmpl", data)
	assertParses(t, "interface.go", iface)

	for _, want := range []string{
		"SyncGrid(ctx context.Context) ([][]any, error)",
		"SyncNumGrid(ctx context.Context) ([][]float64, error)",
		"AsyncGrid(ctx context.Context) ([][]any, error)",
		"AsyncNumGrid(ctx context.Context) ([][]float64, error)",
	} {
		if !strings.Contains(iface, want) {
			t.Errorf("interface.go missing %q:\n%s", want, iface)
		}
	}
}

// TestGenGo_GridReturn_Server verifies the sync path serializes via
// BuildGridFromGo/BuildNumGridFromGo and attaches the offset, and the async
// path validates then queues under the Grid/NumGrid tag.
func TestGenGo_GridReturn_Server(t *testing.T) {
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
		Functions:   gridReturnFunctions(),
		Version:     "test",
		Logging:     config.LoggingConfig{Level: "info", Dir: "logs"},
	}

	srv := renderTemplate(t, "server.go.tmpl", data)
	assertParses(t, "server.go", srv)

	for _, want := range []string{
		// Sync grid: build via wrapper, attach offset.
		"server.BuildGridFromGo(b, res)",
		"ipc.SyncGridResponseAddResult(b, resOffset)",
		"server.BuildNumGridFromGo(b, res)",
		"ipc.SyncNumGridResponseAddResult(b, resOffset)",
		// Async grid: validate then queue under the composite tag.
		"server.ValidateGrid(res)",
		"asyncBatcher.QueueResult(handle, res, protocol.AnyValueGrid, \"\")",
		"server.ValidateNumGrid(res)",
		"asyncBatcher.QueueResult(handle, res, protocol.AnyValueNumGrid, \"\")",
	} {
		if !strings.Contains(srv, want) {
			t.Errorf("server.go missing %q:\n%s", want, srv)
		}
	}
	// The broken form: passing the *protocol.Grid offset path mistakenly to the
	// scalar AddResult (raw res) must NOT appear.
	if strings.Contains(srv, "ipc.SyncGridResponseAddResult(b, res)") {
		t.Errorf("server.go: sync grid must use resOffset, not raw res:\n%s", srv)
	}
}

// TestGenCpp_GridReturn verifies the C++ wrapper: the sync grid live-path uses
// GridToXLOPER12 / NumGridToFP12, the registration type string is Q...$ for
// grid and K%...$ for numgrid, and the async wrappers exist (returning void
// with an async handle, the result routed through AnyToXLOPER12 elsewhere).
func TestGenCpp_GridReturn(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "SyncGrid", Return: "grid", Mode: "sync"},
			{Name: "SyncNumGrid", Return: "numgrid", Mode: "sync"},
			{Name: "AsyncGrid", Return: "grid", Mode: "async", Async: true},
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}
	*cfg.Server.Launch.Enabled = true

	if err := generateCppMain(cfg, tmpDir, false); err != nil {
		t.Fatalf("generateCppMain failed: %v", err)
	}
	contentBytes, err := os.ReadFile(filepath.Join(tmpDir, "xll_main.cpp"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(contentBytes)

	for _, want := range []string{
		"return GridToXLOPER12(resp->result());", // cached + live sync grid
		"return NumGridToFP12(resp->result());",  // sync numgrid
		`std::wstring typeStr = L"Q$"`,           // SyncGrid: Q return, no args
		`std::wstring typeStr = L"K%$"`,          // SyncNumGrid: K% return (FP12)
		`std::wstring typeStr = L">X$"`,          // AsyncGrid: async, no return char
	} {
		if !strings.Contains(content, want) {
			t.Errorf("xll_main.cpp missing %q", want)
		}
	}

	// The async wrapper must return void with an async handle parameter.
	if !strings.Contains(content, "void __stdcall AsyncGrid(LPXLOPER12 asyncHandle)") {
		t.Errorf("xll_main.cpp: AsyncGrid wrapper signature wrong")
	}
}
