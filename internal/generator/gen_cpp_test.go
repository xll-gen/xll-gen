package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/pkg/server"
)

// TestGenCpp_ComplexReturnTypes exercises the C++ template's rendering of
// composite/any RETURN types in isolation. Note: config.Validate now rejects
// range/grid/numgrid/any as return types end-to-end (the Go server cannot
// serialize composite returns — see internal/config), so these functions could
// never reach codegen via the real pipeline. This test calls generateCppMain
// directly, bypassing Validate, to keep coverage of the C++ converter selection
// (AnyToXLOPER12 etc.) at the template level should composite returns ever be
// supported.
func TestGenCpp_ComplexReturnTypes(t *testing.T) {
	t.Parallel()
	// Setup temp dir
	tmpDir, err := os.MkdirTemp("", "bug_repro")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config with functions returning complex types
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
            {
				Name:   "TestAny",
				Return: "any",
				Args:   []config.Arg{},
			},
            {
				Name:   "TestGrid",
				Return: "grid",
				Args:   []config.Arg{},
			},
            {
				Name:   "TestNumGrid",
				Return: "numgrid",
				Args:   []config.Arg{},
			},
            {
				Name:   "TestRange",
				Return: "range",
				Args:   []config.Arg{},
			},
		},
        Server: config.ServerConfig{
            Timeout: "2s",
            Launch: &config.LaunchConfig{Enabled: new(bool)}, // Default false is fine, just needs to be non-nil
        },
	}
    *cfg.Server.Launch.Enabled = true

	// Generate xll_main.cpp
	if err := generateCppMain(cfg, tmpDir, false); err != nil {
		t.Fatalf("generateCppMain failed: %v", err)
	}

	// Read generated file
	contentBytes, err := os.ReadFile(filepath.Join(tmpDir, "xll_main.cpp"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(contentBytes)

	// Verify converters are used
    checks := []struct {
        name string
        want string
    }{
        {"TestAny", "return AnyToXLOPER12(resp->result());"},
        {"TestGrid", "return GridToXLOPER12(resp->result());"},
        {"TestNumGrid", "return NumGridToFP12(resp->result());"},
        {"TestRange", "return RangeToXLOPER12(resp->result());"},
    }

    for _, c := range checks {
        if !strings.Contains(content, c.want) {
            t.Errorf("Function %s: expected '%s', not found", c.name, c.want)
        }
    }
}

// renderCppMain runs generateCppMain into a temp dir and returns the rendered
// xll_main.cpp source. Mirrors the helper-free style of the other gen_cpp tests
// (exercises the real template + funcmap path, not a hand-built data struct).
func renderCppMain(t *testing.T, cfg *config.Config) string {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "gencpp_cmd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	if err := generateCppMain(cfg, tmpDir, false); err != nil {
		t.Fatalf("generateCppMain failed: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(tmpDir, "xll_main.cpp"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestGenCpp_Commands verifies macroType=2 command procs + registration:
// exported Cmd_<Name> procs, literal 2 macroType for commands vs 1 for the
// function, shortcut emission, SetCommands ordering, and that the function
// registration loop is untouched.
func TestGenCpp_Commands(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "Sum", Return: "int", Args: []config.Arg{{Name: "a", Type: "int"}}},
		},
		Commands: []config.Command{
			{Name: "RunReport", Description: "Runs the report", Shortcut: "R", Handler: "RunReport"},
			{Name: "Refresh", Description: "Refreshes data", Handler: "Refresh"},
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}

	content := renderCppMain(t, cfg)

	// 1. Exported command procs.
	if !strings.Contains(content, `extern "C" __declspec(dllexport) int __stdcall Cmd_RunReport() {`) {
		t.Errorf("missing exported Cmd_RunReport proc:\n%s", content)
	}
	if !strings.Contains(content, `extern "C" __declspec(dllexport) int __stdcall Cmd_Refresh() {`) {
		t.Errorf("missing exported Cmd_Refresh proc")
	}
	if !strings.Contains(content, `xll::ribbon::SendCommandInvoke("RunReport", "");`) {
		t.Errorf("Cmd_RunReport does not dispatch SendCommandInvoke")
	}

	// 2. Registration uses literal 2 for commands.
	if !strings.Contains(content, `L"Cmd_RunReport", // Procedure (exported symbol)`) {
		t.Errorf("missing command registration for RunReport")
	}
	if !strings.Contains(content, `2, // MacroType 2 = command`) {
		t.Errorf("command registration does not use literal macroType 2")
	}
	// Shortcut letter emitted for the command that declares one.
	if !strings.Contains(content, `L"R", // Shortcut letter -> Ctrl+Shift+<letter>`) {
		t.Errorf("shortcut letter R not emitted for RunReport")
	}

	// 3. Function loop unchanged: still macroType 1.
	if !strings.Contains(content, "1, // MacroType") {
		t.Errorf("function registration loop changed: missing literal macroType 1")
	}
	if !strings.Contains(content, `L"Sum", // Procedure`) {
		t.Errorf("function registration for Sum missing/changed")
	}

	// SetCommands must precede the registration of any command proc so a
	// shortcut-fired proc resolves its name.
	setIdx := strings.Index(content, "xll::ribbon::SetCommands(")
	regIdx := strings.Index(content, `L"Cmd_RunReport", // Procedure`)
	if setIdx < 0 {
		t.Fatalf("SetCommands call missing")
	}
	if regIdx < 0 || setIdx > regIdx {
		t.Errorf("SetCommands (idx %d) must come before command registration (idx %d)", setIdx, regIdx)
	}
	// SetCommands must also precede the function registration loop (set-before
	// any proc-resolving xlfRegister; no dependency on the function loop).
	funcRegIdx := strings.Index(content, `L"Sum", // Procedure`)
	if funcRegIdx >= 0 && setIdx > funcRegIdx {
		t.Errorf("SetCommands (idx %d) must precede the function registration loop (idx %d)", setIdx, funcRegIdx)
	}
}

// TestGenCpp_NoCommands is the zero-command regression: no Cmd_ procs, no
// command registration, no SetCommands; function registration intact.
func TestGenCpp_NoCommands(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "Sum", Return: "int", Args: []config.Arg{{Name: "a", Type: "int"}}},
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}

	content := renderCppMain(t, cfg)

	if strings.Contains(content, "Cmd_") {
		t.Errorf("command-less render must not emit any Cmd_ symbol")
	}
	if strings.Contains(content, "SendCommandInvoke") {
		t.Errorf("command-less render must not reference SendCommandInvoke")
	}
	if strings.Contains(content, "SetCommands") {
		t.Errorf("command-less render must not call SetCommands")
	}
	if strings.Contains(content, "MacroType 2 = command") {
		t.Errorf("command-less render must not emit command registration")
	}
	// Function registration still present.
	if !strings.Contains(content, `L"Sum", // Procedure`) {
		t.Errorf("function registration missing in command-less render")
	}
}

