package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRepro_NestedIPC_Corruption verifies that the nested IPC call in ConvertAny
// does not incorrectly reuse the global zero-copy slot, which would cause corruption.
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
	if err := runInit(projectName, false); err != nil {
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

	// 4. Verify ConvertAny content in include/xll_converters.cpp
	content, err := os.ReadFile(filepath.Join("generated", "cpp", "include", "xll_converters.cpp"))
	if err != nil {
		t.Fatal("Could not read xll_converters.cpp: ", err)
	}
	code := string(content)

	// Extract ConvertAny body
	startMarker := "flatbuffers::Offset<ipc::types::Any> ConvertAny(LPXLOPER12 op, flatbuffers::FlatBufferBuilder& builder) {"

	idx := strings.Index(code, startMarker)
	if idx == -1 {
		t.Fatal("ConvertAny function not found in generated xll_converters.cpp")
	}

	body := code[idx:]

	// Check for usage of g_host.GetZeroCopySlot() inside ConvertAny
	// This is the BUG: Using the same zero-copy slot recursively corrupts the buffer.
	if strings.Contains(body, "g_host.GetZeroCopySlot()") {
		t.Errorf("BUG DETECTED: ConvertAny uses g_host.GetZeroCopySlot(). This corrupts the shared memory buffer when called recursively during argument processing.")
	}

	// Ensure we ARE using g_host.Send() for safe nested IPC
	if !strings.Contains(body, "g_host.Send(") {
		t.Errorf("ConvertAny does not use g_host.Send(). It must use Send() (blocking, copying) to avoid slot corruption.")
	}
}
