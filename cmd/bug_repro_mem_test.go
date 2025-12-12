package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRepro_MemoryLeak verifies that memory leak fixes are present in the generated code.
// It acts as a static analysis tool on the generated C++ files.
func TestRepro_MemoryLeak(t *testing.T) {
	// 1. Setup temp dir
	tempDir, err := os.MkdirTemp("", "xll-mem-repro")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Setup mock flatc
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

	// 3. Generate (to populate generated/cpp/include)
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
	// We check for casting to char* because it was allocated as new char[]
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

	// Check AnyToXLOPER12 scalars for xlbitDLLFree
	// We check for exact bad patterns.
	// Note: spaces might vary, so we can be a bit lenient or check for lack of xlbitDLLFree.
	// We want to verify that xltypeInt is ALWAYS accompanied by | xlbitDLLFree in AnyToXLOPER12 context.
	// But simple string search is fragile.
	// Let's check specifically inside the function if we can.
	// For now, looking for "x->xltype = xltypeInt;" (literal) vs "x->xltype = xltypeInt | xlbitDLLFree;"
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

	// Check RangeToXLOPER12 SRef for xlbitDLLFree
	if strings.Contains(sConv, "x->xltype = xltypeSRef;") {
		t.Errorf("xll_converters.cpp: RangeToXLOPER12 sets xltypeSRef without xlbitDLLFree (Struct Leak)")
	}

	// Check AnyToXLOPER12 for Range support
	if !strings.Contains(sConv, "case ipc::types::AnyValue_Range:") {
		t.Errorf("xll_converters.cpp: AnyToXLOPER12 missing AnyValue_Range case")
	}
}

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
	// Note: We cannot defer os.Setenv(PATH, oldPath) safely in this scope because it affects global state
	// but since we are running in a test process that exits, or inside a sandbox, it might be okay.
	// Ideally we set it only for the duration.
	// But here we just set it.
}