// TestGenCpp_ArgMarshalling pins the request-building marshalling call shape for
// composite ARG types and the caller-aware (caller:true) path. Regression guard
// for the codegen bug where the template emitted undeclared GridToFlatBuffer /
// NumGridToFlatBuffer / RangeToFlatBuffer with swapped (builder, op) args (real
// API is ConvertGrid/ConvertNumGrid/ConvertRange(op, builder)), and the caller
// path passed ScopedXLOPER12* (&xCaller/&xFormat) plus operator-> that
// ScopedXLOPER12 lacks. Both made the generated xll_main.cpp fail to compile.
func TestGenCpp_ArgMarshalling(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "SumGrid", Return: "float", Args: []config.Arg{{Name: "g", Type: "grid"}}},
			{Name: "SumNumGrid", Return: "float", Args: []config.Arg{{Name: "ng", Type: "numgrid"}}},
			{Name: "RangeAddr", Return: "string", Args: []config.Arg{{Name: "r", Type: "range"}}},
			{Name: "EchoAny", Return: "any", Args: []config.Arg{{Name: "v", Type: "any"}}},
			{Name: "WhoAmI", Return: "string", Args: []config.Arg{}, Caller: true},
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}

	content := renderCppMain(t, cfg)

	// Composite arg converters: correct names + (op, builder) order.
	for _, want := range []string{
		"ConvertGrid(g, builder)",
		"ConvertNumGrid(ng, builder)",
		"ConvertRange(r, builder)",
		"ConvertAny(v, builder)",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("expected marshalling call %q, not found", want)
		}
	}

	// Must NOT regress to the old undeclared helper names.
	for _, bad := range []string{"GridToFlatBuffer", "NumGridToFlatBuffer", "RangeToFlatBuffer", "AnyToFlatBuffer(builder"} {
		if strings.Contains(content, bad) {
			t.Errorf("found stale undeclared converter %q; should use Convert* API", bad)
		}
	}

	// Caller path: ScopedXLOPER12 has no operator-> and CallExcel/ConvertRange
	// want LPXLOPER12, not ScopedXLOPER12*. Pass the scoped object by ref / via
	// .get(); never take its address, never use operator->.
	if strings.Contains(content, "&xCaller") {
		t.Errorf("caller path passes ScopedXLOPER12* (&xCaller); must pass the scoped object directly or via .get()")
	}
	if strings.Contains(content, "&xFormat") {
		t.Errorf("caller path passes ScopedXLOPER12* (&xFormat); must pass the scoped object directly")
	}
	if !strings.Contains(content, "xFormat.get()->") {
		t.Errorf("caller path must dereference via xFormat.get()-> (ScopedXLOPER12 has no operator->)")
	}
	if !strings.Contains(content, "ConvertRange(xCaller.get(), builder, callerFormat)") {
		t.Errorf("caller path must call ConvertRange(xCaller.get(), builder, callerFormat)")
	}
}

