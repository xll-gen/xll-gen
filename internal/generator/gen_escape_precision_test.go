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

	for _, want := range []string{
		// logging.dir wide literal — backslashes escaped, path preserved.
		`std::wstring logDir = L"C:\\temp\\logs";`,
		// build.temp_dir, narrow + wide literals (singlefile branch).
		`tempPattern = "C:\\tmp\\extract";`,
		`binDir = ExpandEnvVarsW(L"C:\\tmp\\extract");`,
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
// server.InitLog via %q, so a backslash Windows path is emitted as a valid Go
// string literal (`"C:\\temp\\logs"`) rather than raw text that would break the
// interpreted string literal.
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

	want := `server.InitLog("C:\\temp\\logs", "info", "TestProj")`
	if !strings.Contains(srv, want) {
		t.Errorf("server.go must emit logging.dir via %%q: expected %q:\n%s", want, srv)
	}
}

// TestGenCpp_RtdFloatTopicRoundTrip is the regression for float/date RTD topic
// strings being formatted with std::to_wstring (6-digit %f), which truncates
// precision and collides distinct scalar arguments onto one topic string (and,
// for rtd-once, one memoize/once-key). The wrapper must use the %.17g
// round-trip helper FormatDoubleRoundTrip instead.
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

	// The round-trip helper must be defined and carry the %.17g marker.
	if !strings.Contains(content, `L"%.17g"`) {
		t.Errorf("FormatDoubleRoundTrip helper must format with %%.17g:\n%s", content)
	}
	if !strings.Contains(content, "std::wstring FormatDoubleRoundTrip(double v)") {
		t.Errorf("FormatDoubleRoundTrip helper definition missing:\n%s", content)
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
