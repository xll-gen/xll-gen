package generator

import (
	"fmt"
	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/pkg/server"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestPNG writes a 16x16 PNG (with partial alpha) and returns its path.
// Duplicated locally from internal/ribbon/ribbon_test.go (no shared test util).
func writeTestPNG(t *testing.T, dir, name string) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.NRGBA{R: 220, G: 60, B: 40, A: uint8(255 - y*12)})
		}
	}
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGenerateRibbonImagesHeader(t *testing.T) {
	baseDir := t.TempDir()
	writeTestPNG(t, baseDir, "icon.png")
	includeDir := filepath.Join(baseDir, "out")
	if err := os.MkdirAll(includeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Project:  config.ProjectConfig{Name: "p"},
		Commands: []config.Command{{Name: "c1", Handler: "c1"}},
		Ribbon: config.RibbonConfig{Tab: "T", Groups: []config.RibbonGroup{{
			Label: "G", Buttons: []config.RibbonButton{{Label: "B", Command: "c1", Image: "icon.png"}},
		}}},
	}
	if err := generateRibbonHeaders(cfg, includeDir, baseDir); err != nil {
		t.Fatal(err)
	}
	imagesH, err := os.ReadFile(filepath.Join(includeDir, "ribbon_images.h"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"kXllRibbonImg0[]",
		`L"xllgen_img_0"`,
		"GetXllRibbonImages",
		"0x89,", // PNG signature first byte must be embedded
		`#include "com/ribbon_image.h"`,
	} {
		if !strings.Contains(string(imagesH), want) {
			t.Errorf("ribbon_images.h missing %q", want)
		}
	}
	xmlH, err := os.ReadFile(filepath.Join(includeDir, "ribbon_xml.h"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(xmlH), `image="xllgen_img_0"`) {
		t.Error("ribbon_xml.h must reference the embedded image name")
	}
}

func TestGenerateRibbonImagesHeaderEmptyWhenNoFileImages(t *testing.T) {
	baseDir := t.TempDir()
	includeDir := filepath.Join(baseDir, "out")
	if err := os.MkdirAll(includeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Project:  config.ProjectConfig{Name: "p"},
		Commands: []config.Command{{Name: "c1", Handler: "c1"}},
		Ribbon: config.RibbonConfig{Tab: "T", Groups: []config.RibbonGroup{{
			Label: "G", Buttons: []config.RibbonButton{{Label: "B", Command: "c1", Image: "HappyFace"}},
		}}},
	}
	if err := generateRibbonHeaders(cfg, includeDir, baseDir); err != nil {
		t.Fatal(err)
	}
	imagesH, err := os.ReadFile(filepath.Join(includeDir, "ribbon_images.h"))
	if err != nil {
		t.Fatal(err) // header must exist even with zero images (unconditional include)
	}
	if !strings.Contains(string(imagesH), "GetXllRibbonImages") {
		t.Error("empty ribbon_images.h must still define GetXllRibbonImages")
	}
}

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
			Launch:  &config.LaunchConfig{Enabled: new(bool)}, // Default false is fine, just needs to be non-nil
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
// composite ARG types and the caller+macro path. Regression guard for the
// codegen bug where the template emitted undeclared GridToFlatBuffer /
// NumGridToFlatBuffer / RangeToFlatBuffer with swapped (builder, op) args (real
// API is ConvertGrid/ConvertNumGrid/ConvertRange(op, builder)), and the caller
// path passed ScopedXLOPER12* (&xCaller/&xFormat) plus operator-> that
// ScopedXLOPER12 lacks. Both made the generated xll_main.cpp fail to compile.
// WhoAmI is caller+macro here so the xlfGetCell number-format fetch is emitted
// (caller:true alone no longer fetches the format — see the split below).
func TestGenCpp_ArgMarshalling(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "SumGrid", Return: "float", Args: []config.Arg{{Name: "g", Type: "grid"}}},
			{Name: "SumNumGrid", Return: "float", Args: []config.Arg{{Name: "ng", Type: "numgrid"}}},
			{Name: "RangeAddr", Return: "string", Args: []config.Arg{{Name: "r", Type: "range"}}},
			{Name: "EchoAny", Return: "any", Args: []config.Arg{{Name: "v", Type: "any"}}},
			{Name: "WhoAmI", Return: "string", Args: []config.Arg{}, Caller: true, Macro: true},
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}

	content := renderCppMain(t, cfg)

	// Composite arg converters: correct names + (op, builder) order.
	// NOTE: a `grid` arg is registered `U`, so Excel passes a REFERENCE for
	// range input (A1:B2). The sync/async path MUST coerce it to values via
	// xll::ConvertGridArg (plain ConvertGrid only handles xltypeMulti and would
	// yield a degenerate 1x1 Nil grid → SumGrid==0). numgrid/range/any are
	// unaffected (range/any ship coordinates by design; FP12 numgrid is not a
	// reference type at the wrapper boundary).
	for _, want := range []string{
		"xll::ConvertGridArg(g, builder, &gridCoerceOk0)",
		"ConvertNumGrid(ng, builder)",
		"ConvertRange(r, builder)",
		"ConvertAny(v, builder)",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("expected marshalling call %q, not found", want)
		}
	}

	// The sync `grid` arg path MUST NOT regress to plain ConvertGrid-on-ref
	// (the empty-grid bug). Assert the coercing converter is the one wired in
	// for the grid arg and that a coerce-failure guard is emitted.
	if strings.Contains(content, "ConvertGrid(g, builder)") {
		t.Errorf("sync grid arg regressed to plain ConvertGrid(g, builder); a `U`-registered ref yields an empty 1x1 grid — must use xll::ConvertGridArg")
	}
	if !strings.Contains(content, "if (!gridCoerceOk0)") {
		t.Errorf("sync grid arg must emit a coerce-failure guard (if (!gridCoerceOk0)) returning the error sentinel, not a silent degenerate grid")
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
		t.Errorf("caller+macro path must dereference via xFormat.get()-> (ScopedXLOPER12 has no operator->)")
	}
	if !strings.Contains(content, "ConvertRange(xCaller.get(), builder, callerFormat)") {
		t.Errorf("caller path must call ConvertRange(xCaller.get(), builder, callerFormat)")
	}
	// xlfGetCell returns an Excel-allocated xltypeStr; only the Result wrapper
	// releases it (xlFree in its destructor, types >= v0.2.9). Plain
	// ScopedXLOPER12 leaked the format string on every macro caller-aware call.
	if !strings.Contains(content, "ScopedXLOPER12Result xFormat;") {
		t.Errorf("caller+macro path must hold the xlfGetCell result in ScopedXLOPER12Result")
	}
}

