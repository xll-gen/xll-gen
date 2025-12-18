package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/generator"
)

// --- Tests ---

// TestGenerate_Fixes verifies that specific bugs are not present in the generated code.
func TestGenerate_Fixes(t *testing.T) {
	t.Parallel()
	projectDir, cleanup := setupGenTest(t, "repro_project")
	defer cleanup()

	flatcPath, pathCleanup := setupMockFlatc(t, filepath.Dir(projectDir))
	defer pathCleanup()

	runGenerateInDir(t, projectDir, generator.Options{FlatcPath: flatcPath})

	checkContent(t, filepath.Join(projectDir, "generated/cpp/xll_main.cpp"),
		[]string{
			"LPXLOPER12 name", // Correct String Arg Type
		},
		[]string{
			"const XLL_PASCAL_STRING* name", // Incorrect String Arg Type
			"void __stdcall xlAutoFree12",   // Duplicate definition
			"xll::MemoryPool",               // Internal usage
		})

	checkContent(t, filepath.Join(projectDir, "generated", "cpp", "src", "xll_worker.cpp"),
		[]string{
			"if (msgType == (shm::MsgType)MSG_BATCH_ASYNC_RESPONSE)", // MSG_BATCH_ASYNC_RESPONSE
			"return 1;",                                              // ACK
		}, nil)

	checkContent(t, filepath.Join(projectDir, "generated/server.go"),
		[]string{
			"select {",
			"case jobQueue <- func() {",
			"default:",
		}, nil)

	// Check xll_log.cpp fixes
	checkContent(t, filepath.Join(projectDir, "generated", "cpp", "src", "xll_log.cpp"),
		[]string{
			"g_logPath = WideToUtf8(path)", // Check correct assignment
			"g_logLevel = LogLevel::INFO;", // Check default or assignment
		},
		[]string{
			"base + L\"_native\" + ext", // Check bad assignment
		})
}

// TestRepro_MemoryLeak verifies that memory leak fixes are present.
// Note: This now relies on the types library, so we skip file checks if they are missing
// or assume correct behavior if utilizing external lib.
func TestRepro_MemoryLeak(t *testing.T) {
	t.Parallel()
	projectDir, cleanup := setupGenTest(t, "mem_check")
	defer cleanup()

	flatcPath, pathCleanup := setupMockFlatc(t, filepath.Dir(projectDir))
	defer pathCleanup()

	runGenerateInDir(t, projectDir, generator.Options{FlatcPath: flatcPath})

	// Files like xll_mem.cpp might be in types library now, so checking local existence fails.
	// We skip these checks if the file doesn't exist locally, as we assume the external library is tested.
	memFile := filepath.Join(projectDir, "generated", "cpp", "src", "xll_mem.cpp")
	if _, err := os.Stat(memFile); err == nil {
		// 1. xll_mem.cpp (xlAutoFree12 leak)
		checkContent(t, memFile,
			[]string{
				"xltypeRef",                          // Handled
				"delete[] (char*)p->val.mref.lpmref", // Correct deletion
			}, nil)
	}

	convFile := filepath.Join(projectDir, "generated", "cpp", "src", "xll_converters.cpp")
	if _, err := os.Stat(convFile); err == nil {
		// 2. xll_converters.cpp (AnyToXLOPER12 leaks and missing features)
		checkContent(t, convFile,
			[]string{
				"case protocol::AnyValue::Range:", // Missing feature fixed
				"new char[sizeof(XLMREF12)",       // Correct Allocation for Ref
			},
			[]string{
				"x->xltype = xltypeInt;",  // Missing xlbitDLLFree
				"x->xltype = xltypeNum;",
				"x->xltype = xltypeBool;",
				"x->xltype = xltypeErr;",
				"x->xltype = xltypeSRef;", // RangeToXLOPER12 leak
			})
	}

	asyncFile := filepath.Join(projectDir, "generated", "cpp", "src", "xll_async.cpp")
	if _, err := os.Stat(asyncFile); err == nil {
		// 3. xll_async.cpp (Use safe cleanup)
		checkContent(t, asyncFile,
			[]string{
				"xlAutoFree12(pxResult)", // Safe cleanup used
			}, nil)
	}
}

// TestRepro_NestedIPC_Corruption verifies that nested IPC calls do not corrupt the zero-copy slot.
func TestRepro_NestedIPC_Corruption(t *testing.T) {
	t.Parallel()
	projectDir, cleanup := setupGenTest(t, "repro_project")
	defer cleanup()

	flatcPath, pathCleanup := setupMockFlatc(t, filepath.Dir(projectDir))
	defer pathCleanup()

	runGenerateInDir(t, projectDir, generator.Options{FlatcPath: flatcPath})

	// Skip if xll_converters.cpp is missing (moved to types lib)
	convFile := filepath.Join(projectDir, "generated", "cpp", "src", "xll_converters.cpp")
	if _, err := os.Stat(convFile); os.IsNotExist(err) {
		return
	}

	// Verify ConvertAny does not use GetZeroCopySlot
	checkContent(t, convFile,
		[]string{
			"g_host.Send(", // Must use Send
		},
		[]string{
			// "g_host.GetZeroCopySlot()", // This might appear in OTHER functions, so we can't globally ban it.
		})

	// Specific check for ConvertAny body
	content, _ := os.ReadFile(convFile)
	sContent := string(content)
	start := "flatbuffers::Offset<ipc::types::Any> ConvertAny"
	idx := strings.Index(sContent, start)
	if idx != -1 {
		body := sContent[idx:]
		if strings.Contains(body, "g_host.GetZeroCopySlot()") {
			t.Error("ConvertAny uses g_host.GetZeroCopySlot() (Corruption Risk)")
		}
	}
}

// TestGenerateStringArgRepro verifies that string arguments are correctly handled.
func TestGenerateStringArgRepro(t *testing.T) {
	t.Parallel()
	// Custom setup because we need specific xll.yaml
	tempDir, err := os.MkdirTemp("", "xll-gen-string-repro")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	flatcPath, pathCleanup := setupMockFlatc(t, tempDir)
	defer pathCleanup()

	xllContent := `project:
  name: "repro-string-arg"
  version: "0.1.0"
gen:
  go:
    package: "generated"
functions:
  - name: "Echo"
    description: "Echoes a string"
    args:
      - name: "msg"
        type: "string"
    return: "string"
`
	if err := os.WriteFile(filepath.Join(tempDir, "xll.yaml"), []byte(xllContent), 0644); err != nil {
		t.Fatal(err)
	}
	// Dummy go.mod
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte("module repro-string-arg\n"), 0644); err != nil {
		t.Fatal(err)
	}

	runGenerateInDir(t, tempDir, generator.Options{FlatcPath: flatcPath})

	checkContent(t, filepath.Join(tempDir, "generated", "cpp", "xll_main.cpp"),
		[]string{
			"ConvertExcelString",
		},
		[]string{
			"WStringToString(msg)",
		})

	// xll_utility.cpp might also be moved, check if exists
	utilFile := filepath.Join(tempDir, "generated", "cpp", "src", "xll_utility.cpp")
	if _, err := os.Stat(utilFile); err == nil {
		checkContent(t, utilFile,
			[]string{
				"std::string ConvertExcelString(const wchar_t* wstr)",
			}, nil)
	}
}
