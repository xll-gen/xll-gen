package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/xll-gen/xll-gen/internal/generator"
)

// cppCompileGateYaml drives the C++ wrapper codegen surface that the Go-side
// compile gate (TestGeneratedServerCompiles) cannot exercise: the generated
// xll_main.cpp export signatures and registration type strings. It focuses on
// the rtd-once grid-spill feature, whose numgrid case was a C++-only BLOCKER —
// the wrapper exported LPXLOPER12 while its body returned NumGridToFP12() (an
// FP12*), so it did not compile. The Go gate never caught it (Go has no
// counterpart to the C++ return-type mismatch), and the generator tests only
// checked registry gating. This fixture + a real cmake --build is the gate.
//
//   - rtd-once numgrid -> MUST export FP12* / register K% (the fixed BLOCKER)
//   - rtd-once grid    -> LPXLOPER12 / Q (control; must stay byte-identical)
//   - rtd-once scalar  -> LPXLOPER12 / Q (control)
//   - sync numgrid     -> FP12* / K% (the reference convention rtd-once mirrors)
const cppCompileGateYaml = `project:
  name: "cpp_gate"
  version: "0.1.0"

rtd:
  enabled: true
  prog_id: "CppGate.RTD"

gen:
  go:
    package: "generated"

logging:
  level: "debug"

functions:
  - name: "NumGridOnce"
    mode: "rtd-once"
    args: [{name: "s", type: "string"}]
    return: "numgrid"
  - name: "GridOnce"
    mode: "rtd-once"
    args: [{name: "s", type: "string"}]
    return: "grid"
  - name: "ScalarOnce"
    mode: "rtd-once"
    args: [{name: "n", type: "int"}]
    return: "float"
  - name: "SyncNumGrid"
    args: [{name: "g", type: "numgrid"}]
    return: "numgrid"
`

// repoRootForCppGate returns the xll-gen repo working tree (one dir above cmd).
func repoRootForCppGate(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(thisFile)) // .../cmd -> repo root
}

// TestRtdOnceNumGridCppCompiles is the C++ compile gate for the generated XLL
// wrapper. It generates the cppCompileGateYaml project, then configures+builds
// the generated/cpp XLL target with cmake (MinGW), overriding the FetchContent
// types/shm sources with the local sibling checkouts so it tests CURRENT code,
// not the pinned tags. The build compiling xll_main.cpp into the .xll is the
// assertion: before the fix it failed with
// "cannot convert 'FP12*' to 'LPXLOPER12' in return" at the rtd-once numgrid
// wrapper.
//
// Skipped (not failed) when the toolchain or the local checkouts are absent, so
// it stays green on CI without MinGW; gated by -short like the other heavy
// gates. Point it at the checkouts via env:
//
//	XLLGEN_TYPES_SRC = <abs path to ../types>  (defaults to ../types sibling)
//	XLLGEN_SHM_SRC   = <abs path to ../shm>    (defaults to ../shm   sibling)
func TestRtdOnceNumGridCppCompiles(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping C++ compile gate in short mode")
	}

	cmakeBin, err := exec.LookPath("cmake")
	if err != nil {
		t.Skip("cmake not on PATH; skipping C++ compile gate")
	}

	repoRoot := repoRootForCppGate(t)
	siblingRoot := filepath.Dir(repoRoot) // .../xll-gen (workspace) -> holds types/, shm/

	typesSrc := os.Getenv("XLLGEN_TYPES_SRC")
	if typesSrc == "" {
		typesSrc = filepath.Join(siblingRoot, "types")
	}
	shmSrc := os.Getenv("XLLGEN_SHM_SRC")
	if shmSrc == "" {
		shmSrc = filepath.Join(siblingRoot, "shm")
	}
	// The rtd-once grid-spill C++ (protocol::RtdOnceGridResult etc.) is only in
	// the local types checkout, not the pinned tag, so the local source is
	// REQUIRED, not just preferred. Skip if either checkout is missing.
	if _, err := os.Stat(filepath.Join(typesSrc, "CMakeLists.txt")); err != nil {
		t.Skipf("local types checkout not found at %s; skipping (set XLLGEN_TYPES_SRC)", typesSrc)
	}
	if _, err := os.Stat(shmSrc); err != nil {
		t.Skipf("local shm checkout not found at %s; skipping (set XLLGEN_SHM_SRC)", shmSrc)
	}

	tempDir, err := os.MkdirTemp("", "xll-cpp-gate")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	projectDir := filepath.Join(tempDir, "cpp_gate")
	if err := runInit(projectDir, false, false); err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	if err := os.WriteFile(filepath.Join(projectDir, "xll.yaml"), []byte(cppCompileGateYaml), 0o644); err != nil {
		t.Fatal(err)
	}

	// Real (cached) flatc: the generated FlatBuffers C++ headers must carry the
	// per-message symbols xll_main.cpp references; a mock flatc emits stubs.
	runGenerateInDir(t, projectDir, generator.Options{})

	cppDir := filepath.Join(projectDir, "generated", "cpp")
	buildDir := filepath.Join(cppDir, "build")

	// Cache FetchContent downloads (flatbuffers/phmap) across runs.
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	fcBase := filepath.Join(cacheDir, "xll-gen", "cpp_gate_fetch")
	_ = os.MkdirAll(fcBase, 0o755)

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
		t.Fatalf("cmake configure failed: %v\n%s", err, out)
	}

	buildCmd := exec.Command(cmakeBin, "--build", buildDir, "--target", "cpp_gate")
	out, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated XLL failed to compile (the rtd-once numgrid BLOCKER regression):\n%s", out)
	}
}
