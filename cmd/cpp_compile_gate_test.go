package cmd

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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
//   - rtd / rtd-once with COMPOSITE args (grid/range/numgrid/any) -> the
//     content-hash payload path (§19.3). Added because no gate compiled it: the
//     rtd-once payload build was moved from the topic loop into the cache-miss
//     branch (it is fully discarded on a memoize hit) and the plain-rtd build
//     gained a g_sentRefCache already-shipped peek, and neither the golden nor
//     the marker tests can catch a C++ scope/compile error in that code.
const cppCompileGateYaml = `project:
  name: "cpp_gate"
  version: "0.1.0"

# A user-declared CalculationCanceled with a RENAMED handler. Until v0.8.36 the
# named-event stub only logged, so nothing compiled the cancel forwarding path;
# now it emits xll::HandleCalculationCanceled() from an exported proc, and the
# built-in CalculationEnded() macro must still be emitted alongside it (no
# CalculationEnded event is declared here on purpose, so both branches of the
# `+"`hasEvent \"CalculationEnded\"`"+` gate are exercised in one build).
events:
  - type: CalculationCanceled
    handler: OnEscape

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
  - name: "RtdComposite"
    mode: "rtd"
    args: [{name: "g", type: "grid"}, {name: "r", type: "range"}, {name: "ng", type: "numgrid"}, {name: "a", type: "any"}]
    return: "float"
  - name: "OnceComposite"
    mode: "rtd-once"
    args: [{name: "g", type: "grid"}, {name: "r", type: "range"}, {name: "ng", type: "numgrid"}, {name: "a", type: "any"}]
    return: "float"
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
// NOTE: deliberately NOT t.Parallel — the cmake C++ gates contend on the shared
// FetchContent cache dir, so this suite runs them serially.
func TestRtdOnceNumGridCppCompiles(t *testing.T) {
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

	buildCmd := exec.Command(cmakeBin, "--build", buildDir, "--target", "cpp_gate", "--parallel", cppGateBuildJobs())
	out, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated XLL failed to compile (the rtd-once numgrid BLOCKER regression):\n%s", out)
	}

	// Excel resolves an event callback by its EXPORTED PROCEDURE NAME — the
	// second argument of xlEventRegister. A handler that compiles but is not
	// exported undecorated (or is renamed by the toolchain) fails silently: the
	// registration is rejected and the event never fires, with nothing in the
	// log. Assert the shipped export table instead of trusting the source.
	// CMAKE_RUNTIME_OUTPUT_DIRECTORY is ${CMAKE_BINARY_DIR}/.. so the .xll lands
	// beside the build dir, in generated/cpp/.
	assertXllExports(t, cppDir, "OnEscape", "xlAutoOpen", "xlAutoClose")

	// Second configuration: XLL_DEBUG=ON (SHM_DEBUG + XLL_DEBUG_LOGGING). The
	// build above leaves XLL_DEBUG at its OFF default, so it only ever compiled
	// the release side of the template's `#ifdef XLL_DEBUG_LOGGING` blocks — a
	// debug-only block could be broken indefinitely and every gate would stay
	// green. A separate build tree (not a reconfigure of buildDir) because CMake
	// keeps unspecified cache variables, which is the same trap Taskfile.yml
	// guards with an explicit -DXLL_DEBUG=OFF. FetchContent is already populated
	// in fcBase, so this costs a compile, not a download.
	debugBuildDir := filepath.Join(cppDir, "build-debug")
	cfgDebug := exec.Command(cmakeBin,
		"-G", "MinGW Makefiles",
		"-S", cppDir,
		"-B", debugBuildDir,
		"-DCMAKE_BUILD_TYPE=Debug",
		"-DXLL_DEBUG=ON",
		"-DFETCHCONTENT_BASE_DIR="+fcBase,
		"-DFETCHCONTENT_SOURCE_DIR_TYPES="+typesSrc,
		"-DFETCHCONTENT_SOURCE_DIR_SHM="+shmSrc,
	)
	if out, err := cfgDebug.CombinedOutput(); err != nil {
		t.Fatalf("cmake configure (XLL_DEBUG=ON) failed: %v\n%s", err, out)
	}
	buildDebug := exec.Command(cmakeBin, "--build", debugBuildDir, "--target", "cpp_gate", "--parallel", cppGateBuildJobs())
	if out, err := buildDebug.CombinedOutput(); err != nil {
		t.Fatalf("generated XLL failed to compile with XLL_DEBUG=ON (debug-only code in xll_main.cpp.tmpl is broken):\n%s", out)
	}
}

