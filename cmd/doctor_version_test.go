package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/internal/versions"
)

// TestFlatbuffersVersionConsistency ensures that the flatc version
// matches the version variable used in the CMake template.
func TestFlatbuffersVersionConsistency(t *testing.T) {
	// 1. Extract version from internal/versions/versions.go
	versionsPath := filepath.Join("..", "internal", "versions", "versions.go")
	versionsBytes, err := os.ReadFile(versionsPath)
	if err != nil {
		t.Fatal(err)
	}
	versionsContent := string(versionsBytes)

	// Look for FlatBuffers = "v..."
	reFlatc := regexp.MustCompile(`FlatBuffers\s+=\s+"(v[0-9]+\.[0-9]+\.[0-9]+)"`)
	matches := reFlatc.FindStringSubmatch(versionsContent)
	if len(matches) < 2 {
		t.Fatalf("Could not find FlatBuffers constant in %s", versionsPath)
	}
	goVersion := matches[1]
	t.Logf("Found Go flatc version: %s", goVersion)

	// 2. Check internal/templates/CMakeLists.txt.tmpl uses the variable
	cmakePath := filepath.Join("..", "internal", "templates", "CMakeLists.txt.tmpl")
	cmakeBytes, err := os.ReadFile(cmakePath)
	if err != nil {
		t.Fatal(err)
	}
	cmakeContent := string(cmakeBytes)

	// Look for "GIT_TAG {{ .Deps.FlatBuffers }}" inside the flatbuffers block
	// We can just search for the string directly as it should be exact.
	expectedTag := "GIT_TAG {{ .Deps.FlatBuffers }}"
	if !strings.Contains(cmakeContent, expectedTag) {
		t.Errorf("CMakeLists.txt.tmpl does not use dynamic versioning. Expected to find: %q", expectedTag)
	}
}

// TestFlatbuffersVersion_TypesProvenance cross-checks that the flatc
// version recorded in the upstream `types` module matches the version
// xll-gen pins. A skew here means `types` was bumped without
// regenerating its FlatBuffers Go sources (or vice versa) — the
// generated Scalar/Any/etc. types may be wire-incompatible with what
// xll-gen's CMake fetched on the C++ side.
//
// Added in v0.3.15 alongside types v0.2.5 which introduced
// protocol.FlatcVersion.
func TestFlatbuffersVersion_TypesProvenance(t *testing.T) {
	xllGenPin := strings.TrimPrefix(versions.FlatBuffers, "v")
	if protocol.FlatcVersion != xllGenPin {
		t.Fatalf("flatc version skew: types module recorded %q but xll-gen pins %q — regenerate types/go/protocol or sync versions.FlatBuffers",
			protocol.FlatcVersion, xllGenPin)
	}
}