// TestGenCpp_AsyncGridArgCoerces pins that an ASYNC function with a `grid` arg
// marshals through the ref-coercing xll::ConvertGridArg (same bug as the sync
// path: a `U`-registered grid arg arrives as a reference for range input, and
// plain ConvertGrid yields a degenerate 1x1 Nil grid). The async coerce-failure
// branch must signal Excel via xlAsyncReturn (an error XLOPER) and return —
// NOT return &g_xlErrValue (the async wrapper returns void).
func TestGenCpp_AsyncGridArgCoerces(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "AsyncSumGrid", Return: "float", Mode: "async", Args: []config.Arg{{Name: "g", Type: "grid"}}},
		},
		Server: config.ServerConfig{
			Timeout:         "2s",
			AsyncAckTimeout: "2s",
			Launch:          &config.LaunchConfig{Enabled: new(bool)},
		},
	}

	content := renderCppMain(t, cfg)

	if !strings.Contains(content, "xll::ConvertGridArg(g, builder, &gridCoerceOk0)") {
		t.Errorf("async grid arg must marshal through xll::ConvertGridArg (ref-coercing), not plain ConvertGrid")
	}
	if strings.Contains(content, "ConvertGrid(g, builder)") {
		t.Errorf("async grid arg regressed to plain ConvertGrid(g, builder) — empty-grid bug")
	}
	// Async coerce-failure path: signal via xlAsyncReturn, then return (void).
	idx := strings.Index(content, "if (!gridCoerceOk0)")
	if idx < 0 {
		t.Fatalf("async grid arg missing coerce-failure guard (if (!gridCoerceOk0))")
	}
	// Examine the guard body window.
	window := content[idx:min(len(content), idx+900)]
	if !strings.Contains(window, "xlAsyncReturn") {
		t.Errorf("async grid coerce-failure must report the error via xlAsyncReturn")
	}
	if strings.Contains(window, "&g_xlErrValue") {
		t.Errorf("async grid coerce-failure must NOT return &g_xlErrValue (async wrapper returns void)")
	}
}

