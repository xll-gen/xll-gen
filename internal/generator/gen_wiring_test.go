package generator

// Batch-3 regressions: the two formerly recognized-but-unwired config keys.
//
//   - gen.go.package: names BOTH the generated Go package and the output
//     directory / import-path segment (config.Config.GoPackage). Default
//     "generated" must stay byte-identical (pinned by TestGolden).
//   - server.launch.command / server.launch.cwd: flow into the generated C++
//     LaunchConfig (cfg.command / cfg.cwd); placeholder expansion lives in the
//     asset internal/assets/files/src/xll_launch.cpp (ResolveServerPath).
//     Precedence: server.launch.command wins over the legacy top-level
//     server.command.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/assets"
	"github.com/xll-gen/xll-gen/internal/config"
)

// TestGenGo_PackageWiring renders interface.go, server.go and Taskfile.yml with
// a non-default gen.go.package and asserts the package clause, the ipc import
// path segment, and the Taskfile paths all agree.
func TestGenGo_PackageWiring(t *testing.T) {
	t.Parallel()

	render := func(t *testing.T, pkg string) (iface, srv, task string) {
		t.Helper()
		dir := t.TempDir()
		cfg := &config.Config{
			Project: config.ProjectConfig{Name: "PkgProj", Version: "0.1"},
			Gen:     config.GenConfig{Go: config.GoConfig{Package: pkg}},
			Functions: []config.Function{
				{Name: "Add", Return: "int", Args: []config.Arg{{Name: "a", Type: "int"}}},
			},
			Server: config.ServerConfig{Timeout: "2s", Launch: &config.LaunchConfig{Enabled: boolPtr(true)}},
		}
		if err := generateInterface(cfg, dir, "mymod"); err != nil {
			t.Fatalf("generateInterface: %v", err)
		}
		if err := generateServer(cfg, dir, "mymod"); err != nil {
			t.Fatalf("generateServer: %v", err)
		}
		if err := generateTaskfile(cfg, dir); err != nil {
			t.Fatalf("generateTaskfile: %v", err)
		}
		read := func(name string) string {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			return string(b)
		}
		return read("interface.go"), read("server.go"), read("Taskfile.yml")
	}

	t.Run("custom package", func(t *testing.T) {
		iface, srv, task := render(t, "xllapi")
		// Note: templates are CRLF, so match without a trailing \n.
		if !strings.Contains(iface, "package xllapi") {
			t.Error("interface.go must carry the configured package clause")
		}
		if !strings.Contains(srv, "package xllapi") {
			t.Error("server.go must carry the configured package clause")
		}
		if !strings.Contains(srv, `"mymod/xllapi/ipc"`) {
			t.Error("server.go must import the ipc subpackage under the configured segment")
		}
		if strings.Contains(srv, `"mymod/generated/ipc"`) {
			t.Error("server.go must not retain the hardcoded generated/ipc import")
		}
		if !strings.Contains(task, "cmake -S xllapi/cpp") {
			t.Error("Taskfile must configure cmake against <package>/cpp")
		}
		if !strings.Contains(task, "remove_directory xllapi") {
			t.Error("Taskfile clean must remove the configured directory")
		}
		if strings.Contains(task, "generated/cpp") {
			t.Error("Taskfile must not retain the hardcoded generated/cpp path")
		}
	})

	t.Run("default is generated", func(t *testing.T) {
		// Empty Package (callers that skip ApplyDefaults) must keep the
		// historical output — the byte-level pin is TestGolden; this is the
		// spot-check.
		iface, srv, task := render(t, "")
		if !strings.Contains(iface, "package generated") || !strings.Contains(srv, "package generated") {
			t.Error("empty gen.go.package must default to package generated")
		}
		if !strings.Contains(srv, `"mymod/generated/ipc"`) {
			t.Error("empty gen.go.package must default the ipc import to generated/ipc")
		}
		if !strings.Contains(task, "cmake -S generated/cpp") || !strings.Contains(task, "remove_directory generated") {
			t.Error("empty gen.go.package must default the Taskfile paths to generated/")
		}
	})
}

