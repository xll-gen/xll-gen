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
	// We need to ensure flatc is available or mock it.
	// EnsureFlatc handles download, so it should work if internet is available (sandbox usually has it).
	// But generated/ipc/Request.go depends on flatc execution.

	if err := runGenerate(); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// 4. Verify files
	expected := []string{
		"generated/schema.fbs",
		"generated/interface.go",
		"generated/server.go",
		"generated/cpp/xll_main.cpp",
		"generated/cpp/include/IPCHost.h", // From assets
	}

	// Check for flatbuffer generated files.
	// Based on --go-namespace ipc, flatc generates in generated/ipc/
	// Function name "Add" -> "AddRequest.go", "AddResponse.go"
	fbFiles := []string{
		"generated/ipc/AddRequest.go",
		"generated/ipc/AddResponse.go",
		"generated/ipc/GetPriceRequest.go",
	}

	for _, f := range append(expected, fbFiles...) {
		if _, err := os.Stat(f); os.IsNotExist(err) {
			t.Errorf("File missing: %s", f)
		}
	}
}
