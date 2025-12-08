package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestFlatbuffersVersionConsistency ensures that the flatc version pinned in the Go code
// matches the version defined in the CMake template.
func TestFlatbuffersVersionConsistency(t *testing.T) {
	// 1. Extract version from internal/generator/flatc.go
	// Since we are running from cmd/, we need to go up one level
	flatcPath := filepath.Join("..", "internal", "generator", "flatc.go")
	flatcBytes, err := os.ReadFile(flatcPath)
	if err != nil {
		t.Fatal(err)
	}
	flatcContent := string(flatcBytes)

	// Look for const flatcVersion = "v..."
	reFlatc := regexp.MustCompile(`const flatcVersion = "(v[0-9]+\.[0-9]+\.[0-9]+)"`)
	matches := reFlatc.FindStringSubmatch(flatcContent)
	if len(matches) < 2 {
		t.Fatalf("Could not find const flatcVersion in %s", flatcPath)
	}
	goVersion := matches[1]
	t.Logf("Found Go flatc version: %s", goVersion)

	// 2. Extract version from internal/templates/CMakeLists.txt.tmpl
	cmakePath := filepath.Join("..", "internal", "templates", "CMakeLists.txt.tmpl")
	cmakeBytes, err := os.ReadFile(cmakePath)
	if err != nil {
		t.Fatal(err)
	}
	cmakeContent := string(cmakeBytes)

	// Look for GIT_TAG v...
	reCmake := regexp.MustCompile(`GIT_TAG\s+(v[0-9]+\.[0-9]+\.[0-9]+)`)
	matchesCmake := reCmake.FindStringSubmatch(cmakeContent)
	if len(matchesCmake) < 2 {
		t.Fatalf("Could not find GIT_TAG in %s", cmakePath)
	}
	cmakeVersion := matchesCmake[1]
	t.Logf("Found CMake GIT_TAG version: %s", cmakeVersion)

	// 3. Compare
	if goVersion != cmakeVersion {
		t.Errorf("Version mismatch! Go: %s, CMake: %s", goVersion, cmakeVersion)
	}
}
