package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// TestPruneGeneratedCpp_RemovesOnlyGeneratedSubtrees pins the blast radius of
// the prune: the three regenerated subdirectories go, everything else under
// <package>/cpp stays. A prune that reached cppDir itself would take out a
// concurrently-configured build tree if a user ever pointed one there.
func TestPruneGeneratedCpp_RemovesOnlyGeneratedSubtrees(t *testing.T) {
	t.Parallel()
	cppDir := t.TempDir()

	stale := []string{
		filepath.Join("src", "xll_removed_asset.cpp"),
		filepath.Join("src", "nested", "deep.cpp"),
		filepath.Join("include", "ribbon_xml.h"),
		filepath.Join("tools", "old_tool.cpp"),
	}
	survivors := []string{
		"CMakeLists.txt",
		"xll_main.cpp",
	}
	for _, rel := range append(append([]string{}, stale...), survivors...) {
		p := filepath.Join(cppDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if err := pruneGeneratedCpp(cppDir); err != nil {
		t.Fatalf("pruneGeneratedCpp: %v", err)
	}

	for _, rel := range stale {
		if _, err := os.Stat(filepath.Join(cppDir, rel)); !os.IsNotExist(err) {
			t.Errorf("%s survived the prune (stat err = %v)", rel, err)
		}
	}
	for _, rel := range survivors {
		if _, err := os.Stat(filepath.Join(cppDir, rel)); err != nil {
			t.Errorf("%s should NOT have been pruned: %v", rel, err)
		}
	}

	// Pruning a tree that was never generated must not be an error.
	if err := pruneGeneratedCpp(filepath.Join(cppDir, "does-not-exist")); err != nil {
		t.Errorf("prune of a nonexistent cppDir returned %v, want nil", err)
	}
}

// TestGenerate_PrunesStaleCppSource is the wiring gate. A .cpp left over from an
// older xll-gen version is not inert: CMakeLists.txt.tmpl compiles
// `file(GLOB "${CMAKE_CURRENT_SOURCE_DIR}/src/*.cpp")`, so the stale file is
// still built and linked into the XLL. Regenerating into an existing project
// must remove it.
//
// FAIL-confirmed by removing the pruneGeneratedCpp call from Generate.
func TestGenerate_PrunesStaleCppSource(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "PruneProject", Version: "0.1.0"},
		Gen:     config.GenConfig{Go: config.GoConfig{Module: "testmod"}},
		Server:  config.ServerConfig{Launch: &config.LaunchConfig{Enabled: new(bool)}},
		Build:   config.BuildConfig{Singlefile: "xll", TempDir: "temp_%PROJECT%"},
		Functions: []config.Function{
			{Name: "Add", Description: "adds", Args: []config.Arg{{Name: "a", Type: "float"}}, Return: "float"},
		},
	}
	*cfg.Server.Launch.Enabled = true

	if err := Generate(cfg, tmpDir, "testmod", Options{}); err != nil {
		t.Fatalf("first Generate: %v", err)
	}

	cppDir := filepath.Join(tmpDir, cfg.GoPackage(), "cpp")
	staleSrc := filepath.Join(cppDir, "src", "xll_asset_deleted_upstream.cpp")
	staleHdr := filepath.Join(cppDir, "include", "xll_header_deleted_upstream.h")
	for _, p := range []string{staleSrc, staleHdr} {
		if err := os.WriteFile(p, []byte("#error stale\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if err := Generate(cfg, tmpDir, "testmod", Options{}); err != nil {
		t.Fatalf("second Generate: %v", err)
	}

	for _, p := range []string{staleSrc, staleHdr} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("stale %s survived regeneration — file(GLOB src/*.cpp) would still compile it (stat err = %v)",
				filepath.Base(p), err)
		}
	}

	// The regenerated tree must still be complete after the prune.
	for _, rel := range []string{
		filepath.Join("src", "xll_log.cpp"),
		filepath.Join("include", "xll_ipc.h"),
		filepath.Join("include", "schema_generated.h"),
		"xll_main.cpp",
		"CMakeLists.txt",
	} {
		if _, err := os.Stat(filepath.Join(cppDir, rel)); err != nil {
			t.Errorf("regenerated tree is missing %s after prune: %v", rel, err)
		}
	}
}
