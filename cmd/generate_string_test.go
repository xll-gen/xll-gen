package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateStringArgRepro(t *testing.T) {
	// 1. Setup temp dir
	tempDir, err := os.MkdirTemp("", "xll-gen-string-repro")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)

	// Change to temp dir
	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}

	// 2. Create xll.yaml with a string argument function
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
	if err := os.WriteFile("xll.yaml", []byte(xllContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create dummy go.mod so getModuleName works
	if err := os.WriteFile("go.mod", []byte("module repro-string-arg\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// 3. Run Generate
	// runGenerate requires flatc. We assume it works if environment is set up.
	// We call runGenerate() from generate.go (in same package)
	if err := runGenerate(); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// 4. Verify xll_main.cpp content
	cppPath := filepath.Join("generated", "cpp", "xll_main.cpp")
	contentBytes, err := os.ReadFile(cppPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(contentBytes)

	// The buggy code:
	// auto msg_off = builder.CreateString(WStringToString(msg));
	// This assumes 'msg' is a null-terminated string, which is incorrect for D% (Pascal string).
	buggyPattern := "WStringToString(msg)"

	if strings.Contains(content, buggyPattern) {
		t.Fatalf("BUG REPRODUCED: Generated code contains incorrect string handling:\n%s\nThis treats Pascal string as null-terminated.", buggyPattern)
	}

	// If we are here, the buggy pattern is NOT found.
	// This means either the code is fixed, or the generation logic changed completely.
	// We should check for the expected correct pattern to be sure.

	// Expected fix pattern:
	// Use helper ConvertExcelString(msg)
	expectedSubstrings := []string{
		"ConvertExcelString(msg)",
		"const char* ConvertExcelString(const wchar_t* wstr)", // Helper definition
	}

	for _, s := range expectedSubstrings {
		if !strings.Contains(content, s) {
			t.Errorf("Generated code missing expected fix part: %s", s)
		}
	}
}
