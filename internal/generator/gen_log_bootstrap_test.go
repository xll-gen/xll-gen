package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// The logging bootstrap moved out of both templates into static code on
// 2026-08-02 (standing project direction: keep generated templates minimal,
// prefer static code — AGENTS.md §18.11.1 records the same move for the ribbon
// connect machinery):
//
//	xll_main.cpp.tmpl  xlAutoOpen preamble -> include/xll_log.h + src/xll_log.cpp
//	                                          (xll::InitNativeLogging)
//	server.go.tmpl     the XLL_LOG_TO_STDOUT if/else
//	                                       -> pkg/server (server.InitServerLogging)
//
// BODY invariants are pinned where the bodies now live:
//
//	internal/assets/log_bootstrap_cpp_test.go   (declarations, InitLog call,
//	                                             MessageBox, the non-fatal rule)
//	internal/assets/testdata/log_paths_native_test.cpp
//	                                            (the resolution, EXECUTED, plus a
//	                                             differential check against a
//	                                             verbatim copy of the deleted
//	                                             template lines)
//	pkg/server/bootstrap_test.go                (sink choice, level, placeholder
//	                                             expansion, the survive-a-bad-dir
//	                                             fallback message)
//
// THIS file owns the WIRING, which cannot move: that each template makes the one
// call with THIS project's values, that the resolved directory reaches
// LaunchConfig::logDir, and — in the spirit of §18.6.1's
// TestChunkSegmentLogicIsExtracted — that neither body has been re-inlined. A
// re-inlined copy would shadow the static code, leave the tests above green, and
// put untested code back in the shipped XLL / generated server.

func logBootstrapCfg() *config.Config {
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Logging: config.LoggingConfig{Level: "debug", Dir: "${BIN_DIR}"},
		Functions: []config.Function{
			{Name: "Sum", Return: "int", Args: []config.Arg{{Name: "a", Type: "int"}}},
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}
	*cfg.Server.Launch.Enabled = true
	return cfg
}

// TestXllMainDelegatesTheLogBootstrap pins the C++ wiring.
func TestXllMainDelegatesTheLogBootstrap(t *testing.T) {
	t.Parallel()
	src := renderCppMain(t, logBootstrapCfg())

	// 1. ONE call, carrying all four generated values in the documented order
	//    (configuredDir, level, projectName, tempPattern, isSingleFile).
	if !strings.Contains(src, `xll::NativeLogPaths logPaths = xll::InitNativeLogging(`) {
		t.Errorf("xlAutoOpen does not call xll::InitNativeLogging:\n%s", src)
	}
	if !strings.Contains(src, `L"${BIN_DIR}", "debug", "TestProj",`) {
		t.Errorf("xlAutoOpen does not pass logging.dir / logging.level / project name to " +
			"InitNativeLogging")
	}
	if !strings.Contains(src, "tempPattern, isSingleFile);") {
		t.Errorf("xlAutoOpen does not pass build.temp_dir / the singlefile flag to " +
			"InitNativeLogging; the singlefile ${BIN_DIR} would then resolve to the XLL " +
			"directory and split the two logs (AGENTS.md §18.12)")
	}
	if got := strings.Count(src, "xll::InitNativeLogging("); got != 1 {
		t.Errorf("InitNativeLogging is called %d times, want exactly 1 — a second bootstrap "+
			"would re-open the sink and re-run the MessageBox", got)
	}

	// 2. The SINGLE-DIRECTORY CONTRACT (AGENTS.md §18.12): the directory the
	//    native log resolved to is the directory the launcher writes
	//    <proj>_go.log into. A per-side default here was the original split-log
	//    bug in singlefile mode.
	if !strings.Contains(src, "cfg.logDir = logPaths.dir;") {
		t.Errorf("LaunchConfig::logDir is not fed from the resolved native log directory:\n%s", src)
	}

	// 3. The relocated body must NOT be re-inlined. Code only — the template's
	//    own comment names the rules it delegated, so a raw substring search
	//    would false-positive on the prose.
	code := stripCppComments(src)
	for _, gone := range []string{
		`if (logDir == L"XLL_DIR")`,
		`logDir = ExpandEnvVarsW(L"${TEMP}")`,
		`ReplaceAll(logDir, L"${BIN_DIR}"`,
		"if (logDir.empty()) logDir = binDir;",
		"_native.log\"",
		"std::string logInitError;",
		"Failed to initialize logging: ",
		"Logging Initialized Successfully",
		"std::wstring binDir = GetXllDir();",
	} {
		if strings.Contains(code, gone) {
			t.Errorf("xll_main.cpp re-inlines the relocated log bootstrap (%q); it must live "+
				"ONLY in include/xll_log.h + src/xll_log.cpp, or the shipped code stops being "+
				"the code the asset tests cover", gone)
		}
	}

	// 4. The two locals the bootstrap used to own and that nothing else needs
	//    are gone, so a future edit cannot quietly start reading a stale copy.
	for _, gone := range []string{"std::wstring logPath", `std::string projName = "`} {
		if strings.Contains(code, gone) {
			t.Errorf("xll_main.cpp still declares %q; the bootstrap owns that now", gone)
		}
	}
}

