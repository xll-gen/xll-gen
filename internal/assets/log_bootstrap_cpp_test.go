package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The native log bootstrap moved out of internal/templates/xll_main.cpp.tmpl's
// xlAutoOpen preamble into include/xll_log.h + src/xll_log.cpp (2026-08-02):
// the ${BIN_DIR} derivation, the logging.dir resolution, the <proj>_native.log
// name, the InitLog call and its failure MessageBox. This file is where the
// BODY invariants that used to be greps over the rendered template now live.
//
// WHERE EACH OLD ASSERTION WENT:
//
//	internal/generator/gen_escape_precision_test.go::TestGenCpp_LogAndTempDirEscaping
//	  `std::wstring logDir = L"C:\\temp\\logs";`
//	                             -> the ESCAPING half stayed in the generator
//	                                (logging.dir is still emitted as a wide
//	                                literal, now as an InitNativeLogging
//	                                argument); the RESOLUTION half became
//	                                TestNativeLogPathsBehavior, which executes
//	                                it instead of grepping it
//	  `binDir = ExpandEnvVarsW(L"C:\\tmp\\extract");`
//	                             -> build.temp_dir now reaches the resolver as
//	                                the `tempPattern` narrow literal the template
//	                                already emitted for LaunchConfig::tempDir;
//	                                the singlefile ${BIN_DIR} behavior it used to
//	                                stand for is
//	                                TestSinglefileBinDirIsTheExtractionDir in the
//	                                native harness
//	  the XLL_DIR / TEMP_DIR / ${} branches, the empty->binDir fallback, the
//	  trailing-separator strip, the logPath join
//	                             -> log_paths_native_test.cpp, asserted by
//	                                EXECUTION and differentially against a
//	                                verbatim copy of the deleted template lines
//	  the InitLog call + MessageBox + "Logging Initialized Successfully"
//	                             -> TestNativeLogBootstrapContract below
//
// What stayed in internal/generator is the WIRING: that xlAutoOpen makes the one
// call, with this project's four xll.yaml values, escaped, and that the
// resolved directory is what LaunchConfig::logDir receives — see
// internal/generator/gen_log_bootstrap_test.go.

// TestNativeLogBootstrapContract pins the parts of the relocated bootstrap that
// the pure-resolution harness cannot reach: the declarations, the init call and
// the "report the failure, load anyway" outcome handling.
func TestNativeLogBootstrapContract(t *testing.T) {
	t.Parallel()
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	hdr, ok := m["include/xll_log.h"]
	if !ok {
		t.Fatalf("embedded asset include/xll_log.h not found")
	}
	code, ok := m["src/xll_log.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/xll_log.cpp not found")
	}

	for _, want := range []string{
		"struct NativeLogPaths {",
		"NativeLogPaths ResolveNativeLogPaths(const std::wstring& configuredDir,",
		"NativeLogPaths InitNativeLogging(const std::wstring& configuredDir,",
	} {
		if !strings.Contains(hdr, want) {
			t.Errorf("xll_log.h missing %q", want)
		}
	}
	// The three fields are the whole contract with the generated TU: `dir` is
	// what LaunchConfig::logDir gets, so the Go log lands beside the native one
	// (AGENTS.md §18.12, the single-directory contract).
	for _, want := range []string{"std::wstring binDir;", "std::wstring dir;", "std::wstring path;"} {
		if !strings.Contains(hdr, want) {
			t.Errorf("NativeLogPaths missing field %q", want)
		}
	}

	for _, want := range []string{
		// The one module-path query lives at the one call site that needs it,
		// and it is passed INTO the resolver so the resolver stays testable.
		"isSingleFile, GetXllDir());",
		// The existing sink initializer is still what actually opens the file.
		"if (!InitLog(paths.path, level, tempPattern, projectName, isSingleFile, logInitError)) {",
		// A failed init is REPORTED AND SURVIVED. The message box is the only
		// channel left (the log is what just failed), and InitNativeLogging must
		// still return so xlAutoOpen carries on loading the add-in.
		`MessageBoxA(NULL, ("Failed to initialize logging: " + logInitError).c_str(), "XLL Initialization Warning", MB_OK | MB_ICONWARNING);`,
		`SAFE_LOG_INFO("Logging Initialized Successfully. LogPath: " + WideToUtf8(paths.path));`,
		"return paths;",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("src/xll_log.cpp missing %q", want)
		}
	}

	// The measured finding that justifies the non-fatal path must travel WITH
	// the code, not be left behind in the template.
	if !strings.Contains(code, "Logging is critical for debugging but not for core functionality.") {
		t.Errorf("src/xll_log.cpp lost the comment recording WHY a failed log init is not fatal")
	}

	// A failure must not also abort the load by some other route.
	if strings.Contains(code, "return NativeLogPaths();") {
		t.Errorf("InitNativeLogging discards the resolved paths on the failure branch; " +
			"LaunchConfig::logDir would then be empty and the Go log would fall back to " +
			"the launch cwd, re-opening the split-log bug (AGENTS.md §18.12)")
	}
}

