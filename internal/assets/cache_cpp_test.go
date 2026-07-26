package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Always-on source markers (no toolchain required)
// ---------------------------------------------------------------------------

// TestCacheMapsUseRealLocks pins that the CacheManager maps keep phmap's `_m`
// (std::mutex) aliases.
//
// Regression it pins (HIGH, adversarial review): `phmap::parallel_flat_hash_map<K,V>`
// spelled with only two template arguments leaves the 7th parameter at its
// default, `phmap::NullMutex` — "use std::mutex to enable internal locks"
// (parallel_hashmap/phmap_fwd_decl.h). Both maps were spelled that way, so the
// header's own "thread-safe concurrent access" claim was false and the maps did NO
// locking. Cache-enabled functions are registered `$` (thread-safe), so Excel's
// multi-threaded recalculation calls Get/Put from several calculation threads
// concurrently — a real data race. An 8-thread Get/Put stress against the
// NullMutex spelling reproducibly trips phmap's own `i < capacity_` assertion
// (see TestCacheNativeBehavior).
//
// The header also carries static_asserts that fail the BUILD if the aliases are
// reverted; this test is the cheap always-on guard.
func TestCacheMapsUseRealLocks(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["include/xll_cache.h"]
	if !ok {
		t.Fatalf("embedded include/xll_cache.h not found in assets")
	}

	for _, want := range []string{
		"phmap::parallel_flat_hash_map_m<std::string, CacheEntry> cache_;",
		"phmap::parallel_flat_hash_map_m<RefKey, uint64_t, RefKeyHash> refCache_;",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("xll_cache.h must declare %q: the plain parallel_flat_hash_map "+
				"spelling defaults to phmap::NullMutex, i.e. NO locking at all, while "+
				"cache-enabled ($-registered) functions call Get/Put from several Excel "+
				"calculation threads", want)
		}
	}

	// The compile-time gates must survive too.
	if strings.Count(src, "must keep the _m (std::mutex) alias") != 2 {
		t.Errorf("xll_cache.h lost one of the two static_asserts that fail the build " +
			"when a map is reverted to the NullMutex (unlocked) spelling")
	}
}

// TestCacheHashPathIsAllocationFree pins the streaming-digest rewrite of the
// cache-key / RTD-token path.
//
// Two defects and one hot-path cost lived on the same lines:
//
//	(1) HIGH, correctness — the xltypeStr branch did
//	    `ConvertExcelString(PascalToWString(px->val.str).c_str())`. PascalToWString
//	    STRIPS the Pascal length prefix; ConvertExcelString is Pascal-ONLY and reads
//	    wstr[0] AS the length. So the first CHARACTER became the count ('H' = 72; a
//	    CJK lead such as U+D55C = 54620) and that many UTF-16 units were transcoded
//	    out of the wstring buffer — a stack out-of-bounds read of ~144 B to ~109 KB
//	    that the 10 MB MAX_STRING_SIZE guard never catches. Any string-bearing cache
//	    key or RTD topic token therefore depended on adjacent stack residue: cache
//	    hits were lost and rtd/rtd-once topic identity churned.
//	(2) MED, performance — SerializeXLOPER built a std::stringstream per XLOPER,
//	    recursively per xltypeMulti CELL (a 100x100 grid = 10k stringstreams + 10k
//	    strings), and MakeCacheKey embedded the whole serialization in the KEY, so
//	    every phmap Get/Put hashed and memcmp'd O(content) bytes.
//
// Both are fixed by feeding raw bytes (length + UTF-16 code units for strings)
// straight into one FNV-1a stream. These markers pin that neither the Pascal
// misuse nor the serialize-into-the-key pattern comes back.
func TestCacheHashPathIsAllocationFree(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["src/xll_cache.cpp"]
	if !ok {
		t.Fatalf("embedded src/xll_cache.cpp not found in assets")
	}
	hdr, ok := m["include/xll_cache.h"]
	if !ok {
		t.Fatalf("embedded include/xll_cache.h not found in assets")
	}

	// Defect 1: the Pascal-only converter must never be handed an already-stripped
	// plain wstring again. Match on CODE only — the fix's explanatory comment
	// quotes the buggy line verbatim.
	code := stripLineComments(src)
	if strings.Contains(code, "ConvertExcelString(ws.c_str())") ||
		strings.Contains(code, "ConvertExcelString(PascalToWString") {
		t.Errorf("xll_cache.cpp feeds a prefix-STRIPPED wstring to the Pascal-only " +
			"ConvertExcelString again: it reads wstr[0] as the length, so the first " +
			"character becomes the count and up to ~109 KB of stack is read out of bounds")
	}

	// The streaming digest must exist and be what the token/key path uses.
	for _, want := range []string{
		"uint64_t HashXLOPERInto(uint64_t seed, const XLOPER12* px)",
		"uint64_t HashXLOPERContent(const XLOPER12* px)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("xll_cache.cpp missing the streaming digest entry point %q", want)
		}
		if !strings.Contains(hdr, strings.TrimSuffix(want, ")")) {
			t.Errorf("xll_cache.h does not declare %q", want)
		}
	}
	if !strings.Contains(src, "return FormatHashToken(typeTag, HashXLOPERContent(px));") {
		t.Errorf("ContentHashToken must hash the XLOPER12 directly (FormatHashToken(typeTag, " +
			"HashXLOPERContent(px))) rather than materializing SerializeXLOPER's string first")
	}

	// Defect 2 of the perf item: the key must be a constant-size digest, not the
	// full serialization of every argument.
	if strings.Contains(code, "ss << SerializeXLOPER(arg)") {
		t.Errorf("MakeCacheKey embeds the full argument serialization in the cache key " +
			"again: key size becomes proportional to the argument CONTENT, so every " +
			"Get/Put costs an O(content) hash + memcmp (a 100x100 grid arg = ~100 KB key)")
	}
	if !strings.Contains(src, "AppendHex16(key, h);") {
		t.Errorf("MakeCacheKey must emit a fixed-width hex digest (AppendHex16)")
	}

	// The RefCache must store the digest, not a formatted string.
	if !strings.Contains(hdr, "uint64_t GetOrComputeRefHash(") {
		t.Errorf("CacheManager::GetOrComputeRefHash must return a uint64_t digest")
	}

	// SerializeXLOPER survives for diagnostics only; make sure that stays labeled,
	// so nobody re-routes the hot path through it.
	if !strings.Contains(src, "DIAGNOSTICS ONLY") {
		t.Errorf("xll_cache.cpp should keep SerializeXLOPER marked DIAGNOSTICS ONLY " +
			"(it costs one stringstream + one std::string per XLOPER, recursively per cell)")
	}
}

