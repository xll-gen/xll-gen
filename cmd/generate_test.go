package cmd

import (
	"os"
	"testing"
)

func TestGenerate(t *testing.T) {
	// 1. Setup temp dir
	tempDir, err := os.MkdirTemp("", "xll-gen-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	origWd, _ := os.Getwd()
	os.Chdir(tempDir)
	defer os.Chdir(origWd)

	// 2. Init
	projectName := "my-project"
	if err := runInit(projectName); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if err := os.Chdir(projectName); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}

	// 3. Generate
	if err := runGenerate(); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// 4. Verify files
	expected := []string{
		"generated/schema.fbs",
		"generated/interface.go",
		"generated/server.go",
		"generated/cpp/xll_main.cpp",
		"generated/cpp/CMakeLists.txt",
		"generated/cpp/include/xll_mem.h", // From existing assets
	}

	fbFiles := []string{
		"generated/ipc/AddRequest.go",
		"generated/ipc/AddResponse.go",
	}

	for _, f := range append(expected, fbFiles...) {
		if _, err := os.Stat(f); os.IsNotExist(err) {
			t.Errorf("File missing: %s", f)
		}
	}

	// Verify SHM headers are NOT present (fetched via CMake)
	unexpected := []string{
		"generated/cpp/include/IPCHost.h",
		"generated/cpp/include/DirectHost.h",
	}
	for _, f := range unexpected {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("File should NOT exist: %s", f)
		}
	}
}
