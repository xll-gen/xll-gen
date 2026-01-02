package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"github.com/xll-gen/xll-gen/internal/config"
)

func TestGenCpp_ComplexReturnTypes(t *testing.T) {
	t.Parallel()
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
            Launch: &config.LaunchConfig{Enabled: new(bool)}, // Default false is fine, just needs to be non-nil
        },
	}
    *cfg.Server.Launch.Enabled = true

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
	t.Parallel()
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
            Launch: &config.LaunchConfig{Enabled: new(bool)},
        },
	}
    *cfg.Server.Launch.Enabled = true

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

	// Verify TestStr error return (MsgID 140)
    // Expect: auto res = slot.Send(-((int)builder.GetSize()), (shm::MsgType)140, ...);
    // Note: The template now uses slot.Send with specific arguments.
    // The exact string might vary slightly due to template logic (e.g. casting).
    // Let's check for the key elements.
    if !strings.Contains(content, "slot.Send(-((int)builder.GetSize()), (shm::MsgType)140") {
         t.Fatal("Could not find expected slot.Send call for TestStr (MsgId 140)")
    }

    // Expect: if (res.HasError())
    if !strings.Contains(content, "if (res.HasError())") {
         t.Fatal("Could not find expected HasError check")
    }

    // Verify TestInt error return (MsgID 141)
    if !strings.Contains(content, "slot.Send(-((int)builder.GetSize()), (shm::MsgType)141") {
         t.Fatal("Could not find expected slot.Send call for TestInt (MsgId 141)")
    }

    // Check that HasError is used at least twice (once for each function)
    if strings.Count(content, "if (res.HasError())") < 2 {
         t.Fatal("Expected at least 2 occurrences of 'if (res.HasError())'")
    }

    // Check for negative size calculation
    if !strings.Contains(content, "-((int)builder.GetSize())") {
        t.Fatal("Expected negative size calculation for zero-copy")
    }

    // Ensure memmove is GONE
    if strings.Contains(content, "std::memmove(slot.GetReqBuffer(), builder.GetBufferPointer(), builder.GetSize());") {
        t.Fatal("Expected NO memmove for zero-copy optimization")
    }
}
