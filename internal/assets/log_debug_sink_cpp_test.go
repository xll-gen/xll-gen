package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// THE NO-LOG-FILE SINK (2026-08-03, backlog §1 "2026-08-03 MED 사이클 파생").
//
// WriteLogUnconditional in src/xll_log.cpp used to open with
// `if (g_logPath.empty()) return;`, and g_logPath is written in exactly one
// place: InitLog, reached only from InitNativeLogging, called only from
// xlAutoOpen. Every line logged before that call — or for the whole session
// after a FAILED InitLog — was therefore dropped in silence.
//
// The guard that made this matter is RibbonAddIn::OnConnection: it refuses a COM
// activation in a session where xlAutoOpen NEVER RAN (the COM Add-ins row and the
// HKCU InprocServer32 deliberately survive an add-in disable), and it logs the
// refusal at WARN. By construction no log file exists in that state, so the one
// line that made the refusal diagnosable was dropped EXACTLY when it mattered —
// while both the code comment and TestOnConnectionRefusesWithoutConnectContext's
// rationale claimed the opposite. No assertion over the refusal site could fix
// that; only a sink that exists before xlAutoOpen.
//
// TestNativeLogDebugSinkBehavior below is the executable half (it runs the
// loggers with and without a log file and observes where the line goes);
// TestNativeLogDebugSinkContract is the source half, for the parts a run cannot
// show — chiefly that the branch sits BEFORE the mutex and the file I/O.

// TestNativeLogDebugSinkBehavior compiles the EMBEDDED xll_log.cpp offline and
// runs internal/assets/testdata/log_debug_sink_native_test.cpp against it.
//
// Same toolchain requirements, and the same skip-don't-fail policy, as
// TestNativeLogPathsBehavior in log_bootstrap_cpp_test.go: g++ plus a types
// checkout (XLLGEN_TYPES_SRC, else the sibling ../types), no FetchContent cache.
func TestNativeLogDebugSinkBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping C++ compile+run gate in short mode")
	}
	if runtime.GOOS != "windows" {
		t.Skip("xll_log.cpp is Windows-only (windows.h / OutputDebugStringW)")
	}

	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ not on PATH; skipping xll_log debug-sink compile+run gate")
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

	harness := filepath.Join(filepath.Dir(thisFile), "testdata", "log_debug_sink_native_test.cpp")
	if _, err := os.Stat(harness); err != nil {
		t.Fatalf("harness %s missing: %v", harness, err)
	}

	exePath := filepath.Join(dir, "log_debug_sink_native_test.exe")

	// gnu++17 (not c++17), NOGDI and -static for exactly the reasons spelled out
	// in log_bootstrap_cpp_test.go: the MS `_cdecl`/`pascal` spellings in
	// types/xlcall.h, windows.h's ERROR macro colliding with LogLevel::ERROR, and
	// std::timed_mutex dragging in libwinpthread.
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
		t.Fatalf("log_debug_sink native harness failed to compile: %v\n%s", err, out)
	}

	out, err := exec.Command(exePath).CombinedOutput()
	t.Logf("log_debug_sink_native_test output:\n%s", out)
	if err != nil {
		t.Fatalf("log_debug_sink native harness reported failures (or crashed): %v", err)
	}
	if !strings.Contains(string(out), "0 failures") {
		t.Fatalf("log_debug_sink native harness did not report 0 failures")
	}
}

