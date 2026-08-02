package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// TestGenCpp_LogAndTempDirEscaping is the regression for logging.dir and
// build.temp_dir being emitted verbatim into C++ (wide) string literals. A
// Windows path like `C:\temp\logs` contains backslashes that C++ would treat
// as escape sequences (`\t` -> TAB, `\l` -> invalid) — corrupting the path or
// breaking the build. Both must route through escapeCppString so a backslash is
// emitted as `\\` (a literal backslash in the resulting C string).
func TestGenCpp_LogAndTempDirEscaping(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Build:   config.BuildConfig{Singlefile: "xll", TempDir: `C:\tmp\extract`},
		Logging: config.LoggingConfig{Level: "info", Dir: `C:\temp\logs`},
		Functions: []config.Function{
			{Name: "Add", Return: "int", Args: []config.Arg{{Name: "a", Type: "int"}}},
		},
		Server: config.ServerConfig{Timeout: "2s", Launch: &config.LaunchConfig{Enabled: new(bool)}},
	}
	content := renderCppMain(t, cfg)

	// WHERE THE OLD ASSERTIONS WENT (2026-08-02). The log-bootstrap BODY moved
	// out of the template into the xll_log.cpp asset (xll::InitNativeLogging),
	// so two of the three literals below no longer exist as separate statements.
	// This test is still the ESCAPING gate — the literals are asserted in their
	// NEW home, which is the argument list of the single relocated call:
	//
	//	`std::wstring logDir = L"C:\\temp\\logs";`
	//	                      -> the InitNativeLogging configuredDir argument
	//	                         (below); the resolution it fed is executed by
	//	                         internal/assets/testdata/log_paths_native_test.cpp
	//	`binDir = ExpandEnvVarsW(L"C:\\tmp\\extract");`
	//	                      -> build.temp_dir now reaches the resolver through
	//	                         the SAME narrow `tempPattern` literal that
	//	                         LaunchConfig::tempDir already used, so the wide
	//	                         duplicate is gone. The narrow literal's escaping
	//	                         is still asserted, and it is now the ONLY place
	//	                         build.temp_dir is emitted — a strictly smaller
	//	                         escaping surface.
	for _, want := range []string{
		// logging.dir wide literal — backslashes escaped, path preserved.
		`L"C:\\temp\\logs", "info", "TestProj",`,
		// build.temp_dir, narrow literal (singlefile branch).
		`tempPattern = "C:\\tmp\\extract";`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("expected escaped literal %q, not found in:\n%s", want, content)
		}
	}

	// The raw (unescaped) forms must be gone: an un-escaped `\t`/`\l` would
	// corrupt the emitted literal.
	for _, bad := range []string{
		`L"C:\temp\logs"`,
		`"C:\tmp\extract"`,
	} {
		if strings.Contains(content, bad) {
			t.Errorf("found unescaped (corrupting) literal %q", bad)
		}
	}
}

// TestGenGo_LogDirEscaping is the Go-side twin: logging.dir flows into
// server.InitServerLogging via %q, so a backslash Windows path is emitted as a
// valid Go string literal (`"C:\\temp\\logs"`) rather than raw text that would
// break the interpreted string literal.
//
// WHERE THE OLD ASSERTION WENT (2026-08-02): the call used to be
// `server.InitLog(...)` inside a template-rendered XLL_LOG_TO_STDOUT if/else.
// That branch moved to pkg/server (server.InitServerLogging, executed by
// pkg/server/bootstrap_test.go); the %q escaping stayed here, on the one
// argument that still carries it.
func TestGenGo_LogDirEscaping(t *testing.T) {
	t.Parallel()
	// Build the server.go data struct directly (serverDataFor hardcodes a plain
	// logging.dir); we need our backslash path to flow through.
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
		Functions: []config.Function{
			{Name: "Add", Return: "int", Args: []config.Arg{{Name: "a", Type: "int"}}},
		},
		Version: "test",
		Logging: config.LoggingConfig{Level: "info", Dir: `C:\temp\logs`},
	}
	srv := renderTemplate(t, "server.go.tmpl", data)
	// The Go render must parse — an un-escaped backslash path would not.
	assertParses(t, "server.go", srv)

	want := `server.InitServerLogging("C:\\temp\\logs", "info", "TestProj")`
	if !strings.Contains(srv, want) {
		t.Errorf("server.go must emit logging.dir via %%q: expected %q:\n%s", want, srv)
	}
}

