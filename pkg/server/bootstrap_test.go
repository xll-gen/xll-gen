package server

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/pkg/log"
)

// The generated server's logger bootstrap moved out of
// internal/templates/server.go.tmpl into InitServerLogging (2026-08-02). These
// tests EXECUTE it; the template gate that used to grep the rendered text now
// only asserts the wiring (see
// internal/generator/gen_escape_precision_test.go::TestGenGo_LogDirEscaping and
// gen_log_bootstrap_test.go).
//
// None of them call t.Parallel(): they swap os.Stdout and re-point the process
// -wide slog default, so they must not overlap with each other. Go runs every
// non-parallel top-level test to completion before resuming the parallel ones,
// which is what keeps that safe inside this package.

// releaseLogFile re-points the global logger at stdout, which closes the file
// pkg/log is holding. Load-bearing on Windows: t.TempDir()'s RemoveAll fails on
// a file that is still open, so without this the test would fail in cleanup with
// an error that says nothing about what it is testing.
func releaseLogFile(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { _ = log.Init("", "info") })
}

// captureStdout redirects os.Stdout for the duration of fn and returns what was
// written. fmt.Printf resolves os.Stdout at call time, and log.Init("") captures
// it when it builds the handler, so both the fallback messages and the stdout
// log sink are observable this way.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	func() {
		defer func() {
			os.Stdout = orig
			_ = w.Close()
		}()
		fn()
	}()
	out := <-done
	_ = r.Close()
	return out
}

// TestInitServerLogging_LauncherRedirectUsesStdout pins the sink choice for the
// production path: the C++ launcher (xll_launch.cpp) sets XLL_LOG_TO_STDOUT=1
// AND redirects this process's stdout into <logDir>\<proj>_go.log itself. The
// server must therefore write to stdout and must NOT open that file a second
// time — two writers interleave partial lines, and the extra handle keeps the
// file locked independently of the inherited one.
func TestInitServerLogging_LauncherRedirectUsesStdout(t *testing.T) {
	dir := t.TempDir()
	releaseLogFile(t)
	t.Setenv(StdoutLogEnvVar, "1")

	out := captureStdout(t, func() {
		InitServerLogging(dir, "info", "Proj")
		log.Info("marker-from-stdout-sink")
	})

	if !strings.Contains(out, "marker-from-stdout-sink") {
		t.Errorf("with %s=1 the logger must write to stdout; got %q", StdoutLogEnvVar, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "Proj_go.log")); err == nil {
		t.Errorf("the launcher already owns <proj>_go.log; the server opened it a SECOND time")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the stdout branch created files in logging.dir: %v", entries)
	}
}

// TestInitServerLogging_NoLauncherOpensTheProjectLogFile pins the other sink:
// started WITHOUT the launcher (dev `go run`, regtest, a user's own main) the
// server resolves logging.dir itself and opens <logDir>\<projectName>_go.log.
// The file NAME is part of the contract — README/TUTORIAL troubleshooting and
// the C++ side's ResolveServerPath both spell it <proj>_go.log.
func TestInitServerLogging_NoLauncherOpensTheProjectLogFile(t *testing.T) {
	dir := t.TempDir()
	releaseLogFile(t)
	t.Setenv(StdoutLogEnvVar, "")

	out := captureStdout(t, func() {
		InitServerLogging(dir, "info", "Proj")
		log.Info("marker-from-file-sink")
	})

	if strings.Contains(out, "marker-from-file-sink") {
		t.Errorf("without the launcher the log must go to the FILE, not stdout; got %q", out)
	}
	body, err := os.ReadFile(filepath.Join(dir, "Proj_go.log"))
	if err != nil {
		t.Fatalf("expected <logDir>\\Proj_go.log to be created: %v", err)
	}
	if !strings.Contains(string(body), "marker-from-file-sink") {
		t.Errorf("Proj_go.log does not contain the logged line: %q", body)
	}
}

// TestInitServerLogging_OnlyTheExactValueOneSelectsStdout: the comparison is
// `== "1"`, not "is the variable set" and not a truthiness test. A user whose
// environment happens to carry XLL_LOG_TO_STDOUT=0 (or the empty string, which
// is how a cleared variable often arrives) must still get the file sink,
// otherwise their server logs vanish into a stdout nobody is capturing.
func TestInitServerLogging_OnlyTheExactValueOneSelectsStdout(t *testing.T) {
	for _, val := range []string{"", "0", "true", "TRUE", "yes", "11", " 1"} {
		t.Run("val="+val, func(t *testing.T) {
			dir := t.TempDir()
			releaseLogFile(t)
			t.Setenv(StdoutLogEnvVar, val)

			InitServerLogging(dir, "info", "Proj")
			if _, err := os.Stat(filepath.Join(dir, "Proj_go.log")); err != nil {
				t.Errorf("%s=%q must NOT select the stdout sink: %v", StdoutLogEnvVar, val, err)
			}
		})
	}
}

