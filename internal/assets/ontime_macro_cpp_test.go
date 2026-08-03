package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The two xlcOnTime macro registrations moved out of
// internal/templates/xll_main.cpp.tmpl's xlAutoOpen (2026-08-03) into
// xll::RegisterOnTimeMacro, an inline function in
// internal/assets/files/include/xll_lifecycle.h. They were two byte-identical
// 18-line xlfRegister calls differing only in the macro name and the log label,
// and neither carried a template variable.
//
// WHERE THE OLD ASSERTIONS WENT:
//
//	internal/generator/gen_deferred_runner_test.go
//	  the rendered `xll::RegisterFunction(... 2, ...)` blocks
//	                       -> the SHAPE (macroType 2, TypeText "I",
//	                          FunctionText == Procedure, hidden, non-fatal on
//	                          rejection, register id released) is EXECUTED by
//	                          testdata/ontime_macro_native_test.cpp below
//	                       -> the WIRING (which macros THIS project registers,
//	                          and that the registered name is the same accessor
//	                          the export and the xlcOnTime schedule use) stayed
//	                          in internal/generator
//
// None of the shape was visible to the old greps: `2,` in a rendered argument
// list says nothing about which parameter it lands on, and "the add-in still
// loads when Excel rejects the registration" has no textual form at all.

// TestOnTimeMacroRegistrationBehavior compiles the EMBEDDED xll_lifecycle.h
// against stubbed xlfRegister / Excel12 / LogError and runs the assertions in
// internal/assets/testdata/ontime_macro_native_test.cpp.
//
// Requires g++ and a types checkout (XLLGEN_TYPES_SRC, else the sibling
// ../types). Like the log-paths harness it needs NO FetchContent cache and no
// shm: RegisterOnTimeMacro is inline precisely so a TU can reach it without
// src/xll_lifecycle.cpp (which pulls in shm, the worker and the ribbon add-in).
// Skipped (not failed) when the toolchain is absent, and under -short.
func TestOnTimeMacroRegistrationBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping C++ compile+run gate in short mode")
	}
	if runtime.GOOS != "windows" {
		t.Skip("xll_lifecycle.h is Windows-only (windows.h / XLOPER12)")
	}

	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ not on PATH; skipping RegisterOnTimeMacro compile+run gate")
	}

	_, thisFile, ok := callerFile()
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// .../internal/assets/<file> -> repo root -> workspace root (holds types/).
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	typesSrc := os.Getenv("XLLGEN_TYPES_SRC")
	if typesSrc == "" {
		typesSrc = filepath.Join(filepath.Dir(repoRoot), "types")
	}
	typesInc := filepath.Join(typesSrc, "include")
	if _, err := os.Stat(filepath.Join(typesInc, "types", "xlcall.h")); err != nil {
		t.Skipf("types headers not found under %s; skipping (set XLLGEN_TYPES_SRC)", typesInc)
	}
	shmInc := os.Getenv("XLLGEN_SHM_SRC")
	if shmInc == "" {
		shmInc = filepath.Join(filepath.Dir(repoRoot), "shm")
	}
	shmInc = filepath.Join(shmInc, "include")
	// xll_lifecycle.h includes xll_launch.h and shm/Logger.h transitively; the
	// headers alone are enough (nothing from shm is linked).
	if _, err := os.Stat(filepath.Join(shmInc, "shm", "Logger.h")); err != nil {
		t.Skipf("shm headers not found under %s; skipping (set XLLGEN_SHM_SRC)", shmInc)
	}

	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}

	dir := t.TempDir()
	incDir := filepath.Join(dir, "include")
	if err := os.MkdirAll(incDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The generated layout uses FLAT includes (AGENTS.md §16.3). These are what
	// xll_lifecycle.h pulls in from the asset tree.
	for _, name := range []string{"xll_lifecycle.h", "xll_log.h", "xll_launch.h", "xll_path.h"} {
		content, ok := m["include/"+name]
		if !ok {
			t.Fatalf("embedded include/%s not found in assets", name)
		}
		if err := os.WriteFile(filepath.Join(incDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	harness := filepath.Join(filepath.Dir(thisFile), "testdata", "ontime_macro_native_test.cpp")
	if _, err := os.Stat(harness); err != nil {
		t.Fatalf("harness %s missing: %v", harness, err)
	}
	exePath := filepath.Join(dir, "ontime_macro_native_test.exe")

	// gnu++17 (not c++17) and NOGDI mirror the real build: types/xlcall.h uses
	// the MS spellings `_cdecl`/`pascal`, which MinGW only defines outside
	// __STRICT_ANSI__, and windows.h's ERROR macro collides with
	// LogLevel::ERROR.
	args := []string{
		"-std=gnu++17", "-O1", "-DNOGDI", "-DUNICODE", "-D_UNICODE", "-fexceptions",
		"-static", "-static-libgcc", "-static-libstdc++",
		"-I", incDir,
		"-I", typesInc,
		"-I", shmInc,
		"-o", exePath,
		harness,
	}
	if out, err := exec.Command(gxx, args...).CombinedOutput(); err != nil {
		t.Fatalf("ontime_macro native harness failed to compile: %v\n%s", err, out)
	}

	out, err := exec.Command(exePath).CombinedOutput()
	t.Logf("ontime_macro_native_test output:\n%s", out)
	if err != nil {
		t.Fatalf("ontime_macro native harness reported failures (or crashed): %v", err)
	}
	if !strings.Contains(string(out), "ALL OK") {
		t.Fatalf("ontime_macro native harness did not report ALL OK")
	}
}
