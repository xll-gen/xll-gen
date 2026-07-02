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

// TestRegtestCMakePinsMatchVersions guards the hardcoded GIT_TAG pins in
// internal/regtest/testdata/CMakeLists.txt against drift from
// internal/versions/versions.go. That file is a go:embed STATIC asset (the
// regtest mock-host build), so unlike the generated CMakeLists.txt.tmpl it is
// NOT templated from versions.go — the shm / types / flatbuffers tags there are
// maintained BY HAND (see the 677b331 manual bump). Without this gate a
// versions.go bump silently leaves the regtest fixture building the old
// dependency. Note the XLLGEN_TYPES_SRC / XLLGEN_SHM_SRC overrides only redirect
// the SOURCE dir; shm and flatbuffers otherwise always come from these pins.
//
// This is the 6th shared-dependency pin location (AGENTS.md §18.2).
func TestRegtestCMakePinsMatchVersions(t *testing.T) {
	cmakePath := filepath.Join("..", "internal", "regtest", "testdata", "CMakeLists.txt")
	data, err := os.ReadFile(cmakePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// Each FetchContent_Declare has "GIT_REPOSITORY .../<name>.git" followed
	// (across newlines) by "GIT_TAG <tag>". Capture the tag per known repo.
	pins := map[string]string{
		"flatbuffers": versions.FlatBuffers,
		"shm":         versions.SHM,
		"types":       versions.Types,
	}
	for repo, want := range pins {
		re := regexp.MustCompile(`/` + regexp.QuoteMeta(repo) + `\.git\s+GIT_TAG\s+(\S+)`)
		m := re.FindStringSubmatch(content)
		if len(m) < 2 {
			t.Errorf("could not find GIT_TAG for %s in %s", repo, cmakePath)
			continue
		}
		if got := m[1]; got != want {
			t.Errorf("regtest CMakeLists.txt %s GIT_TAG = %q, but versions.go pins %q — sync the hand-maintained pin (AGENTS.md §18.2)", repo, got, want)
		}
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
