package cmd

import (
	"os"
	"regexp"
	"testing"
)

func TestGenerateUsesDynamicFlatcVersion(t *testing.T) {
	// 1. Setup temp dir
	tempDir, err := os.MkdirTemp("", "xll-gen-dynamic-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	origWd, _ := os.Getwd()
	os.Chdir(tempDir)
	defer os.Chdir(origWd)

	// Create dummy xll.yaml
	xllContent := `project:
  name: "test-proj"
  version: "0.1.0"
functions: []
gen:
  go:
    package: "generated"
`
	if err := os.WriteFile("xll.yaml", []byte(xllContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("go.mod", []byte("module test-proj"), 0644); err != nil {
		t.Fatal(err)
	}

	// 2. Run Generate
	// This calls EnsureFlatc -> Download -> Generate CMake
	// We assume the environment allows downloading or has cached flatc.
	if err := runGenerate(); err != nil {
		t.Fatalf("runGenerate failed: %v", err)
	}

	// 3. Check CMakeLists.txt
	cmakePath := "generated/cpp/CMakeLists.txt"
	content, err := os.ReadFile(cmakePath)
	if err != nil {
		t.Fatal(err)
	}

	s := string(content)
	// Check for GIT_TAG vX.Y.Z
	// Note: We expect 'v' prefix because getFlatcVersion/downloadFlatc ensures it.
	re := regexp.MustCompile(`GIT_TAG\s+(v[0-9]+\.[0-9]+\.[0-9]+)`)
	matches := re.FindStringSubmatch(s)
	if len(matches) < 2 {
		t.Fatalf("CMakeLists.txt does not contain valid GIT_TAG version. Content:\n%s", s)
	}

	t.Logf("Generated CMakeLists.txt uses version: %s", matches[1])
}
