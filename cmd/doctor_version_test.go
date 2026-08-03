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

// TestGoModPinsMatchVersions guards xll-gen's OWN go.mod against drift from
// internal/versions/versions.go. This is the 7th shared-dependency pin location
// (AGENTS.md §18.2), and until 2026-08-03 it was the only one with no gate.
//
// It had already drifted. versions.go said shm v0.8.20 while go.mod said v0.8.18:
// the shm v0.8.19 and v0.8.20 releases bumped the C++ pin and left the Go one
// behind. History shows the two used to move together (v0.8.15 → v0.8.16 →
// v0.8.18 in lockstep), so this was an omission, not a policy.
//
// WHY IT MATTERS, given that generated projects were unaffected: MVS picks the
// higher version for a generated project (the showcase pins shm directly), so the
// PRODUCT was fine. What was not fine is that xll-gen's own test suite compiled
// pkg/server and pkg/rtd against the OLDER shm — precisely the packages that
// import shm/go — so a Go-API regression introduced by shm v0.8.19/v0.8.20 could
// not have been caught here. A stale pin in a test-only position is still a hole
// in the thing the tests are for.
//
// sugar is deliberately NOT checked: versions.go does not carry a sugar pin (it
// feeds C++ FetchContent tags, and sugar is Go-only), so go.mod is its single
// source of truth and there is nothing to drift from.
func TestGoModPinsMatchVersions(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	for repo, want := range map[string]string{
		"shm":   versions.SHM,
		"types": versions.Types,
	} {
		re := regexp.MustCompile(`github\.com/xll-gen/` + regexp.QuoteMeta(repo) + `\s+(v\S+)`)
		m := re.FindStringSubmatch(content)
		if len(m) < 2 {
			t.Errorf("could not find the %s requirement in go.mod", repo)
			continue
		}
		if got := m[1]; got != want {
			t.Errorf("go.mod pins %s %s but versions.go pins %s — xll-gen's own packages would be "+
				"built and TESTED against a different version than generated projects get "+
				"(AGENTS.md §18.2)", repo, got, want)
		}
	}
}

// TestDoctorCMakeMinMatchesTemplate guards doctor's CMake floor against the
// floor the generated project actually demands. `doctor` exists to block BEFORE
// the build does; when minCMakeVersion sits below the template's
// cmake_minimum_required, doctor reports a green checkmark and the user meets
// CMake's own "CMake 3.28 or higher is required" at configure time instead —
// without the winget remediation line xll-gen authored for exactly that case.
//
// doctor may gate HIGHER than the template (a future generator feature could
// need a newer CMake before the template is bumped), never lower.
func TestDoctorCMakeMinMatchesTemplate(t *testing.T) {
	tmplPath := filepath.Join("..", "internal", "templates", "CMakeLists.txt.tmpl")
	data, err := os.ReadFile(tmplPath)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^\s*cmake_minimum_required\s*\(\s*VERSION\s+([0-9]+(?:\.[0-9]+)*)`)
	m := re.FindStringSubmatch(string(data))
	if len(m) < 2 {
		t.Fatalf("could not find cmake_minimum_required in %s", tmplPath)
	}
	tmplMin := m[1]

	want, wantOK := parseVersion(tmplMin)
	got, gotOK := parseVersion(minCMakeVersion)
	if !wantOK || !gotOK {
		t.Fatalf("unparseable versions: template %q (ok=%v), doctor %q (ok=%v)", tmplMin, wantOK, minCMakeVersion, gotOK)
	}
	if compareVersions(got, want) < 0 {
		t.Errorf("doctor minCMakeVersion = %q but %s requires %q — doctor would report green on a CMake the build then rejects (AGENTS.md §18.2)",
			minCMakeVersion, tmplPath, tmplMin)
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