// TestNativeLogPathsBehavior compiles the EMBEDDED xll_log.cpp offline and runs
// internal/assets/testdata/log_paths_native_test.cpp against it.
//
// Requires g++ and a types checkout (XLLGEN_TYPES_SRC, else the sibling
// ../types). Unlike the cache/gridarg harnesses it needs NO FetchContent cache:
// the resolver pulls in nothing but <string>, windows.h and types' string
// helpers. Skipped (not failed) when the toolchain is absent, and under -short.
func TestNativeLogPathsBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping C++ compile+run gate in short mode")
	}
	if runtime.GOOS != "windows" {
		t.Skip("xll_log.cpp is Windows-only (windows.h / ExpandEnvironmentStringsW)")
	}

	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ not on PATH; skipping xll_log compile+run gate")
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

	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}

	dir := t.TempDir()
	incDir := filepath.Join(dir, "include")
	if err := os.MkdirAll(incDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The generated layout uses FLAT includes (AGENTS.md §16.3).
	for _, name := range []string{"xll_log.h", "xll_path.h"} {
		content, ok := m["include/"+name]
		if !ok {
			t.Fatalf("embedded include/%s not found in assets", name)
		}
		if err := os.WriteFile(filepath.Join(incDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var srcFiles []string
	for _, name := range []string{"xll_log.cpp", "xll_path.cpp"} {
		content, ok := m["src/"+name]
		if !ok {
			t.Fatalf("embedded src/%s not found in assets", name)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		srcFiles = append(srcFiles, p)
	}

	harness := filepath.Join(filepath.Dir(thisFile), "testdata", "log_paths_native_test.cpp")
	if _, err := os.Stat(harness); err != nil {
		t.Fatalf("harness %s missing: %v", harness, err)
	}

	exePath := filepath.Join(dir, "log_paths_native_test.exe")

	// gnu++17 (not c++17) and NOGDI mirror the real build: types/xlcall.h uses
	// the MS spellings `_cdecl`/`pascal`, which MinGW only defines outside
	// __STRICT_ANSI__, and windows.h's ERROR macro collides with
	// LogLevel::ERROR (CMakeLists.txt.tmpl defines NOGDI for exactly that
	// reason).
	//
	// -static mirrors CMakeLists.txt.tmpl's link options and is REQUIRED here,
	// unlike the cache/gridarg harnesses: xll_log.cpp's std::timed_mutex pulls
	// in libwinpthread, and a dynamically linked harness dies at load with
	// STATUS_ENTRYPOINT_NOT_FOUND (0xc0000139) unless the exact MinGW runtime
	// DLLs happen to be first on PATH.
	args := []string{
		"-std=gnu++17", "-O1", "-DNOGDI", "-DUNICODE", "-D_UNICODE", "-fexceptions",
		"-static", "-static-libgcc", "-static-libstdc++",
		"-I", incDir,
		"-I", typesInc,
		"-o", exePath,
		harness,
	}
	args = append(args, srcFiles...)
	args = append(args, filepath.Join(typesSrc, "src", "utility.cpp"))
	if out, err := exec.Command(gxx, args...).CombinedOutput(); err != nil {
		t.Fatalf("log_paths native harness failed to compile: %v\n%s", err, out)
	}

	out, err := exec.Command(exePath).CombinedOutput()
	t.Logf("log_paths_native_test output:\n%s", out)
	if err != nil {
		t.Fatalf("log_paths native harness reported failures (or crashed): %v", err)
	}
	if !strings.Contains(string(out), "0 failures") {
		t.Fatalf("log_paths native harness did not report 0 failures")
	}
}