// TestGetOrComputeRefHashHashesNonRefArgs pins that the non-xltypeRef branch of
// GetOrComputeRefHash COMPUTES a hash instead of returning a constant.
//
// Regression it pins (HIGH, found while reworking the key): the guard read
//
//	if (!pRef || (pRef->xltype & xltypeRef) == 0) return "";
//
// and xltypeSRef (0x0400) does not intersect xltypeRef (0x0008), so EVERY
// single-area reference argument — the common shape Excel passes for a `U`
// argument on the calling sheet — contributed an EMPTY string to the cache key.
// All of them collapsed onto one key, so a cache-enabled function called on A1:B2
// could be served the result computed for C5:D9. The `else if (xltype ==
// xltypeSRef) ss << computeFn(pRef)` branch that sat below the guard was dead
// code, which is what showed the intent was to compute.
//
// The guard itself is load-bearing and stays (AGENTS.md §22): only an xltypeRef
// carries the (idSheet, rect table) the per-cycle RefCache is keyed on, so
// anything else must be hashed directly rather than memoized.
func TestGetOrComputeRefHashHashesNonRefArgs(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["src/xll_cache.cpp"]
	if !ok {
		t.Fatalf("embedded src/xll_cache.cpp not found in assets")
	}
	if strings.Contains(stripLineComments(src), `(pRef->xltype & xltypeRef) == 0) return "";`) {
		t.Errorf("GetOrComputeRefHash returns an empty hash for non-xltypeRef input again: " +
			"every xltypeSRef argument contributes nothing to the cache key, so distinct " +
			"single-area ranges share one entry")
	}
	if !strings.Contains(src, "if (refTy != xltypeRef) return computeFn(pRef);") {
		t.Errorf("GetOrComputeRefHash must hash a non-xltypeRef argument directly " +
			"(`if (refTy != xltypeRef) return computeFn(pRef);`) — it just skips the " +
			"per-(sheet,rect) RefCache, it must not skip the hash")
	}
}

// ---------------------------------------------------------------------------
// Compile + run gate
// ---------------------------------------------------------------------------