// TestXllMainLogBootstrapSinglefile: singlefile is the configuration §18.12
// exists for, and the flag/temp-dir pair is what carries it across the call.
func TestXllMainLogBootstrapSinglefile(t *testing.T) {
	t.Parallel()
	cfg := logBootstrapCfg()
	cfg.Build = config.BuildConfig{Singlefile: "xll", TempDir: `${TEMP}\pkg`}
	src := renderCppMain(t, cfg)

	if !strings.Contains(src, "isSingleFile = true;") ||
		!strings.Contains(src, `tempPattern = "${TEMP}\\pkg";`) {
		t.Errorf("the singlefile branch must still set both locals before the bootstrap call:\n%s", src)
	}
	// Both must be SET before the call reads them.
	call := strings.Index(src, "xll::InitNativeLogging(")
	set := strings.Index(src, "isSingleFile = true;")
	if call < 0 || set < 0 || set > call {
		t.Errorf("isSingleFile is assigned after InitNativeLogging reads it (set=%d call=%d)", set, call)
	}
	// LaunchConfig still gets the same pattern, from the same local.
	if !strings.Contains(src, "cfg.tempDir = StringToWString(tempPattern);") {
		t.Errorf("LaunchConfig::tempDir must keep using the same tempPattern local:\n%s", src)
	}
}

// TestGenServer_LogBootstrapIsDelegated pins the Go wiring.
func TestGenServer_LogBootstrapIsDelegated(t *testing.T) {
	t.Parallel()
	rendered := renderTemplate(t, "server.go.tmpl", serverDataFor(logBootstrapCfg()))
	assertParses(t, "server.go", rendered)

	if !strings.Contains(rendered, `server.InitServerLogging("logs", "info", "TestProj")`) {
		t.Errorf("Serve does not delegate the logger bootstrap to pkg/server:\n%s", rendered)
	}

	// The bootstrap must run BEFORE anything that logs, or the first lines of a
	// failed SHM connect — the commonest startup failure — go nowhere.
	init := strings.Index(rendered, "server.InitServerLogging(")
	firstLog := strings.Index(rendered, `log.Info("Connecting to SHM"`)
	if init < 0 || firstLog < 0 || init > firstLog {
		t.Errorf("the logger must be initialized before the first log line (init=%d firstLog=%d)",
			init, firstLog)
	}

	srv := stripCppComments(rendered)
	for _, gone := range []string{
		`os.Getenv("XLL_LOG_TO_STDOUT")`,
		"server.InitLog(",
		`log.Init("",`,
		"Failed to initialize stdout logger",
		"Failed to initialize logger",
	} {
		if strings.Contains(srv, gone) {
			t.Errorf("server.go re-inlines the relocated logger bootstrap (%q); it must live "+
				"ONLY in pkg/server.InitServerLogging", gone)
		}
	}
}

// TestGenServer_LogBootstrapCompilesWithoutFunctionsOrRtd: the two Printf
// fallbacks that moved out were fmt's only UNCONDITIONAL uses in server.go.tmpl
// — everything left sits inside {{if .Functions}} / {{if .Rtd.Enabled}}. A
// project with neither would have failed to build on an unused "fmt" import,
// and that project is exactly the one nobody generates by hand.
func TestGenServer_LogBootstrapCompilesWithoutFunctionsOrRtd(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "Bare", Version: "0.1"},
		Server:  config.ServerConfig{Timeout: "2s", Launch: &config.LaunchConfig{Enabled: new(bool)}},
	}
	rendered := renderTemplate(t, "server.go.tmpl", serverDataFor(cfg))
	// assertParses only proves syntax; the unused-import error is a TYPE error,
	// so assert the blank identifier that keeps fmt alive is actually emitted.
	assertParses(t, "server.go", rendered)
	if !strings.Contains(rendered, "var _ = fmt.Sprintf") {
		t.Errorf("server.go must keep an unconditional use of fmt now that the two logger "+
			"Printf fallbacks moved to pkg/server:\n%s", rendered)
	}
}