func TestGenCpp_StringErrorReturn(t *testing.T) {
	t.Parallel()
	// Setup temp dir
	tmpDir, err := os.MkdirTemp("", "bug_repro")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config with a string return function
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{
				Name:   "TestStr",
				Return: "string",
				Args:   []config.Arg{},
			},
            {
				Name:   "TestInt",
				Return: "int",
				Args:   []config.Arg{},
			},
		},
        Server: config.ServerConfig{
            Timeout: "2s",
            Launch: &config.LaunchConfig{Enabled: new(bool)},
        },
	}
    *cfg.Server.Launch.Enabled = true

	// Generate xll_main.cpp
	if err := generateCppMain(cfg, tmpDir, false); err != nil {
		t.Fatalf("generateCppMain failed: %v", err)
	}

	// Read generated file
	contentBytes, err := os.ReadFile(filepath.Join(tmpDir, "xll_main.cpp"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(contentBytes)

    // Verify TestStr / TestInt slot.Send call site uses MsgUserStart + index.
    // Deriving from server.MsgUserStart avoids the silent rot the prior
    // hardcoded 133 introduced when MsgUserStart bumped to 140.
    strID := fmt.Sprintf("slot.Send(-((int)builder.GetSize()), (shm::MsgType)%d", server.MsgUserStart+0)
    intID := fmt.Sprintf("slot.Send(-((int)builder.GetSize()), (shm::MsgType)%d", server.MsgUserStart+1)
    if !strings.Contains(content, strID) {
         t.Fatalf("Could not find expected slot.Send call for TestStr (MsgId %d)", server.MsgUserStart+0)
    }
    if !strings.Contains(content, intID) {
         t.Fatalf("Could not find expected slot.Send call for TestInt (MsgId %d)", server.MsgUserStart+1)
    }

    // Expect: if (res.HasError())
    if !strings.Contains(content, "if (res.HasError())") {
         t.Fatal("Could not find expected HasError check")
    }

    // Check that HasError is used at least twice (once for each function)
    if strings.Count(content, "if (res.HasError())") < 2 {
         t.Fatal("Expected at least 2 occurrences of 'if (res.HasError())'")
    }

    // Check for negative size calculation
    if !strings.Contains(content, "-((int)builder.GetSize())") {
        t.Fatal("Expected negative size calculation for zero-copy")
    }

    // Ensure memmove is GONE
    if strings.Contains(content, "std::memmove(slot.GetReqBuffer(), builder.GetBufferPointer(), builder.GetSize());") {
        t.Fatal("Expected NO memmove for zero-copy optimization")
    }
}
