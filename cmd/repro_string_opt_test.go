package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"xll-gen/internal/config"
	"xll-gen/internal/generator"
)

func TestReproStringOpt(t *testing.T) {
	// 1. Setup config
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "Repro"},
		Functions: []config.Function{
			{
				Name: "TestOpt",
				Args: []config.Arg{{Name: "s", Type: "string?"}},
				Return: "string",
			},
		},
		Gen: config.GenConfig{
			Go: config.GoConfig{Package: "generated"},
		},
	}

	// 2. Setup Temp Dir
	tempDir, err := os.MkdirTemp("", "repro_bug")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	origWd, _ := os.Getwd()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	// 3. Generate
    // We suppress stdout to avoid noise
    oldStdout := os.Stdout
    os.Stdout = nil
    defer func() { os.Stdout = oldStdout }()

	if err := generator.Generate(cfg, "repro", generator.Options{}); err != nil {
		t.Fatal(err)
	}
    os.Stdout = oldStdout

	// 4. Verify xll_main.cpp
	content, err := os.ReadFile(filepath.Join(tempDir, "generated", "cpp", "xll_main.cpp"))
	if err != nil {
		t.Fatal(err)
	}
	sContent := string(content)

	// Expect Correct Behavior (Fix)
	// Arg type should be LPXLOPER12
	if !strings.Contains(sContent, "LPXLOPER12 s") {
		t.Errorf("Expected 'LPXLOPER12 s', got code with arguments: %s", extractArgs(sContent))
	}
    // Check registration string. Return Q, Arg Q (for string?), ThreadSafe $ -> "QQ$"
    // lookupArgXllType("string?") should return "Q".
    // lookupXllType("string") returns "Q".
    // So "QQ$"
	if !strings.Contains(sContent, `TempStr12(L"QQ$")`) {
		t.Errorf("Expected 'TempStr12(L\"QQ$\")', found in code")
	}

    // Check for xltypeMissing check
    if !strings.Contains(sContent, "xltypeMissing") {
        t.Errorf("Expected check for xltypeMissing")
    }
}

func extractArgs(s string) string {
    // fast hack to find the function signature
    idx := strings.Index(s, "__stdcall TestOpt(")
    if idx == -1 { return "TestOpt not found" }
    end := strings.Index(s[idx:], ")")
    return s[idx : idx+end+1]
}