// assertXllExports opens the built .xll under root and fails unless every name
// is present in its PE export directory.
func assertXllExports(t *testing.T, root string, names ...string) {
	t.Helper()

	var matches []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".xll") {
			matches = append(matches, path)
		}
		return nil
	})
	if len(matches) == 0 {
		t.Fatalf("no .xll artifact found under %s", root)
	}

	have, err := peExportNames(matches[0])
	if err != nil {
		t.Fatalf("read export directory of %s: %v", matches[0], err)
	}
	for _, want := range names {
		if !have[want] {
			t.Errorf("built XLL does not export %q; Excel resolves callbacks by exported name, "+
				"so this would fail registration silently. Exports: %v", want, sortedKeys(have))
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// peExportNames returns the names in a PE image's export directory.
// debug/pe exposes IMPORTS but not exports, so the IMAGE_EXPORT_DIRECTORY is
// walked by hand: DataDirectory[0] -> the section holding it -> AddressOfNames
// -> the NUL-terminated name strings.
func peExportNames(path string) (map[string]bool, error) {
	f, err := pe.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var dirRVA, dirSize uint32
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		if len(oh.DataDirectory) == 0 {
			return nil, fmt.Errorf("no data directories")
		}
		dirRVA, dirSize = oh.DataDirectory[0].VirtualAddress, oh.DataDirectory[0].Size
	case *pe.OptionalHeader32:
		if len(oh.DataDirectory) == 0 {
			return nil, fmt.Errorf("no data directories")
		}
		dirRVA, dirSize = oh.DataDirectory[0].VirtualAddress, oh.DataDirectory[0].Size
	default:
		return nil, fmt.Errorf("unsupported optional header %T", f.OptionalHeader)
	}
	if dirRVA == 0 || dirSize == 0 {
		return nil, fmt.Errorf("image has no export directory")
	}

	// readRVA returns up to n bytes of image data starting at the given RVA.
	readRVA := func(rva uint32, n int) ([]byte, error) {
		for _, s := range f.Sections {
			if rva >= s.VirtualAddress && rva < s.VirtualAddress+s.VirtualSize {
				data, err := s.Data()
				if err != nil {
					return nil, err
				}
				off := int(rva - s.VirtualAddress)
				if off >= len(data) {
					return nil, fmt.Errorf("rva %#x past section %s data", rva, s.Name)
				}
				end := off + n
				if end > len(data) {
					end = len(data)
				}
				return data[off:end], nil
			}
		}
		return nil, fmt.Errorf("rva %#x is in no section", rva)
	}

	hdr, err := readRVA(dirRVA, 40)
	if err != nil {
		return nil, err
	}
	if len(hdr) < 40 {
		return nil, fmt.Errorf("truncated export directory")
	}
	numNames := binary.LittleEndian.Uint32(hdr[24:])
	namesRVA := binary.LittleEndian.Uint32(hdr[32:])

	out := make(map[string]bool, numNames)
	for i := uint32(0); i < numNames; i++ {
		ptr, err := readRVA(namesRVA+i*4, 4)
		if err != nil {
			return nil, err
		}
		nameRVA := binary.LittleEndian.Uint32(ptr)
		raw, err := readRVA(nameRVA, 512)
		if err != nil {
			return nil, err
		}
		if z := bytes.IndexByte(raw, 0); z >= 0 {
			raw = raw[:z]
		}
		out[string(raw)] = true
	}
	return out, nil
}
