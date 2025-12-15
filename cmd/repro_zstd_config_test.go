package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/generator"
)

func TestRepro_ZstdConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "repro_zstd_config")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cmd := exec.Command("go", "mod", "init", "repro_zstd")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to init go mod: %v", err)
	}

	runGen := func(t *testing.T, cfg *config.Config, name string) {
		config.ApplyDefaults(cfg)

		cwd, _ := os.Getwd()
		defer os.Chdir(cwd)
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatalf("Failed to chdir: %v", err)
		}

		opts := generator.Options{DisablePidSuffix: true}
		if err := generator.Generate(cfg, "repro_zstd", opts); err != nil {
			t.Fatalf("Generate failed: %v", err)
		}

		cmakePath := filepath.Join(tmpDir, "generated", "cpp", "CMakeLists.txt")
		content, err := os.ReadFile(cmakePath)
		if err != nil {
			t.Fatalf("Failed to read CMakeLists.txt: %v", err)
		}

		if name == "EmptySinglefile" { // Disabled
			if strings.Contains(string(content), "zstd") {
				t.Errorf("CMakeLists.txt should NOT contain 'zstd' when singlefile is empty, but it does.")
			}
		} else { // "XllSinglefile" -> Enabled
			if !strings.Contains(string(content), "zstd") {
				t.Errorf("CMakeLists.txt SHOULD contain 'zstd' when singlefile is 'xll', but it does not.")
			}
		}
	}

	// Case 1: Singlefile is "xll" -> Should HAVE zstd
	t.Run("XllSinglefile", func(t *testing.T) {
		cfg := &config.Config{
			Project: config.ProjectConfig{Name: "TestProjZstd", Version: "0.1.0"},
			Build: config.BuildConfig{
				Singlefile: "xll",
			},
			Logging: config.LoggingConfig{Level: "info"},
		}
		runGen(t, cfg, "XllSinglefile")
	})

	// Case 2: Singlefile is empty (Explicitly disabled or default if not set) -> Should NOT have zstd
	t.Run("EmptySinglefile", func(t *testing.T) {
		cfg := &config.Config{
			Project: config.ProjectConfig{Name: "TestProjNoZstd", Version: "0.1.0"},
			Build: config.BuildConfig{
				Singlefile: "", // Empty
			},
			Logging: config.LoggingConfig{Level: "info"},
		}
		runGen(t, cfg, "EmptySinglefile")
	})
}
