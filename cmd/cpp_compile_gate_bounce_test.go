package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/xll-gen/xll-gen/internal/generator"
)

// cppBounceGateYaml is a ribbon-enabled project template parameterized by the
// ribbon.bounce mode. The keep-open and off modes render GetExcelApplicationOrBounce
// with template-elided regions (no close-by-identity machinery in keep-open; no
// bounce body at all in off) — the string-presence tests in
// internal/generator/gen_ribbon_bounce_test.go pin WHAT is emitted, and this
// gate proves the emitted subset actually COMPILES AND LINKS: a future template
// edit that leaves a dangling reference to an elided symbol (scratchName,
// GetActiveWorkbookName, ScratchCloseEventSuppressor, ...) fails here at
// generation-test time instead of in a user's build.
const cppBounceGateYaml = `project:
  name: "%s"
  version: "0.1.0"

gen:
  go:
    package: "generated"

logging:
  level: "debug"

functions:
  - name: "AddOne"
    mode: "sync"
    args: [{name: "n", type: "int"}]
    return: "int"

commands:
  - name: "RunThing"
    description: "bounce gate command"

ribbon:
  tab: "Gate"
  groups:
    - label: "G"
      buttons:
        - label: "Run"
          command: RunThing
  bounce: %s
`

// TestRibbonBounceModesCppCompile generates a ribbon project for each
// ribbon.bounce mode (full, keep-open, off) and builds the generated/cpp XLL
// with cmake (MinGW), overriding the FetchContent types/shm sources with the
// local sibling checkouts. keep-open/off are the elided-render modes with
// dangling-symbol risk; full is the only mode that compiles the
// ScratchCloseEventSuppressor COM guard, so all three carry unique coverage.
//
// Skipped (not failed) when the toolchain or the local checkouts are absent;
// gated by -short like the sibling gates.
// NOTE: deliberately NOT t.Parallel — the cmake C++ gates contend on the
// shared FetchContent cache dir, so this suite runs them serially; the two
// modes also run sequentially inside this one test for the same reason.
func TestRibbonBounceModesCppCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping C++ compile gate in short mode")
	}

	cmakeBin, err := exec.LookPath("cmake")
	if err != nil {
		t.Skip("cmake not on PATH; skipping C++ compile gate")
	}

	repoRoot := repoRootForCppGate(t)
	siblingRoot := filepath.Dir(repoRoot)

	typesSrc := os.Getenv("XLLGEN_TYPES_SRC")
	if typesSrc == "" {
		typesSrc = filepath.Join(siblingRoot, "types")
	}
	shmSrc := os.Getenv("XLLGEN_SHM_SRC")
	if shmSrc == "" {
		shmSrc = filepath.Join(siblingRoot, "shm")
	}
	if _, err := os.Stat(filepath.Join(typesSrc, "CMakeLists.txt")); err != nil {
		t.Skipf("local types checkout not found at %s; skipping (set XLLGEN_TYPES_SRC)", typesSrc)
	}
	if _, err := os.Stat(shmSrc); err != nil {
		t.Skipf("local shm checkout not found at %s; skipping (set XLLGEN_SHM_SRC)", shmSrc)
	}

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	fcBase := filepath.Join(cacheDir, "xll-gen", "cpp_gate_fetch")
	_ = os.MkdirAll(fcBase, 0o755)

	for _, tc := range []struct {
		mode string // ribbon.bounce value
		proj string // project name == cmake target (must be a valid identifier)
	}{
		// full compiles the ScratchCloseEventSuppressor (the DLP
		// close-suppression guard) — the other two modes elide it, so full is
		// the only mode where that COM code is exercised by a compiler.
		{mode: "full", proj: "cpp_bounce_full_gate"},
		{mode: "keep-open", proj: "cpp_bounce_keepopen_gate"},
		{mode: "off", proj: "cpp_bounce_off_gate"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			tempDir, err := os.MkdirTemp("", "xll-cpp-bounce-gate")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(tempDir)

			projectDir := filepath.Join(tempDir, tc.proj)
			if err := runInit(projectDir, false, false); err != nil {
				t.Fatalf("runInit failed: %v", err)
			}

			yaml := fmt.Sprintf(cppBounceGateYaml, tc.proj, tc.mode)
			if err := os.WriteFile(filepath.Join(projectDir, "xll.yaml"), []byte(yaml), 0o644); err != nil {
				t.Fatal(err)
			}

			runGenerateInDir(t, projectDir, generator.Options{})

			cppDir := filepath.Join(projectDir, "generated", "cpp")
			buildDir := filepath.Join(cppDir, "build")

			cfgCmd := exec.Command(cmakeBin,
				"-G", "MinGW Makefiles",
				"-S", cppDir,
				"-B", buildDir,
				"-DCMAKE_BUILD_TYPE=Debug",
				"-DFETCHCONTENT_BASE_DIR="+fcBase,
				"-DFETCHCONTENT_SOURCE_DIR_TYPES="+typesSrc,
				"-DFETCHCONTENT_SOURCE_DIR_SHM="+shmSrc,
			)
			if out, err := cfgCmd.CombinedOutput(); err != nil {
				t.Fatalf("cmake configure failed (bounce %s): %v\n%s", tc.mode, err, out)
			}

			buildCmd := exec.Command(cmakeBin, "--build", buildDir, "--target", tc.proj)
			out, err := buildCmd.CombinedOutput()
			if err != nil {
				t.Fatalf("generated ribbon XLL (bounce %s) failed to compile/link — likely a dangling reference to a template-elided symbol:\n%s", tc.mode, out)
			}
		})
	}
}
