package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/generator"
	"gopkg.in/yaml.v3"
)

// --- Helpers ---

// setupMockFlatc creates a dummy flatc binary in the temp dir.
// Returns the path to the binary and a cleanup function.
func setupMockFlatc(t *testing.T, tempDir string) (string, func()) {
	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}

	flatcName := "flatc"
	if runtime.GOOS == "windows" {
		flatcName += ".exe"
	}
	flatcPath := filepath.Join(binDir, flatcName)

	// Mock flatc that satisfies EnsureFlatc check if possible, or just exists.
	script := "#!/bin/sh\necho flatc version 25.9.23\n"
	if runtime.GOOS == "windows" {
		script = "@echo off\necho flatc version 25.9.23\n"
	}
	if err := os.WriteFile(flatcPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	// Make executable on unix
	if runtime.GOOS != "windows" {
		os.Chmod(flatcPath, 0755)
	}

	return flatcPath, func() {
		os.Remove(flatcPath)
	}
}

// runGenerateInDir runs the generator in the specified directory.
func runGenerateInDir(t *testing.T, dir string, opts generator.Options) {
	// Read xll.yaml
	cfgPath := filepath.Join(dir, "xll.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("failed to read xll.yaml: %v", err)
	}
	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to parse xll.yaml: %v", err)
	}
	config.ApplyDefaults(&cfg)
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("config validate failed: %v", err)
	}

	// Read go.mod for module name
	goModPath := filepath.Join(dir, "go.mod")
	modData, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatalf("failed to read go.mod: %v", err)
	}
	modName := ""
	for _, line := range strings.Split(string(modData), "\n") {
		if strings.HasPrefix(line, "module ") {
			modName = strings.TrimSpace(strings.TrimPrefix(line, "module "))
			break
		}
	}
	if modName == "" {
		t.Fatalf("module name not found in go.mod")
	}

	if err := generator.Generate(&cfg, dir, modName, opts); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
}

// setupGenTest prepares a temporary directory and runs init.
// It returns the projectDir (inside tempDir) and a cleanup function.
func setupGenTest(t *testing.T, name string) (string, func()) {
	tempDir, err := os.MkdirTemp("", "xll-test-"+name)
	if err != nil {
		t.Fatal(err)
	}

	projectDir := filepath.Join(tempDir, name)
	if err := runInit(projectDir, true, false); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	return projectDir, func() {
		os.RemoveAll(tempDir)
	}
}

func checkContent(t *testing.T, path string, mustContain []string, mustNotContain []string) {
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Could not read %s: %v", path, err)
	}
	sContent := string(content)

	for _, s := range mustContain {
		if !strings.Contains(sContent, s) {
			t.Errorf("%s missing expected content: %q", path, s)
		}
	}
	for _, s := range mustNotContain {
		if strings.Contains(sContent, s) {
			t.Errorf("%s contains forbidden content: %q", path, s)
		}
	}
}