// TestGenCpp_RtdFloatTopicRoundTrip is the regression for float/date RTD topic
// strings being formatted with std::to_wstring (6-digit %f), which truncates
// precision and collides distinct scalar arguments onto one topic string (and,
// for rtd-once, one memoize/once-key). The wrapper must use the %.17g
// round-trip helper FormatDoubleRoundTrip instead.
//
// WHERE THE OLD ASSERTIONS WENT (2026-08-03). FormatDoubleRoundTrip had no
// template variables in it, so it moved to include/xll_topic.h and this test
// keeps only the WIRING half:
//
//	`L"%.17g"` in the rendered template            -> internal/assets/topic_cpp_test.go::TestFormatDoubleRoundTripUsesShortestRoundTrip
//	`std::wstring FormatDoubleRoundTrip(double v)` -> ditto (definition + inline linkage)
//	the four call sites + the lossy-path ban       -> STAY HERE (they are per-argument codegen)
//	                                                  plus the #include that makes them resolve
func TestGenCpp_RtdFloatTopicRoundTrip(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Rtd:     config.RtdConfig{Enabled: true, ProgID: "TestProj.RTD"},
		Functions: []config.Function{
			{Name: "FloatTick", Mode: "rtd", Return: "float",
				Args: []config.Arg{{Name: "f", Type: "float"}}},
			{Name: "FloatOnce", Mode: "rtd-once", Return: "float",
				Args: []config.Arg{{Name: "g", Type: "float"}}},
		},
		Server: config.ServerConfig{Timeout: "2s", Launch: &config.LaunchConfig{Enabled: new(bool)}},
	}
	content := renderCppMain(t, cfg)

	// The helper is reached through the asset header (unqualified, via the
	// file's `using namespace xll;`). Without the include the four call sites
	// below do not compile — this is the whole wiring contract.
	if !strings.Contains(content, `#include "xll_topic.h"`) {
		t.Errorf("xll_main.cpp must include xll_topic.h (FormatDoubleRoundTrip lives there):\n%s", content)
	}
	// Do-not-re-inline guard, in the spirit of AGENTS.md §18.6.1's
	// TestChunkSegmentLogicIsExtracted: a re-emitted copy in the template would
	// shadow the asset, leave the asset test green, and put untested code back in
	// the shipped XLL.
	code := stripCppComments(content)
	for _, gone := range []string{
		"static std::wstring FormatDoubleRoundTrip(",
		`L"%.17g"`,
	} {
		if strings.Contains(code, gone) {
			t.Errorf("xll_main.cpp re-inlines the relocated topic formatter (%q); it must live ONLY in "+
				"include/xll_topic.h", gone)
		}
	}

	// Both the rtd and rtd-once float args must use the round-trip helper.
	if !strings.Contains(content, "FormatDoubleRoundTrip(f)") {
		t.Errorf("rtd float arg must use FormatDoubleRoundTrip:\n%s", content)
	}
	if !strings.Contains(content, "FormatDoubleRoundTrip(g)") {
		t.Errorf("rtd-once float arg must use FormatDoubleRoundTrip:\n%s", content)
	}

	// The lossy %f path on those float args must be gone.
	for _, bad := range []string{"std::to_wstring(f)", "std::to_wstring(g)"} {
		if strings.Contains(content, bad) {
			t.Errorf("float RTD topic must not use lossy std::to_wstring: found %q", bad)
		}
	}
}

// TestGenCpp_RtdProgIDDescriptionEscaping is the regression for rtd.prog_id /
// rtd.description being emitted verbatim into C++ wide-string literals. A
// description carrying a quote or backslash (free text — validation only
// rejects control chars/quotes/backslash/whitespace in the prog_id, NOT the
// description) would terminate or corrupt the literal. Both the ProgID and the
// FriendlyName (description) globals — and the two xlfRtd wrapper wProgID
// literals — must route through escapeCppString. Self-policy: all free text is
// escaped (gen_cpp_test.go convention).
func TestGenCpp_RtdProgIDDescriptionEscaping(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Rtd: config.RtdConfig{
			Enabled:     true,
			ProgID:      "TestProj.RTD",
			Description: `He said "hi"\go`,
			Clsid:       "{11111111-2222-3333-4444-555555555555}",
		},
		Functions: []config.Function{
			{Name: "Tick", Mode: "rtd", Return: "float",
				Args: []config.Arg{{Name: "s", Type: "string"}}},
		},
		Server: config.ServerConfig{Timeout: "2s", Launch: &config.LaunchConfig{Enabled: new(bool)}},
	}
	content := renderCppMain(t, cfg)

	// The description's interior quotes and backslash must be escaped so the
	// emitted literal stays well-formed.
	want := `const wchar_t* g_szFriendlyName = L"He said \"hi\"\\go";`
	if !strings.Contains(content, want) {
		t.Errorf("expected escaped FriendlyName literal %q, not found in:\n%s", want, content)
	}
	// The raw (corrupting) form must be gone.
	if strings.Contains(content, `L"He said "hi"\go"`) {
		t.Errorf("found unescaped (corrupting) description literal")
	}
	// ProgID globals + wrapper wProgID all route through escapeCppString; for a
	// clean ProgID the bytes are unchanged, so assert it is present intact.
	if !strings.Contains(content, `L"TestProj.RTD"`) {
		t.Errorf("expected ProgID literal L\"TestProj.RTD\" in:\n%s", content)
	}
}

// TestConfig_RejectsControlCharsInDirs pins the validation guard: a control
// character (e.g. an embedded NUL) in logging.dir or build.temp_dir is rejected
// at config time, before it can reach a generated C++ literal or the filesystem.
func TestConfig_RejectsControlCharsInDirs(t *testing.T) {
	t.Parallel()
	base := func() *config.Config {
		return &config.Config{
			Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
			Functions: []config.Function{
				{Name: "Add", Return: "int", Args: []config.Arg{{Name: "a", Type: "int"}}},
			},
			Server: config.ServerConfig{Timeout: "2s"},
		}
	}

	t.Run("logging.dir NUL rejected", func(t *testing.T) {
		cfg := base()
		cfg.Logging.Dir = "logs\x00evil"
		if err := config.Validate(cfg); err == nil {
			t.Error("expected control character in logging.dir to be rejected")
		}
	})

	t.Run("build.temp_dir control char rejected", func(t *testing.T) {
		cfg := base()
		cfg.Build.TempDir = "tmp\x01evil"
		if err := config.Validate(cfg); err == nil {
			t.Error("expected control character in build.temp_dir to be rejected")
		}
	})

	t.Run("clean Windows paths accepted", func(t *testing.T) {
		cfg := base()
		cfg.Logging.Dir = `C:\temp\logs`
		cfg.Build.TempDir = `C:\tmp\extract`
		if err := config.Validate(cfg); err != nil {
			t.Errorf("clean backslash paths must validate: %v", err)
		}
	})
}
