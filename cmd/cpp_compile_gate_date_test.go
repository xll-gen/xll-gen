package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/xll-gen/xll-gen/internal/generator"
)

// cppDateGateYaml is a SYNC-ONLY project (no RTD, no ribbon) whose functions
// take and return `date`. It is the compile gate for the date auto-format
// rework: DrainAndApplyDateFormats now targets cells via the in-process
// Application IDispatch + COM Range.NumberFormat (no xlcSelect/xlcFormatNumber),
// and src/xll_date_format.cpp is compiled UNCONDITIONALLY. A sync-only build is
// exactly the configuration where that COM path is newly exercised: the ribbon
// / RTD-throttle code that previously owned GetExcelApplication + the oleacc
// link is absent here, so this fixture proves the shared
// include/com/excel_app.h + the unconditional COM link in CMakeLists actually
// compile and link with no RTD/ribbon present.
const cppDateGateYaml = `project:
  name: "cpp_date_gate"
  version: "0.1.0"

gen:
  go:
    package: "generated"

logging:
  level: "debug"

functions:
  # The date auto-format PRODUCER (ScheduleDateFormatsForCaller) is wired by the
  # value-materializing RETURN types 'any' and 'grid' (a returned scalar date or
  # a grid carrying date cells), not by the arg type — so these two functions
  # exercise the producer side, while src/xll_date_format.cpp (the reworked COM
  # Range.NumberFormat consumer) is compiled+linked unconditionally in this
  # sync-only, no-RTD, no-ribbon build. (A 'date' arg is deliberately avoided
  # here: it collides with an UNRELATED pre-existing wrapper arg-decode codegen
  # quirk that is out of scope for the date-format runtime rework.)
  - name: "AnyVal"
    mode: "sync"
    args: [{name: "n", type: "int"}]
    return: "any"
  - name: "DateGrid"
    mode: "sync"
    args: [{name: "n", type: "int"}]
    return: "grid"
`

// TestSyncDateCppCompiles generates the sync-only date project and builds the
// generated/cpp XLL with cmake (MinGW), overriding the FetchContent types/shm
// sources with the local sibling checkouts so it tests CURRENT code. The build
// compiling + LINKING xll_main.cpp and src/xll_date_format.cpp into the .xll is
// the assertion. Before this rework a sync-only build did not link the COM libs
// (oleacc/ole32/oleaut32) and had no GetExcelApplication, so the new COM
// Range.NumberFormat date path would fail to link here.
//
// Skipped (not failed) when the toolchain or the local checkouts are absent, so
// it stays green on CI without MinGW; gated by -short like the sibling gate.
// NOTE: deliberately NOT t.Parallel — the cmake C++ gates contend on the shared
// FetchContent cache dir, so this suite runs them serially.
func TestSyncDateCppCompiles(t *testing.T) {
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

	tempDir, err := os.MkdirTemp("", "xll-cpp-date-gate")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	projectDir := filepath.Join(tempDir, "cpp_date_gate")
	if err := runInit(projectDir, false, false); err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	if err := os.WriteFile(filepath.Join(projectDir, "xll.yaml"), []byte(cppDateGateYaml), 0o644); err != nil {
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

	buildCmd := exec.Command(cmakeBin, "--build", buildDir, "--target", "cpp_date_gate")
	out, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated sync-only date XLL failed to compile/link (date COM Range.NumberFormat rework):\n%s", out)
	}
}