// TestCacheNativeBehavior compiles the EMBEDDED xll_cache.{h,cpp} together with
// testdata/cache_native_test.cpp and runs it.
//
// The whole file is testable without Excel: it only reaches Excel through
// xlCoerce/xlFree on the xltypeRef/xltypeSRef branch, and the harness supplies its
// own Excel12v. The harness covers, with synthetic XLOPER12s:
//
//   - defect 1 — a string token/serialization must be IDENTICAL when computed with
//     two different stack-residue patterns underneath (WithDirtyStack). Against the
//     pre-fix code this fails immediately ("Hello" produced three different tokens
//     and 152- vs 143-byte serializations for a 5-character string) and then
//     segfaults on a string whose first code unit is U+0800 (a 4 KB OOB read).
//   - defect 2 — an 8-thread x 20k-iteration Get/Put stress over a deliberately
//     overlapping key space. Against the NullMutex spelling this trips phmap's own
//     `i < capacity_` assertion 3 runs out of 3.
//   - perf 3 — determinism, collision resistance (string vs number, adjacent CJK
//     code points, empty string, embedded NUL, geometry-sensitive grids) and the
//     constant-size shape of the cache key.
//
// Requires g++ and, for flatbuffers + phmap headers, the C++-gate FetchContent
// cache that the cmake gates populate (<UserCacheDir>/xll-gen/cpp_gate_fetch).
// Skipped (not failed) when any of that is absent, and under -short, like the
// heavier cmake gates. Point it at a types checkout with XLLGEN_TYPES_SRC.
func TestCacheNativeBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping C++ compile+run gate in short mode")
	}
	if runtime.GOOS != "windows" {
		t.Skip("xll_cache.cpp is Windows-only (windows.h / xlcall.h)")
	}

	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ not on PATH; skipping xll_cache compile+run gate")
	}

	_, thisFile, ok := callerFile()
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// .../internal/assets/<file> -> repo root -> workspace root (holds types/).
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	typesSrc := os.Getenv("XLLGEN_TYPES_SRC")
	if typesSrc == "" {
		typesSrc = filepath.Join(filepath.Dir(repoRoot), "types")
	}
	typesInc := filepath.Join(typesSrc, "include")
	if _, err := os.Stat(filepath.Join(typesInc, "types", "xlcall.h")); err != nil {
		t.Skipf("types headers not found under %s; skipping (set XLLGEN_TYPES_SRC)", typesInc)
	}

	// flatbuffers (pulled in by types/converters.h) and phmap live in the shared
	// C++-gate FetchContent cache.
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	fcBase := filepath.Join(cacheDir, "xll-gen", "cpp_gate_fetch")
	fbInc := filepath.Join(fcBase, "flatbuffers-src", "include")
	phmapInc := filepath.Join(fcBase, "phmap-src")
	for _, probe := range []struct{ path, what string }{
		{filepath.Join(fbInc, "flatbuffers", "flatbuffers.h"), "flatbuffers"},
		{filepath.Join(phmapInc, "parallel_hashmap", "phmap.h"), "phmap"},
	} {
		if _, err := os.Stat(probe.path); err != nil {
			t.Skipf("%s headers not found under %s; run a cmake C++ gate once to populate "+
				"the FetchContent cache, then re-run", probe.what, fcBase)
		}
	}

	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}

	dir := t.TempDir()
	incDir := filepath.Join(dir, "include")
	if err := os.MkdirAll(incDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The generated layout uses FLAT includes (AGENTS.md §16.3), so drop every
	// header the unit under test needs into one directory.
	for _, name := range []string{"xll_cache.h", "xll_log.h", "xll_excel.h"} {
		content, ok := m["include/"+name]
		if !ok {
			t.Fatalf("embedded include/%s not found in assets", name)
		}
		if err := os.WriteFile(filepath.Join(incDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cacheCpp := filepath.Join(dir, "xll_cache.cpp")
	content, ok := m["src/xll_cache.cpp"]
	if !ok {
		t.Fatalf("embedded src/xll_cache.cpp not found in assets")
	}
	if err := os.WriteFile(cacheCpp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	harness := filepath.Join(filepath.Dir(thisFile), "testdata", "cache_native_test.cpp")
	if _, err := os.Stat(harness); err != nil {
		t.Fatalf("harness %s missing: %v", harness, err)
	}

	exePath := filepath.Join(dir, "cache_native_test.exe")

	// gnu++17 (not c++17) and NOGDI mirror the real build: types/xlcall.h uses the
	// MS spellings `_cdecl`/`pascal`, which MinGW only defines outside
	// __STRICT_ANSI__, and windows.h's ERROR macro collides with LogLevel::ERROR
	// (CMakeLists.txt.tmpl defines NOGDI for exactly that reason).
	args := []string{
		"-std=gnu++17", "-O2", "-DNOGDI", "-DUNICODE", "-D_UNICODE", "-fexceptions",
		"-I", incDir,
		"-I", typesInc,
		"-I", fbInc,
		"-I", phmapInc,
		"-o", exePath,
		harness,
		cacheCpp,
		filepath.Join(typesSrc, "src", "utility.cpp"),
		filepath.Join(typesSrc, "src", "converters.cpp"),
		filepath.Join(typesSrc, "src", "mem.cpp"),
	}
	if out, err := exec.Command(gxx, args...).CombinedOutput(); err != nil {
		t.Fatalf("xll_cache native harness failed to compile: %v\n%s", err, out)
	}

	runArgs := []string{}
	if testing.Verbose() {
		runArgs = append(runArgs, "-bench")
	}
	out, err := exec.Command(exePath, runArgs...).CombinedOutput()
	t.Logf("cache_native_test output:\n%s", out)
	if err != nil {
		t.Fatalf("xll_cache native harness reported failures (or crashed): %v", err)
	}
	if !strings.Contains(string(out), "0 failures") {
		t.Fatalf("xll_cache native harness did not report 0 failures")
	}
}

// stripLineComments drops `//` line comments so a marker assertion cannot be
// satisfied (or tripped) by prose. The fix's own comments deliberately quote the
// buggy code they replaced, which would otherwise make the "is it gone?" checks
// useless. Good enough for these single-line C++ markers; block comments are not
// used in the spots under test.
func stripLineComments(src string) string {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if i := strings.Index(ln, "//"); i >= 0 {
			ln = ln[:i]
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// callerFile is runtime.Caller(1) for the calling test, isolated so the frame
// depth stays obvious.
func callerFile() (uintptr, string, bool) {
	pc, file, _, ok := runtime.Caller(1)
	return pc, file, ok
}