// TestGenCpp_CallerMacroSplit pins the v0.5.0 caller/macro registration split:
//   - caller:true alone is POSITION-ONLY and thread-safe: type string carries
//     '$' and no '#', the wrapper emits xlfCaller + ConvertRange with an empty
//     format, and it does NOT emit the macro-only xlfGetCell fetch.
//   - macro:true alone registers the function as a macro-sheet equivalent: type
//     string carries '#' and no '$'. Without caller:true there is no xlfCaller
//     block.
//   - caller+macro is the legacy full behavior: '#', no '$', and the wrapper
//     fetches the caller number format via xlfGetCell(7).
func TestGenCpp_CallerMacroSplit(t *testing.T) {
	t.Parallel()

	mk := func(fn config.Function) string {
		cfg := &config.Config{
			Project:   config.ProjectConfig{Name: "TestProj", Version: "0.1"},
			Functions: []config.Function{fn},
			Server: config.ServerConfig{
				Timeout: "2s",
				Launch:  &config.LaunchConfig{Enabled: new(bool)},
			},
		}
		return renderCppMain(t, cfg)
	}

	// regBlock returns the registration { ... } block for the named function so
	// type-string assertions are not confused by other functions' blocks.
	regBlock := func(content, name string) string {
		// Match the comment line head only (template line endings may be CRLF).
		marker := "// Register " + name
		i := strings.Index(content, marker)
		if i < 0 {
			t.Fatalf("registration block for %q not found", name)
		}
		rest := content[i+len(marker):]
		// The next function's registration block (or the commands section)
		// starts at the next "// Register " marker; bound the slice there.
		if j := strings.Index(rest, "// Register "); j >= 0 {
			return rest[:j]
		}
		return rest
	}

	// The registration-block comment mentions xlfCaller/xlfGetCell by name to
	// explain the split, so assert on the actual emitted CALLS
	// (CallExcel(xlf..., ...)) rather than the bare tokens.
	t.Run("caller only: thread-safe, position-only, no xlfGetCell", func(t *testing.T) {
		content := mk(config.Function{Name: "WhoAmI", Return: "string", Caller: true})
		blk := regBlock(content, "WhoAmI")
		// Type string: Q (string return) + '$', no '#'.
		if !strings.Contains(blk, `std::wstring typeStr = L"Q$"`) {
			t.Errorf("caller-only type string must be Q$ (thread-safe, no '#'); block:\n%s", blk)
		}
		// Wrapper keeps the xlfCaller call + empty-format ConvertRange.
		if !strings.Contains(content, "CallExcel(xlfCaller") {
			t.Errorf("caller-only must still call xlfCaller")
		}
		if !strings.Contains(content, "ConvertRange(xCaller.get(), builder, callerFormat)") {
			t.Errorf("caller-only must call ConvertRange(xCaller.get(), builder, callerFormat)")
		}
		// callerFormat is declared and stays "" — no xlfGetCell fetch.
		if !strings.Contains(content, `std::string callerFormat = "";`) {
			t.Errorf("caller-only must declare an empty callerFormat")
		}
		if strings.Contains(content, "CallExcel(xlfGetCell") {
			t.Errorf("caller-only must NOT emit the macro-only xlfGetCell fetch")
		}
		if strings.Contains(content, "ScopedXLOPER12Result xFormat;") {
			t.Errorf("caller-only must NOT declare the xlfGetCell result holder")
		}
	})

	t.Run("macro only: macro-sheet, no thread-safe, no caller block", func(t *testing.T) {
		content := mk(config.Function{Name: "MacroFn", Return: "string", Macro: true})
		blk := regBlock(content, "MacroFn")
		// Type string: Q (string return) + '#', no '$'.
		if !strings.Contains(blk, `std::wstring typeStr = L"Q#"`) {
			t.Errorf("macro-only type string must be Q# (macro-sheet, no '$'); block:\n%s", blk)
		}
		// No caller:true → no xlfCaller wrapper block at all.
		if strings.Contains(content, "CallExcel(xlfCaller") {
			t.Errorf("macro-only (no caller) must NOT emit an xlfCaller block")
		}
	})

	t.Run("caller+macro: macro-sheet + xlfGetCell format fetch", func(t *testing.T) {
		content := mk(config.Function{Name: "WhoAmIFmt", Return: "string", Caller: true, Macro: true})
		blk := regBlock(content, "WhoAmIFmt")
		if !strings.Contains(blk, `std::wstring typeStr = L"Q#"`) {
			t.Errorf("caller+macro type string must be Q# (macro-sheet, no '$'); block:\n%s", blk)
		}
		if !strings.Contains(content, "CallExcel(xlfCaller") {
			t.Errorf("caller+macro must call xlfCaller")
		}
		if !strings.Contains(content, "CallExcel(xlfGetCell") {
			t.Errorf("caller+macro must fetch the number format via xlfGetCell")
		}
		if !strings.Contains(content, "ScopedXLOPER12Result xFormat;") {
			t.Errorf("caller+macro must hold the xlfGetCell result in ScopedXLOPER12Result")
		}
		if !strings.Contains(content, "ConvertRange(xCaller.get(), builder, callerFormat)") {
			t.Errorf("caller+macro must call ConvertRange(xCaller.get(), builder, callerFormat)")
		}
	})
}

