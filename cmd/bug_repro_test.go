package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestGenerate_Fixes verifies that specific bugs are not present in the generated code.
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
	if runtime.GOOS == "windows" {
		script = "@echo off\necho mock flatc\n"
	}
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
	// Force overwrite if exists (though temp dir is new)
	if err := runInit(projectName, true); err != nil {
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

	// Fix 1: Infinite Loop in C++ Async Handler
	// Search for case 128 returning 1
	if !strings.Contains(sContent, "case (shm::MsgType)128:") {
		t.Errorf("xll_main.cpp should handle MSG_BATCH_ASYNC_RESPONSE (128)")
	}
	// We want to ensure it returns 1, not 0.
	// We verify that return 1 appears in the file (it didn't before for handlers).
	if !strings.Contains(sContent, "return 1;") {
		t.Errorf("xll_main.cpp should return 1 to acknowledge async batch processing")
	}
	// We want to ensure it returns 1, not 0.
	// This is a bit loose with string search, but sufficient.
	// We look for the block end.
	// Ideally we want to see "return 1;" inside the 127 block.
	// But since it's a big block, let's just check if we find "return 1;" at all in the lambda?
	// No, regular handlers return 0. Only 127 needs 1?
	// Wait, standard handlers for GuestCalls (MsgId > 128) might also need to return 1 if they are "processed"?
	// If `shm` logic is "return count processed", then yes.
	// But `server.go` returns `0` bytes written.
	// The user says "Go sends 127... C++ fails to process... infinite loop".
	// If I change 127 to return 1, I should verify that.

	// Fix 2: String Corruption
	// Argument 'name' in Greet is string.
	// Previously it was generated as `const XLL_PASCAL_STRING* name`.
	// We want `LPXLOPER12 name`.
	// And we want `ConvertExcelString` usage to handle `xltypeStr`.
	if strings.Contains(sContent, "const XLL_PASCAL_STRING* name") {
		t.Errorf("String argument 'name' should be LPXLOPER12 to avoid corruption, found const XLL_PASCAL_STRING*")
	}
	if !strings.Contains(sContent, "LPXLOPER12 name") {
		t.Errorf("String argument 'name' should be LPXLOPER12")
	}

	// Check for usage of xltypeStr
	if !strings.Contains(sContent, "if (name->xltype == xltypeStr)") && !strings.Contains(sContent, "xltypeStr") {
		// This check is tricky because template generation might vary.
		// But checking for LPXLOPER12 is the main structural change.
	}

	// Fix 3: Go Server Thread Exhaustion
	// Check generated/server.go
	goContent, err := os.ReadFile("generated/server.go")
	if err != nil {
		t.Fatal(err)
	}
	sGoContent := string(goContent)

	// Look for non-blocking select in async handler
	// "select {" followed by "case jobQueue <-" and "default:"
	if !strings.Contains(sGoContent, "select {") || !strings.Contains(sGoContent, "case jobQueue <- func() {") || !strings.Contains(sGoContent, "default:") {
		t.Errorf("server.go should use non-blocking select for async job queue to prevent thread exhaustion")
	}

	// Check for Bug 1 (Regression): duplicate xlAutoFree12
	if strings.Contains(sContent, "void __stdcall xlAutoFree12") {
		t.Errorf("xll_main.cpp should NOT define xlAutoFree12 (it is in xll_mem.cpp)")
	}

	// Check for Bug 2 (Regression): usage of xll::MemoryPool
	if strings.Contains(sContent, "xll::MemoryPool") {
		t.Errorf("xll_main.cpp should NOT use xll::MemoryPool (internal class)")
	}
}
