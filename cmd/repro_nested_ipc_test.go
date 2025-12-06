package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepro_NestedIPC_Corruption(t *testing.T) {
	// 1. Setup temp dir
	tempDir := t.TempDir()
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)
	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}

	// 2. Init
	projectName := "repro_project"
	if err := runInit(projectName); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if err := os.Chdir(projectName); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}

	// 3. Generate
	// runGenerate requires xll.yaml (created by runInit)
	if err := runGenerate(); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// 4. Verify xll_main.cpp content
	content, err := os.ReadFile(filepath.Join("generated", "cpp", "xll_main.cpp"))
	if err != nil {
		t.Fatal(err)
	}
	code := string(content)

	// Extract ConvertAny body
	startMarker := "flatbuffers::Offset<ipc::types::Any> ConvertAny(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder) {"
	endMarker := "// Guest Call Handler"

	idx := strings.Index(code, startMarker)
	if idx == -1 {
		t.Fatal("ConvertAny function not found in generated code")
	}

	body := code[idx:]
	endIdx := strings.Index(body, endMarker)
	if endIdx != -1 {
		body = body[:endIdx]
	}

	// Check for usage of g_host.GetZeroCopySlot() inside ConvertAny
	// This is the BUG: Using the same zero-copy slot recursively corrupts the buffer.
	if strings.Contains(body, "g_host.GetZeroCopySlot()") {
		t.Errorf("BUG DETECTED: ConvertAny uses g_host.GetZeroCopySlot(). This corrupts the shared memory buffer when called recursively during argument processing.")
	}
}