// TestXllMainRibbonImageWiring pins the Task-5 template wiring: the ribbon image
// table is installed at bootstrap, the GDI+ engine is torn down in xlAutoClose,
// the generated header is included, and CMake links gdiplus. Also a negative
// case: a ribbon-DISABLED render must not reference SetRibbonImages.
func TestXllMainRibbonImageWiring(t *testing.T) {
	t.Parallel()
	ribbonCfg := &config.Config{
		Project:  config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Commands: []config.Command{{Name: "RunReport", Handler: "RunReport"}},
		Ribbon: config.RibbonConfig{Tab: "T", Groups: []config.RibbonGroup{{
			Label: "G", Buttons: []config.RibbonButton{{Label: "B", Command: "RunReport", Image: "icon.png"}},
		}}},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}

	mainSrc := renderCppMain(t, ribbonCfg)
	for _, want := range []string{
		`#include "ribbon_images.h"`,
		"xll::ribbon::SetRibbonImages(GetXllRibbonImages());",
		"xll::ribbon::ShutdownRibbonImageEngine();",
	} {
		if !strings.Contains(mainSrc, want) {
			t.Errorf("xll_main.cpp missing %q", want)
		}
	}

	// CMakeLists.txt must link gdiplus for the ribbon image decoder TU.
	cmakeDir, err := os.MkdirTemp("", "gencpp_cmake")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(cmakeDir) })
	if err := generateCMake(ribbonCfg, cmakeDir); err != nil {
		t.Fatalf("generateCMake failed: %v", err)
	}
	cmakeBytes, err := os.ReadFile(filepath.Join(cmakeDir, "CMakeLists.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cmakeBytes), "gdiplus") {
		t.Errorf("CMakeLists.txt ribbon block must link gdiplus")
	}

	// Negative: a ribbon-disabled render gates SetRibbonImages behind
	// {{if .Ribbon.Enabled}}, so it must be absent.
	noRibbonCfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "Sum", Return: "int", Args: []config.Arg{{Name: "a", Type: "int"}}},
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}
	noRibbonSrc := renderCppMain(t, noRibbonCfg)
	if strings.Contains(noRibbonSrc, "SetRibbonImages") {
		t.Errorf("ribbon-disabled render must not reference SetRibbonImages")
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
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
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

// TestGenCpp_DescriptionEscaping is the regression for free-text config
// fields (function/command/arg descriptions, category, help topic) being
// emitted into C++ wide-string literals without escaping: a quote, backslash
// or newline in a description broke (or injected code into) the generated
// xll_main.cpp. All free-text emission must route through escapeCppString.
func TestGenCpp_DescriptionEscaping(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{
				Name:        "Tricky",
				Return:      "int",
				Description: "Says \"hi\" with a \\ backslash\nand a newline",
				Category:    "Cat\"egory",
				HelpTopic:   "https://example.com/?q=\"x\"",
				Args: []config.Arg{
					{Name: "a", Type: "int", Description: "arg \"desc\""},
				},
			},
		},
		Commands: []config.Command{
			{Name: "Run", Description: "Cmd \"quoted\"", Handler: "Run"},
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}

	content := renderCppMain(t, cfg)

	for _, want := range []string{
		`L"Says \"hi\" with a \\ backslash\nand a newline", // FunctionHelp`,
		`L"Cat\"egory", // Category`,
		`L"https://example.com/?q=\"x\"", // HelpTopic`,
		`L"arg \"desc\"",`,
		`L"Cmd \"quoted\"", // FunctionHelp`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("expected escaped literal %q, not found", want)
		}
	}

	// The raw (unescaped) forms must be gone: a bare interior quote would
	// terminate the literal early.
	for _, bad := range []string{
		"L\"Says \"hi\"",
		"L\"Cat\"egory\"",
		"L\"Cmd \"quoted\"\"",
	} {
		if strings.Contains(content, bad) {
			t.Errorf("found unescaped literal %q", bad)
		}
	}
}

