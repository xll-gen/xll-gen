package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xll-gen/xll-gen/internal/generator"
)

// TestGenerate runs a full generation cycle in a temporary directory and verifies that
// all expected files are created and no legacy files exist.
func TestGenerate(t *testing.T) {
	t.Parallel()
	// 1. Setup temp dir
	tempDir, err := os.MkdirTemp("", "xll-gen-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// 2. Init
	projectName := "my-project"
	projectDir := filepath.Join(tempDir, projectName)
	if err := runInit(projectDir, false, false); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// 3. Generate
	runGenerateInDir(t, projectDir, generator.Options{})

	// 4. Verify files
	expected := []string{
		"generated/schema.fbs",
		"generated/interface.go",
		"generated/server.go",
		"generated/cpp/xll_main.cpp",
		"generated/cpp/CMakeLists.txt",
		"generated/cpp/include/xll_mem.h", // From existing assets
		"Taskfile.yml",
	}

	fbFiles := []string{
		"generated/ipc/AddRequest.go",
		"generated/ipc/AddResponse.go",
	}

	for _, f := range append(expected, fbFiles...) {
		if _, err := os.Stat(filepath.Join(projectDir, f)); os.IsNotExist(err) {
			t.Errorf("File missing: %s", f)
		}
	}

	// Verify SHM headers are NOT present (fetched via CMake)
	unexpected := []string{
		"generated/cpp/include/IPCHost.h",
		"generated/cpp/include/DirectHost.h",
	}
	for _, f := range unexpected {
		if _, err := os.Stat(filepath.Join(projectDir, f)); !os.IsNotExist(err) {
			t.Errorf("File should NOT exist: %s", f)
		}
	}
}
