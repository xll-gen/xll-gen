package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"xll-gen/internal/config"
)

func TestGenCpp_StringErrorReturn(t *testing.T) {
	// Setup temp dir
	tmpDir, err := os.MkdirTemp("", "bug_repro")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config with a string return function
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{
				Name:   "TestStr",
				Return: "string",
				Args:   []config.Arg{},
			},
            {
				Name:   "TestInt",
				Return: "int",
				Args:   []config.Arg{},
			},
            {
				Name:   "TestAny",
				Return: "any",
				Args:   []config.Arg{},
			},
            {
				Name:   "TestGrid",
				Return: "grid",
				Args:   []config.Arg{},
			},
		},
        Server: config.ServerConfig{
            Timeout: "2s",
        },
	}

	// Generate xll_main.cpp
    // generateCppMain(cfg *config.Config, dir string, shouldAppendPid bool) error
	if err := generateCppMain(cfg, tmpDir, false); err != nil {
		t.Fatalf("generateCppMain failed: %v", err)
	}

	// Read generated file
	contentBytes, err := os.ReadFile(filepath.Join(tmpDir, "xll_main.cpp"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(contentBytes)

	// Verify TestStr error return
    // We expect: if (!respBuf) return &g_xlErrValue;

    expectedFix := "if (!respBuf) return &g_xlErrValue;"
    if !strings.Contains(content, expectedFix) {
        t.Logf("Generated content:\n%s", content)
        t.Fatalf("Could not find expected fix pattern: '%s'", expectedFix)
    }

    // Check TestAny and TestGrid too
    expectedFixAny := "if (!respBuf) return &g_xlErrValue;"
    if strings.Count(content, expectedFixAny) < 3 {
         t.Fatalf("Expected at least 3 occurrences of return &g_xlErrValue (string, any, grid)")
    }

    // Check TestInt should return 0
    // "if (!respBuf) return 0;"
    // We need to be careful with context matching, but simplistic check helps
    if !strings.Contains(content, "if (!respBuf) return 0;") {
         t.Fatalf("Expected int return 0 on error")
    }

    // Check if g_xlErrValue is defined
    if !strings.Contains(content, "static XLOPER12 g_xlErrValue;") {
        t.Fatalf("g_xlErrValue definition missing")
    }

    if !strings.Contains(content, "g_xlErrValue.xltype = xltypeErr;") {
        t.Fatalf("g_xlErrValue initialization missing")
    }
}

func TestGenCpp_NullableReturn(t *testing.T) {
	// Setup temp dir
	tmpDir, err := os.MkdirTemp("", "bug_repro_nullable")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config with a int? return function
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{
				Name:   "TestNullableInt",
				Return: "int?",
				Args:   []config.Arg{},
			},
			{
				Name:   "TestNullableIntAsync",
				Return: "int?",
				Args:   []config.Arg{},
				Async:  true,
			},
		},
		Server: config.ServerConfig{
			Timeout: "2s",
		},
	}

	// Generate xll_main.cpp
	if err := generateCppMain(cfg, tmpDir, false); err != nil {
		t.Fatalf("generateCppMain failed: %v", err)
	}

	// Read generated file
	contentBytes, err := os.ReadFile(filepath.Join(tmpDir, "xll_main.cpp"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(contentBytes)

	// Check 1: Return type in C++ signature
	// Expected: __declspec(dllexport) LPXLOPER12 __stdcall TestNullableInt
	if strings.Contains(content, "__declspec(dllexport) LPXLOPER12 __stdcall TestNullableInt") {
		t.Log("Confirmed: Correct C++ return type 'LPXLOPER12' generated")
	} else {
		t.Errorf("Expected C++ signature LPXLOPER12, got something else.")
	}

	// Check 2: xlfRegister string
	// Expected: TempStr12(L"Q$") (assuming no args)
	if strings.Contains(content, "TempStr12(L\"Q$") {
		t.Log("Confirmed: Correct Excel Type String 'Q' generated")
	} else {
		// Find what was generated
		start := strings.Index(content, "TempStr12(L\"")
		end := start + 20
		snippet := ""
		if start != -1 && end < len(content) {
			snippet = content[start:end]
		}
		t.Errorf("Expected type string 'Q$', got something else. Snippet: %s", snippet)
	}

	// Check 3: Return statement
	// Expected: NewXLOPER12() and return xRes
	if strings.Contains(content, "NewXLOPER12()") && strings.Contains(content, "return xRes;") {
		t.Log("Confirmed: Proper XLOPER return logic used")
	} else {
		t.Errorf("Expected NewXLOPER12 and return xRes.")
	}

	// Check logic details
	if !strings.Contains(content, "xRes->xltype = xltypeInt | xlbitDLLFree;") {
		t.Errorf("Missing xltypeInt | xlbitDLLFree assignment")
	}
	if !strings.Contains(content, "xRes->xltype = xltypeNil | xlbitDLLFree;") {
		t.Errorf("Missing xltypeNil | xlbitDLLFree assignment")
	}

	// Check Async logic
	// Should use stack variable XLOPER12 xRes; and NOT NewXLOPER12() or xlbitDLLFree
	// We need to look inside the TestNullableIntAsync block (case ID + 1)
	// or search for the pattern specifically.
	// Pattern: xRes.xltype = xltypeInt; (without xlbitDLLFree)
	if !strings.Contains(content, "xRes.xltype = xltypeInt;") {
		t.Errorf("Missing stack-based xltypeInt assignment for Async")
	}
	// And verify we don't use xlbitDLLFree for async int?
	// It's hard to verify "not present" in a specific block without parsing.
	// But we can check if the specific line "xRes.xltype = xltypeInt | xlbitDLLFree;" appears TWICE (it should appear ONCE for sync).
	count := strings.Count(content, "xRes->xltype = xltypeInt | xlbitDLLFree;")
	if count != 1 {
		t.Errorf("Expected exactly 1 occurrence of 'xRes->xltype = xltypeInt | xlbitDLLFree;' (Sync only), found %d", count)
	}
}