// TestGenCpp_LaunchCommandCwdWiring asserts server.launch.command/cwd flow into
// the emitted C++ LaunchConfig, the legacy top-level server.command still
// works, and launch.command wins when both are set. Also pins the asset-side
// placeholder expansion (ResolveServerPath) the emitted values rely on.
func TestGenCpp_LaunchCommandCwdWiring(t *testing.T) {
	t.Parallel()

	base := func(launch *config.LaunchConfig, legacyCmd string) *config.Config {
		return &config.Config{
			Project: config.ProjectConfig{Name: "LaunchProj", Version: "0.1"},
			Functions: []config.Function{
				{Name: "Add", Return: "int", Args: []config.Arg{{Name: "a", Type: "int"}}},
			},
			Server: config.ServerConfig{Timeout: "2s", Command: legacyCmd, Launch: launch},
		}
	}

	t.Run("launch command and cwd wired with escaping", func(t *testing.T) {
		content := renderCppMain(t, base(&config.LaunchConfig{
			Enabled: boolPtr(true),
			Command: `"${XLL_DIR}\srv.exe" --flag`,
			Cwd:     `${XLL_DIR}\data`,
		}, ""))
		if !strings.Contains(content, `cfg.command = "\"${XLL_DIR}\\srv.exe\" --flag";`) {
			t.Error("launch.command must be emitted into cfg.command via escapeCppString")
		}
		if !strings.Contains(content, `cfg.cwd = "${XLL_DIR}\\data";`) {
			t.Error("launch.cwd must be emitted into cfg.cwd via escapeCppString")
		}
	})

	t.Run("legacy server.command still honored", func(t *testing.T) {
		content := renderCppMain(t, base(&config.LaunchConfig{Enabled: boolPtr(true)}, "legacy_server.exe"))
		if !strings.Contains(content, `cfg.command = "legacy_server.exe";`) {
			t.Error("legacy top-level server.command must still flow into cfg.command when launch.command is unset")
		}
	})

	t.Run("launch.command wins over legacy", func(t *testing.T) {
		content := renderCppMain(t, base(&config.LaunchConfig{
			Enabled: boolPtr(true),
			Command: "${BIN} --new",
		}, "legacy_server.exe"))
		if !strings.Contains(content, `cfg.command = "${BIN} --new";`) {
			t.Error("launch.command must take precedence over the legacy server.command")
		}
		if strings.Contains(content, `cfg.command = "legacy_server.exe";`) {
			t.Error("legacy command must not be emitted when launch.command is set")
		}
	})

	t.Run("defaults stay empty", func(t *testing.T) {
		content := renderCppMain(t, base(&config.LaunchConfig{Enabled: boolPtr(true)}, ""))
		if !strings.Contains(content, `cfg.command = "";`) || !strings.Contains(content, `cfg.cwd = "";`) {
			t.Error("empty launch.command/cwd must emit empty strings (C++ default resolution)")
		}
	})

	t.Run("asset expands all command placeholders", func(t *testing.T) {
		// The emitted cfg.command/cfg.cwd rely on ResolveServerPath's
		// placeholder expansion; pin the markers so the asset cannot silently
		// lose them (same marker-test discipline as gen_rtd_connect_test.go).
		m, err := assets.Assets()
		if err != nil {
			t.Fatalf("assets.Assets(): %v", err)
		}
		src, ok := m["src/xll_launch.cpp"]
		if !ok {
			t.Fatal("asset src/xll_launch.cpp not found")
		}
		for _, marker := range []string{
			`ReplaceAll(wCmd, L"${BIN_DIR}", binDir);`,
			`ReplaceAll(wCmd, L"${XLL_DIR}", xllDir);`,
			`ReplaceAll(wCmd, L"${BIN}", defaultBinPath);`,
			`ReplaceAll(wCwdCfg, L"${BIN_DIR}", binDir);`,
			`ReplaceAll(wCwdCfg, L"${XLL_DIR}", xllDir);`,
		} {
			if !strings.Contains(src, marker) {
				t.Errorf("xll_launch.cpp missing placeholder-expansion marker: %s", marker)
			}
		}
	})
}
