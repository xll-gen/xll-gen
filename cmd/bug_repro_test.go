package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setupMockFlatc creates a dummy flatc binary in the temp dir and adds it to PATH.
func setupMockFlatc(t *testing.T, tempDir string) {
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
    // We rely on test process cleanup or subsequent tests overwriting PATH if needed.
    // In a single run, this is acceptable.
}

// TestGenerate_Fixes verifies that specific bugs are not present in the generated code.
func TestGenerate_Fixes(t *testing.T) {
	// 1. Setup temp dir
	tempDir, err := os.MkdirTemp("", "xll-bug-repro")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

    setupMockFlatc(t, tempDir)

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
	if !strings.Contains(sContent, "case (shm::MsgType)128:") {
		t.Errorf("xll_main.cpp should handle MSG_BATCH_ASYNC_RESPONSE (128)")
	}
	if !strings.Contains(sContent, "return 1;") {
		t.Errorf("xll_main.cpp should return 1 to acknowledge async batch processing")
	}

	// Fix 2: String Corruption
	if strings.Contains(sContent, "const XLL_PASCAL_STRING* name") {
		t.Errorf("String argument 'name' should be LPXLOPER12 to avoid corruption, found const XLL_PASCAL_STRING*")
	}
	if !strings.Contains(sContent, "LPXLOPER12 name") {
		t.Errorf("String argument 'name' should be LPXLOPER12")
	}

	// Fix 3: Go Server Thread Exhaustion
	goContent, err := os.ReadFile("generated/server.go")
	if err != nil {
		t.Fatal(err)
	}
	sGoContent := string(goContent)

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

// TestRepro_MemoryLeak verifies that memory leak fixes are present in the generated code.
// It acts as a static analysis tool on the generated C++ files.
func TestRepro_MemoryLeak(t *testing.T) {
	// 1. Setup temp dir
	tempDir, err := os.MkdirTemp("", "xll-mem-repro")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	setupMockFlatc(t, tempDir)

	// Change WD
	origWd, _ := os.Getwd()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	// 2. Init
	projectName := "mem_check"
	if err := runInit(projectName, true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// 3. Generate
	if err := os.Chdir(projectName); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}

	if err := runGenerate(); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// 4. Verify xll_mem.cpp (xlAutoFree12 leak)
	memCppPath := filepath.Join("generated", "cpp", "include", "xll_mem.cpp")
	memContent, err := os.ReadFile(memCppPath)
	if err != nil {
		t.Fatalf("Could not read xll_mem.cpp: %v", err)
	}
	sMem := string(memContent)

	if !strings.Contains(sMem, "xltypeRef") {
		t.Errorf("xll_mem.cpp: xlAutoFree12 does not handle xltypeRef (Memory Leak)")
	}
	if !strings.Contains(sMem, "delete[] (char*)p->val.mref.lpmref") && !strings.Contains(sMem, "delete[] (char*)") {
		t.Errorf("xll_mem.cpp: xlAutoFree12 does not correctly delete lpmref as char array")
	}

	// 5. Verify xll_converters.cpp (AnyToXLOPER12 leaks and missing features)
	convCppPath := filepath.Join("generated", "cpp", "include", "xll_converters.cpp")
	convContent, err := os.ReadFile(convCppPath)
	if err != nil {
		t.Fatalf("Could not read xll_converters.cpp: %v", err)
	}
	sConv := string(convContent)

	if strings.Contains(sConv, "x->xltype = xltypeInt;") {
		t.Errorf("xll_converters.cpp: AnyToXLOPER12 sets xltypeInt without xlbitDLLFree (Struct Leak)")
	}
	if strings.Contains(sConv, "x->xltype = xltypeNum;") {
		t.Errorf("xll_converters.cpp: AnyToXLOPER12 sets xltypeNum without xlbitDLLFree (Struct Leak)")
	}
	if strings.Contains(sConv, "x->xltype = xltypeBool;") {
		t.Errorf("xll_converters.cpp: AnyToXLOPER12 sets xltypeBool without xlbitDLLFree (Struct Leak)")
	}
	if strings.Contains(sConv, "x->xltype = xltypeErr;") {
		t.Errorf("xll_converters.cpp: AnyToXLOPER12 sets xltypeErr without xlbitDLLFree (Struct Leak)")
	}

	if strings.Contains(sConv, "x->xltype = xltypeSRef;") {
		t.Errorf("xll_converters.cpp: RangeToXLOPER12 sets xltypeSRef without xlbitDLLFree (Struct Leak)")
	}

	if !strings.Contains(sConv, "case ipc::types::AnyValue_Range:") {
		t.Errorf("xll_converters.cpp: AnyToXLOPER12 missing AnyValue_Range case")
	}

	// 6. Verify xll_async.cpp (Range leak in manual cleanup)
	asyncCppPath := filepath.Join("generated", "cpp", "include", "xll_async.cpp")
	asyncContent, err := os.ReadFile(asyncCppPath)
	if err != nil {
		t.Fatalf("Could not read xll_async.cpp: %v", err)
	}
	sAsync := string(asyncContent)

	if !strings.Contains(sAsync, "v.xltype & xltypeRef") {
		t.Errorf("xll_async.cpp: Cleanup loop does not handle xltypeRef (Memory Leak)")
	}
}
