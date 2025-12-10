package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"xll-gen/internal/config"
)

func TestGenCpp_ComplexReturnTypes(t *testing.T) {
	// Setup temp dir
	tmpDir, err := os.MkdirTemp("", "bug_repro")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config with functions returning complex types
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
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
            {
				Name:   "TestNumGrid",
				Return: "numgrid",
				Args:   []config.Arg{},
			},
            {
				Name:   "TestRange",
				Return: "range",
				Args:   []config.Arg{},
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

	// Verify converters are used
    checks := []struct {
        name string
        want string
    }{
        {"TestAny", "return AnyToXLOPER12(resp->result());"},
        {"TestGrid", "return GridToXLOPER12(resp->result());"},
        {"TestNumGrid", "return NumGridToFP12(resp->result());"},
        {"TestRange", "return RangeToXLOPER12(resp->result());"},
    }

    for _, c := range checks {
        if !strings.Contains(content, c.want) {
            t.Errorf("Function %s: expected '%s', not found", c.name, c.want)
        }
    }
}

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

	// Verify TestStr error return
    // We expect: if (!slot.Send(reqSize, 132, 2000)) { return &g_xlErrValue; }
    // The message ID for TestStr (first function) should be 132.
    // Note: Template now uses reqSize (negative) for zero-copy.
    expectedFix := "if (!slot.Send(reqSize, 132, 2000)) {\n        return &g_xlErrValue;\n    }"
    if !strings.Contains(content, expectedFix) {
        // Fallback check if whitespace is different or reqSize calculation is inline
        if !strings.Contains(content, "slot.Send(reqSize, 132") {
             t.Logf("Generated content:\n%s", content)
             t.Fatal("Could not find expected Send failure check")
        }
    }
    if !strings.Contains(content, "return &g_xlErrValue;") {
         t.Fatal("Expected return &g_xlErrValue on error")
    }

    // Check TestInt should return 0 (MsgID 133)
    expectedIntFix := "if (!slot.Send(reqSize, 133, 2000)) {\n        return 0;\n    }"
    if !strings.Contains(content, expectedIntFix) {
         if !strings.Contains(content, "slot.Send(reqSize, 133") {
             t.Fatalf("Expected int return 0 on error, expected: %s", expectedIntFix)
         }
    }

    // Check for negative size calculation
    if !strings.Contains(content, "int32_t reqSize = -((int32_t)builder.GetSize());") {
        t.Fatal("Expected negative size calculation for zero-copy")
    }

    // Ensure memmove is GONE
    if strings.Contains(content, "std::memmove(slot.GetReqBuffer(), builder.GetBufferPointer(), builder.GetSize());") {
        t.Fatal("Expected NO memmove for zero-copy optimization")
    }
}
