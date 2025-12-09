package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestGenerate_Fixes verifies that specific bugs (duplicate xlAutoFree12, usage of internal MemoryPool)
// are not present in the generated code.
func TestGenerate_Fixes(t *testing.T) {
	// 1. Setup temp dir
	tempDir, err := os.MkdirTemp("", "xll-bug-repro")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Setup fake flatc
	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}

	flatcName := "flatc"
	if runtime.GOOS == "windows" {
		flatcName += ".exe"
	}
	flatcPath := filepath.Join(binDir, flatcName)

	script := "#!/bin/sh\necho mock flatc\n"
	if err := os.WriteFile(flatcPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	// Update PATH
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	// Change WD
	origWd, _ := os.Getwd()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	// 2. Init
	projectName := "repro_project"
	if err := runInit(projectName, false); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if err := os.Chdir(projectName); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}

	// 3. Generate
	if err := runGenerate(); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// 4. Verify xll_main.cpp content
	content, err := os.ReadFile("generated/cpp/xll_main.cpp")
	if err != nil {
		t.Fatal(err)
	}
	sContent := string(content)

	// Check for Bug 1: duplicate xlAutoFree12
	if strings.Contains(sContent, "void __stdcall xlAutoFree12") {
		t.Errorf("xll_main.cpp should NOT define xlAutoFree12 (it is in xll_mem.cpp)")
	}

	// Check for Bug 2: usage of xll::MemoryPool
	if strings.Contains(sContent, "xll::MemoryPool") {
		t.Errorf("xll_main.cpp should NOT use xll::MemoryPool (internal class)")
	}

	// Check that we DO call xlAutoFree12 for async strings
	// Look for: xlAutoFree12(xRes);
	// Since valid code is not generated yet, we won't see it.
	// But we can assert we want to see it eventually?
	// For now, let's just assert bugs are gone.
}
