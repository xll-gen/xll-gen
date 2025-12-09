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
    // We expect: if (!slot->Send(...)) return &g_xlErrValue;
    // Note: The timeout value 2000 is hardcoded in the test config or template default

    // expectedFix := "if (!slot->Send(builder.GetSize(), 132, 2000)) {\n        return &g_xlErrValue;\n    }"
    // Normalize newlines for robust checking
    content = strings.ReplaceAll(content, "\r\n", "\n")
    if !strings.Contains(content, "if (!slot->Send(builder.GetSize(), 132, 2000)) {\n        return &g_xlErrValue;\n    }") {
        t.Logf("Generated content:\n%s", content)
        t.Fatalf("Could not find expected fix pattern for string return")
    }

    // Check TestInt should return 0
    // "if (!slot->Send(...)) {\n        return 0;\n    }"
    if !strings.Contains(content, "if (!slot->Send(builder.GetSize(), 133, 2000)) {\n        return 0;\n    }") {
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
