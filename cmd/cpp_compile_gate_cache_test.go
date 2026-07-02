package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/xll-gen/xll-gen/internal/generator"
)

// cppCacheGateYaml is a SYNC-ONLY, cache-enabled project (no RTD, no ribbon)
// that exercises the cache-key collection ladder in xll_main.cpp.tmpl for the
// argument types that previously fell through to a blanket
// `cacheArgs.push_back((LPXLOPER12){{.Name}})`:
//
//   - `date` (ArgCppType "double"): the blanket cast was `(LPXLOPER12)d`, an
//     ill-formed cast from a `double` to a pointer -> the generated C++ FAILED
//     TO COMPILE (GCC "invalid cast from type 'double' to type 'LPXLOPER12'",
//     MSVC C2440) for any cache-enabled non-async function with a date arg.
//   - `numgrid` (FP12*): the blanket cast reinterpreted the FP12* as an
//     XLOPER12* -> compiled but mis-keyed / could AV; the fix folds its
//     ContentHashTokenFP12 into the cache key instead.
//   - `string` (registered LPXLOPER12): the old branch called CreateStringXLOPER,
//     which is defined nowhere — so cache-enabled functions with a string arg
//     never compiled. Now pushed directly (Excel-owned XLOPER12*, content-hashed
//     by SerializeXLOPER's xltypeStr case) like grid/range/any.
//   - `grid`/`range`/`any` (genuine LPXLOPER12): kept their correct blanket
//     push (MakeCacheKey content-hashes them). Included so the gate proves the
//     preserved path still compiles alongside the reworked branches.
//
// The build compiling + LINKING xll_main.cpp (with cache.enabled true and these
// args present) is the assertion — the date arg alone regressed the compile.
const cppCacheGateYaml = `project:
  name: "cpp_cache_gate"
  version: "0.1.0"

cache:
  enabled: true
  ttl: "10m"

gen:
  go:
    package: "generated"

logging:
  level: "debug"

functions:
  - name: "CacheDate"
    mode: "sync"
    args: [{name: "d", type: "date"}]
    return: "int"
  - name: "CacheNumGrid"
    mode: "sync"
    args: [{name: "ng", type: "numgrid"}]
    return: "int"
  - name: "CacheComposite"
    mode: "sync"
    args: [{name: "g", type: "grid"}, {name: "r", type: "range"}, {name: "a", type: "any"}]
    return: "int"
  - name: "CacheString"
    mode: "sync"
    args: [{name: "s", type: "string"}]
    return: "int"
`

// TestCacheKeyCppCompiles generates the sync-only cache-enabled project and
// builds the generated/cpp XLL with cmake (MinGW), overriding the FetchContent
// types/shm sources with the local sibling checkouts so it tests CURRENT code.
// It is the C++ compile gate for the cache-key ladder fix: before the fix the
// date arg produced `(LPXLOPER12)d`, which does not compile.
//
// Skipped (not failed) when the toolchain or the local checkouts are absent, so
// it stays green on CI without MinGW; gated by -short like the sibling gates.
//
// NOTE: deliberately NOT t.Parallel — the cmake C++ gates contend on the shared
// FetchContent cache dir, so this suite runs them serially.
func TestCacheKeyCppCompiles(t *testing.T) {
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

	tempDir, err := os.MkdirTemp("", "xll-cpp-cache-gate")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	projectDir := filepath.Join(tempDir, "cpp_cache_gate")
	if err := runInit(projectDir, false, false); err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	if err := os.WriteFile(filepath.Join(projectDir, "xll.yaml"), []byte(cppCacheGateYaml), 0o644); err != nil {
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

	buildCmd := exec.Command(cmakeBin, "--build", buildDir, "--target", "cpp_cache_gate")
	out, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated cache-enabled XLL failed to compile/link (cache-key ladder date/numgrid fix):\n%s", out)
	}
}
