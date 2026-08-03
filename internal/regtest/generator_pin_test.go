package regtest

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/versions"
)

// TestGenerateSimCMakePinsShmToVersions asserts on the file that actually SHIPS
// into a generated project, not on the template source.
//
// cmd/doctor_version_test.go::TestRegtestCMakePinsMatchVersions reads
// internal/templates/regtest_CMakeLists.txt.tmpl from disk. That is the right
// gate for drift, but it says nothing about what generateSimCMake WRITES. The
// two are identical only because generateSimCMake copies the template verbatim —
// a deliberate choice (the file holds no template actions, and running static
// CMake through text/template would make any future `{{` an action), and a choice
// a later refactor could reverse. This test pins the OUTPUT.
//
// What it protects: this mock host and the generated project's Go server are the
// two ends of ONE shm attach. The Go end is resolved to versions.SHM by
// internal/generator/dependencies.go, so if the C++ end here is a branch — it was
// `GIT_TAG main` until 2026-08-03 — the two ends move independently. shm SPEC
// §2.1 makes a wire-version mismatch a HARD FAILURE at attach: NewDirectGuest
// refuses ("protocol version mismatch: 0x80000 (expected 0x70000)") and the
// generated project's regtest does not start at all. Nothing degrades; it stops.
//
// Note that no test BUILDS this file — cmd/regression_test.go compiles the mock
// host from the separate static fixture internal/regtest/testdata/CMakeLists.txt
// (embedded as regtest.CMakeLists), which carries its own pins. So this
// string-level assertion is the only automated statement about the shm version
// that shipped regtest scaffolding will fetch.
func TestGenerateSimCMakePinsShmToVersions(t *testing.T) {
	dir := t.TempDir()
	if err := generateSimCMake(&config.Config{}, dir); err != nil {
		t.Fatalf("generateSimCMake: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "CMakeLists.txt"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	for _, pin := range []struct {
		repo  string
		konst string // the versions.go identifier, for the failure message
		want  string
	}{
		{"shm", "versions.SHM", versions.SHM},
		{"flatbuffers", "versions.FlatBuffers", versions.FlatBuffers},
	} {
		re := regexp.MustCompile(`/` + regexp.QuoteMeta(pin.repo) + `\.git\s+GIT_TAG\s+(\S+)`)
		m := re.FindStringSubmatch(content)
		if len(m) < 2 {
			t.Errorf("generated regtest CMakeLists.txt has no GIT_TAG for %s", pin.repo)
			continue
		}
		if got := m[1]; got != pin.want {
			t.Errorf("generated regtest CMakeLists.txt fetches %s at %q, but generated projects resolve their Go side to %s = %q — the two ends of the shm attach would disagree and shm SPEC §2.1 fails the attach outright (AGENTS.md §18.2)",
				pin.repo, got, pin.konst, pin.want)
		}
	}

	// Belt and braces: nothing template-shaped may survive into the emitted
	// file. generateSimCMake does NOT render, so a `{{ .Deps.SHM }}` written
	// into the template would ship literally and CMake would try to clone a ref
	// named "{{".
	if regexp.MustCompile(`\{\{`).MatchString(content) {
		t.Errorf("generated regtest CMakeLists.txt contains an unrendered template action; generateSimCMake copies verbatim by design, so pins here must be literal tags")
	}
}