// TestNativeLogDebugSinkContract pins the two properties the run above cannot
// observe from outside the process.
func TestNativeLogDebugSinkContract(t *testing.T) {
	t.Parallel()
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	raw, ok := m["src/xll_log.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/xll_log.cpp not found")
	}
	code := stripCppCommentsAsset(raw)

	// The DEFINITION, not the forward declaration above it: the declaration ends
	// in `bool boundedWait = false);`, the definition in `bool boundedWait) {`.
	// (Anchoring on the whole signature would be line-ending sensitive — the
	// assets carry CRLF.)
	wuIdx := strings.Index(code, "bool boundedWait) {")
	if wuIdx < 0 {
		t.Fatalf("could not find the WriteLogUnconditional DEFINITION in src/xll_log.cpp")
	}
	body := code[wuIdx:]
	if e := strings.Index(body, "\nvoid LogError("); e > 0 {
		body = body[:e]
	}

	// 1. The empty-path branch must EMIT, not `return` bare. This is the whole
	//    fix: OnConnection's refusal runs in a session where InitLog never ran.
	//    Located and read STRUCTURALLY (emptyLogPathBranch) rather than by
	//    matching one brace style — `if (g_logPath.empty()) { return; }` is the
	//    same defect and must not slip past.
	loc := reEmptyLogPathIf.FindStringIndex(body)
	if loc == nil {
		t.Fatalf("could not find the empty-g_logPath branch in WriteLogUnconditional:\n%s", body)
	}
	emptyIdx := loc[0]
	branch, found := emptyLogPathBranch(body)
	if !found {
		t.Fatalf("could not read the body of the empty-g_logPath branch:\n%s", body)
	}
	if !strings.Contains(branch, "WriteDebugOutputFallback(levelStr, msg);") {
		t.Errorf("WriteLogUnconditional drops every line while g_logPath is empty. That is the "+
			"state RibbonAddIn::OnConnection's refusal guard runs in — xlAutoOpen never ran, so "+
			"InitNativeLogging never ran, so the refusal WARN is silently discarded exactly when "+
			"it is the only diagnosis available. The branch must route the line to the "+
			"debug-output fallback; as written it is:\n%s", branch)
	}

	// 2. …and it must do so BEFORE the lock and the file I/O. The teardown
	//    loggers reach this function under the Phase 2 "bounded kernel calls
	//    only" rule (AGENTS.md §20.2); a fallback placed after the timed_mutex
	//    acquisition would make the no-log-file path pay for a lock it can never
	//    need.
	lockIdx := strings.Index(body, "std::unique_lock<std::timed_mutex> lock(g_logMutex")
	if lockIdx < 0 {
		t.Fatalf("the g_logMutex acquisition is gone from WriteLogUnconditional:\n%s", body)
	}
	if emptyIdx > lockIdx {
		t.Errorf("the no-log-file branch (@%d) must precede the g_logMutex acquisition (@%d): the "+
			"teardown loggers call this under the bounded-kernel-call rule and must not take a lock "+
			"to discover there is no file", emptyIdx, lockIdx)
	}

	// The sink itself must be the debug-output channel — the one sink that needs
	// no initialization, no file and no configuration. A fallback that wrote to a
	// second file would just be a second thing that can fail to open.
	fbIdx := strings.Index(code, "static void WriteDebugOutputFallback(")
	if fbIdx < 0 {
		t.Fatalf("WriteDebugOutputFallback is not defined in src/xll_log.cpp")
	}
	fb := code[fbIdx:]
	if e := strings.Index(fb, "\nstatic void WriteLogUnconditional("); e > 0 {
		fb = fb[:e]
	}
	if !strings.Contains(fb, "OutputDebugStringW(") {
		t.Errorf("the no-log-file fallback must emit through OutputDebugStringW:\n%s", fb)
	}
	if strings.Contains(fb, "std::ofstream") || strings.Contains(fb, "CreateFile") {
		t.Errorf("the no-log-file fallback must not open a file — it exists precisely because the "+
			"file sink is unavailable:\n%s", fb)
	}
	// StringToWString throws std::length_error on an oversized input. A logger
	// that can throw is unusable from the paths this fallback exists for.
	if strings.Contains(fb, "StringToWString(") {
		t.Errorf("the fallback must not convert via StringToWString: it THROWS std::length_error, and "+
			"this sink runs on paths (COM activation refusal, teardown) that must not raise:\n%s", fb)
	}
	// The level must survive into the fallback line.
	if !strings.Contains(fb, "levelStr") {
		t.Errorf("the fallback line must carry the level tag:\n%s", fb)
	}
}
