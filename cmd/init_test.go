package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRunInit verifies that the init command correctly scaffolds a new project with
// the expected file structure.
func TestRunInit(t *testing.T) {
	// Create a temp dir for testing
	tempDir, err := os.MkdirTemp("", "xll-gen-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Change to temp dir
	originalWd, _ := os.Getwd()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer os.Chdir(originalWd)

	projectName := "my-test-project"
	if err := runInit(projectName, false, false); err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	// Verify files exist
	expectedFiles := []string{
		"xll.yaml",
		"main.go",
		".gitignore",
		"go.mod",
	}

	for _, f := range expectedFiles {
		path := filepath.Join(projectName, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("Expected file %s not created", path)
		}
	}
}
