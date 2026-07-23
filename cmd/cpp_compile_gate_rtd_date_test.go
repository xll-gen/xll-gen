package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/xll-gen/xll-gen/internal/generator"
)

// cppRtdDateGateYaml is an RTD-enabled project with rtd AND rtd-once functions
// that take a `date` argument. It is the C++ compile gate for Defect C: a date
// arg (ArgCppType "double", passed by value) was routed into the rtd/rtd-once
// topic-building loop's COMPOSITE branch, which calls
// xll::ContentHashToken('a', <double>) / declares refPayload — but
// ContentHashToken takes an XLOPER12*, so the generated wrapper FAILED TO
// COMPILE for any rtd(-once) function with a date arg. The fix stringifies the
// date serial as a plain scalar topic (via the %.17g round-trip helper
// xll_main's FormatDoubleRoundTrip), co-changed with the Go dispatch
// (server.SerialToTime(ParseFloat(...))).
const cppRtdDateGateYaml = `project:
  name: "cpp_rtd_date_gate"
  version: "0.1.0"

rtd:
  enabled: true
  prog_id: "RtdDateGate.RTD"

gen:
  go:
    package: "generated"

logging:
  level: "debug"

functions:
  - name: "RtdAsOf"
    mode: "rtd"
    args: [{name: "asof", type: "date"}, {name: "sym", type: "string"}]
    return: "float"
  - name: "AsOfOnce"
    mode: "rtd-once"
    args: [{name: "asof", type: "date"}]
    return: "float"
`

// TestRtdDateCppCompiles generates the rtd+date project and builds the
// generated/cpp XLL with cmake (MinGW), overriding the FetchContent types/shm
// sources with the local sibling checkouts so it tests CURRENT code.
//
// Skipped (not failed) when the toolchain or the local checkouts are absent, so
// it stays green on CI without MinGW; gated by -short like the sibling gates.
//
// NOTE: deliberately NOT t.Parallel — the cmake C++ gates contend on the shared
// FetchContent cache dir, so they run serially.
func TestRtdDateCppCompiles(t *testing.T) {
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

	tempDir, err := os.MkdirTemp("", "xll-cpp-rtd-date-gate")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	projectDir := filepath.Join(tempDir, "cpp_rtd_date_gate")
	if err := runInit(projectDir, false, false); err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	if err := os.WriteFile(filepath.Join(projectDir, "xll.yaml"), []byte(cppRtdDateGateYaml), 0o644); err != nil {
		t.Fatal(err)
	}

	runGenerateInDir(t, projectDir, generator.Options{})

	cppDir := filepath.Join(projectDir, "generated", "cpp")
	buildDir := filepath.Join(cppDir, "build")

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

	buildCmd := exec.Command(cmakeBin, "--build", buildDir, "--target", "cpp_rtd_date_gate")
	out, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated rtd+date XLL failed to compile/link (Defect C: rtd/rtd-once date topic):\n%s", out)
	}
}