// TestGenCpp_RtdThrottle pins the rtd.throttle_interval wiring: with the
// field set, xlAutoOpen applies Application.RTD.ThrottleInterval through the
// in-process Application route (GetExcelApplication), and the CMake render
// links oleacc even when the ribbon is disabled. Without the field, none of
// that machinery is emitted.
func TestGenCpp_RtdThrottle(t *testing.T) {
	t.Parallel()
	base := func(throttle string) *config.Config {
		return &config.Config{
			Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
			Functions: []config.Function{
				{Name: "Tick", Return: "any", Mode: "rtd"},
			},
			Rtd: config.RtdConfig{
				Enabled:          true,
				ProgID:           "TestProj.Rtd",
				Clsid:            "{11111111-2222-3333-4444-555555555555}",
				Description:      "t",
				ThrottleInterval: throttle,
			},
			Server: config.ServerConfig{
				Timeout: "2s",
				Launch:  &config.LaunchConfig{Enabled: new(bool)},
			},
		}
	}

	withThrottle := renderCppMain(t, base("250ms"))
	for _, want := range []string{
		"static IDispatch* GetExcelApplication()",
		"static bool SetRtdThrottleInterval(long ms)",
		"SetRtdThrottleInterval(250)",
		"#include <oleacc.h>",
		`#include "com/dispatch_helpers.h"`,
		// xlAutoOpen often runs before any workbook exists (no EXCEL7 child
		// window -> Application unreachable), so the calc-end callback must
		// carry the bounded one-shot retry.
		`TryApplyRtdThrottle("xlAutoOpen")`,
		`TryApplyRtdThrottle("calc end")`,
	} {
		if !strings.Contains(withThrottle, want) {
			t.Errorf("throttle render missing %q", want)
		}
	}

	without := renderCppMain(t, base(""))
	for _, bad := range []string{"SetRtdThrottleInterval", "GetExcelApplication", "oleacc.h"} {
		if strings.Contains(without, bad) {
			t.Errorf("throttle-less render must not contain %q", bad)
		}
	}

	// CMake: oleacc linked exactly when the throttle is configured (ribbon off).
	cmakeFor := func(cfg *config.Config) string {
		dir, err := os.MkdirTemp("", "gencpp_throttle")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		if err := generateCMake(cfg, dir); err != nil {
			t.Fatalf("generateCMake failed: %v", err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "CMakeLists.txt"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if !strings.Contains(cmakeFor(base("250ms")), "oleacc") {
		t.Errorf("CMake with throttle must link oleacc")
	}
	// oleacc is now linked UNCONDITIONALLY: the always-on date auto-format
	// module (src/xll_date_format.cpp) targets cells via the in-process
	// Application IDispatch route (AccessibleObjectFromWindow + COM
	// Range.NumberFormat, no selection change), so the COM libs must link in
	// every build — including throttle-less, ribbon-off, sync-only projects.
	if !strings.Contains(cmakeFor(base("")), "oleacc") {
		t.Errorf("CMake must link oleacc unconditionally (always-on date-format COM Range path)")
	}
}