// TestInitServerLogging_HonorsTheConfiguredLevel: logging.level has to reach
// BOTH sinks. It used to be interpolated into two separate log.Init call sites
// in the template, so nothing stopped one of them from drifting.
func TestInitServerLogging_HonorsTheConfiguredLevel(t *testing.T) {
	t.Run("stdout sink", func(t *testing.T) {
		dir := t.TempDir()
		releaseLogFile(t)
		t.Setenv(StdoutLogEnvVar, "1")

		out := captureStdout(t, func() {
			InitServerLogging(dir, "error", "Proj")
			log.Info("info-must-be-filtered")
			log.Error("error-must-survive")
		})
		if strings.Contains(out, "info-must-be-filtered") {
			t.Errorf("level=error did not reach the stdout sink: %q", out)
		}
		if !strings.Contains(out, "error-must-survive") {
			t.Errorf("level=error suppressed an ERROR line: %q", out)
		}
	})

	t.Run("file sink", func(t *testing.T) {
		dir := t.TempDir()
		releaseLogFile(t)
		t.Setenv(StdoutLogEnvVar, "")

		InitServerLogging(dir, "error", "Proj")
		log.Info("info-must-be-filtered")
		log.Error("error-must-survive")

		body, err := os.ReadFile(filepath.Join(dir, "Proj_go.log"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "info-must-be-filtered") {
			t.Errorf("level=error did not reach the file sink: %q", body)
		}
		if !strings.Contains(string(body), "error-must-survive") {
			t.Errorf("level=error suppressed an ERROR line: %q", body)
		}
	})
}

// TestInitServerLogging_ExpandsLogDirPlaceholders: logging.dir is a raw xll.yaml
// string, so the ${XLL_DIR} / ${BIN_DIR} / ${ANY_ENV_VAR} expansion is part of
// the bootstrap, not of the caller. This is the standalone-Go half of the
// single-directory contract (AGENTS.md §18.12 item 4).
func TestInitServerLogging_ExpandsLogDirPlaceholders(t *testing.T) {
	base := t.TempDir()
	releaseLogFile(t)
	t.Setenv(StdoutLogEnvVar, "")
	t.Setenv("XLL_DIR", base)

	InitServerLogging(filepath.Join("${XLL_DIR}", "nested"), "info", "Proj")
	log.Info("placeholder-marker")

	body, err := os.ReadFile(filepath.Join(base, "nested", "Proj_go.log"))
	if err != nil {
		t.Fatalf("${XLL_DIR} was not expanded (expected <XLL_DIR>\\nested\\Proj_go.log): %v", err)
	}
	if !strings.Contains(string(body), "placeholder-marker") {
		t.Errorf("log line missing from the expanded path: %q", body)
	}
}

// TestInitServerLogging_SurvivesAnUnusableLogDir is the "report but keep going"
// rule. An add-in that works without a log beats one that refuses to start
// because its log directory is read-only, so the bootstrap must neither panic
// nor return early into the caller's error path — Serve carries on to ConnectSHM
// either way. The message is asserted verbatim because it is the ONLY signal a
// user gets that logging is off.
func TestInitServerLogging_SurvivesAnUnusableLogDir(t *testing.T) {
	dir := t.TempDir()
	releaseLogFile(t)
	t.Setenv(StdoutLogEnvVar, "")

	// A FILE where the log directory should be: MkdirAll cannot create a
	// directory under it, so log.Init fails.
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		InitServerLogging(filepath.Join(blocker, "logs"), "info", "Proj")
	})

	if !strings.Contains(out, "Failed to initialize logger:") {
		t.Errorf("a failed file-sink init must print %q to stdout; got %q",
			"Failed to initialize logger:", out)
	}
	if strings.Contains(out, "Failed to initialize stdout logger:") {
		t.Errorf("the file-sink failure reported the STDOUT branch's message: %q", out)
	}
}

// TestInitServerLogging_ResolvesTheSameFileTwice: Serve can run more than once
// in a process (tests, a user main that restarts), and pkg/log closes the
// previous handle on re-Init. A second bootstrap must land on the same path and
// keep appending rather than leaking a handle or truncating.
func TestInitServerLogging_ResolvesTheSameFileTwice(t *testing.T) {
	dir := t.TempDir()
	releaseLogFile(t)
	t.Setenv(StdoutLogEnvVar, "")

	InitServerLogging(dir, "info", "Proj")
	log.Info("first-run")
	InitServerLogging(dir, "info", "Proj")
	log.Info("second-run")

	body, err := os.ReadFile(filepath.Join(dir, "Proj_go.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first-run", "second-run"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("re-init truncated the log; %q missing from %q", want, body)
		}
	}
}
