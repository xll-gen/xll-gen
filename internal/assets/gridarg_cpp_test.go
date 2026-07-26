package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/templates"
)

// ---------------------------------------------------------------------------
// Always-on source markers (no toolchain required)
// ---------------------------------------------------------------------------

// TestSHMAllocatorNeverReturnsNullIntoFlatbuffers pins the actual crash
// mechanism behind "a whole-column reference into a `grid` argument kills
// Excel".
//
// flatbuffers::Allocator has NO failure channel. Its base-class
// reallocate_downward does
//
//	uint8_t* new_p = allocate(new_size);
//	memcpy_downward(old_p, old_size, new_p, new_size, back, front);
//
// and memcpy_downward memcpy's into `new_p + new_size - back` unconditionally.
// SHMAllocator::allocate answered nullptr for anything larger than the slot's
// request buffer, so an over-capacity request did not fail the build — it
// memcpy'd into a near-null address and took the Excel PROCESS down.
//
// That is why the reported crash reproduces far below "3.1M cells": measured on
// the shipped showcase build, 120x120 = 14,400 cells returned the right answer
// and 140x140 = 19,600 cells died — precisely where protocol::Grid's ~28 bytes
// per cell stops fitting a 512 KiB request buffer.
func TestSHMAllocatorNeverReturnsNullIntoFlatbuffers(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["include/SHMAllocator.h"]
	if !ok {
		t.Fatalf("embedded include/SHMAllocator.h not found in assets")
	}
	code := stripLineComments(src)

	// The pre-fix body verbatim. Its return of nullptr is the defect.
	if strings.Contains(code, "if (size > size_) {") && strings.Contains(code, "return nullptr;") &&
		!strings.Contains(code, "overflowed_") {
		t.Errorf("SHMAllocator::allocate returns nullptr for an over-capacity request again: " +
			"flatbuffers' Allocator::reallocate_downward memcpy's into whatever allocate() " +
			"returns, so this is an access violation inside the Excel process, not an error")
	}

	for _, want := range []string{
		"bool overflowed_ = false;",
		"bool Overflowed() const { return overflowed_; }",
		"overflowed_ = true;",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("SHMAllocator must latch an over-capacity request in a sticky flag "+
				"(%q missing) so the caller can refuse the call instead of sending "+
				"bytes that are not in shared memory", want)
		}
	}

	// deallocate must free heap fallbacks but never the shared-memory buffer.
	if !strings.Contains(code, "if (p != nullptr && p != buffer_) {") {
		t.Errorf("SHMAllocator::deallocate must free heap fallbacks and skip the " +
			"shared-memory buffer; freeing buffer_ corrupts the heap and leaking every " +
			"fallback turns a refused call into a memory leak")
	}
}

// TestGridArgGuardsDeclared pins the reference-shape guards on the `grid`
// argument path.
//
// Regressions (both reproduced against the shipped showcase, Excel
// 16.0.20131.20154, cache disabled):
//
//   - HIGH: =SumGrid($P:$R) killed the Excel process. Bounding the reference
//     AREA before xlCoerce keeps a pathological argument from ever reaching the
//     builder (and from making Excel materialize ~100 MB of XLOPER12 for an
//     answer that is going to be thrown away).
//   - MED: =SumGrid(($P$1:$R$5,$P$6:$R$10)) returned 0 while the contiguous
//     $P$1:$R$10 returned 2805. xlCoerce cannot flatten a union reference; the
//     error value it answers with used to be wrapped by ConvertGrid as a 1x1
//     grid the handler summed to 0 — no crash, no error, a wrong number.
func TestGridArgGuardsDeclared(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	hdr, ok := m["include/xll_cache.h"]
	if !ok {
		t.Fatalf("embedded include/xll_cache.h not found in assets")
	}
	src, ok := m["src/xll_cache.cpp"]
	if !ok {
		t.Fatalf("embedded src/xll_cache.cpp not found in assets")
	}
	code := stripLineComments(src)

	for _, want := range []string{
		"enum class GridArgStatus {",
		"kMultiArea,",
		"kTooLarge,",
		"kNotAnArray,",
		"constexpr uint64_t kMaxGridArgCells = 16384;",
		"void MeasureRefArg(const XLOPER12* op, uint64_t* outCells, uint32_t* outAreas);",
		"const char* GridArgStatusText(GridArgStatus s);",
	} {
		if !strings.Contains(hdr, want) {
			t.Errorf("xll_cache.h must declare %q", want)
		}
	}

	// The bool out-param carried no reason, so the wrapper could only ever log
	// "coerce failed" — for a refusal that never called Excel.
	if strings.Contains(hdr, "bool* coerceOk") {
		t.Errorf("ConvertGridArg still takes a bool* coerceOk: a boolean cannot " +
			"distinguish a coerce failure from a union reference or an over-large " +
			"area, and each needs a different diagnostic")
	}

	// The two guards must run BEFORE the coerce, in that order.
	iMeasure := strings.Index(code, "MeasureRefArg(op, &cells, &areas);")
	iMulti := strings.Index(code, "if (areas > 1) return refuse(GridArgStatus::kMultiArea);")
	iTooBig := strings.Index(code, "if (cells > maxCells) return refuse(GridArgStatus::kTooLarge);")
	iCoerce := strings.Index(code, "xll::CallExcel(xlCoerce, &xVal, op, &xType)")
	switch {
	case iMeasure < 0:
		t.Errorf("ConvertGridArg must measure the reference shape (MeasureRefArg) " +
			"before deciding whether to coerce it")
	case iMulti < 0:
		t.Errorf("ConvertGridArg must refuse a multi-area (union) reference: xlCoerce " +
			"cannot produce the union and a protocol::Grid is one rectangle, so the " +
			"old code shipped a 1x1 error grid the handler summed to 0")
	case iTooBig < 0:
		t.Errorf("ConvertGridArg must refuse a reference whose area exceeds maxCells")
	case iCoerce < 0:
		t.Fatalf("ConvertGridArg no longer coerces the reference at all")
	case iMulti > iCoerce || iTooBig > iCoerce:
		t.Errorf("the multi-area and size guards must run BEFORE xlCoerce: the whole " +
			"point is to not ask Excel to materialize ~100 MB of XLOPER12 for an " +
			"argument that is going to be refused")
	}

	// And the post-coerce type check, which is the guard that does not depend on
	// how Excel chooses to report a shape it cannot flatten.
	if !strings.Contains(code, "if (vt != xltypeMulti) {") ||
		!strings.Contains(code, "return refuse(GridArgStatus::kNotAnArray);") {
		t.Errorf("ConvertGridArg must verify that xlCoerce actually answered with the " +
			"xltypeMulti it asked for: ConvertGrid's non-multi fall-through wraps an " +
			"error value as a 1x1 grid, which the handler reads as data")
	}
}

// TestGridArgRefusalReachesTheCell pins that the generated wrapper turns a
// ConvertGridArg refusal into #VALUE! (and an RTD payload SKIP), and that it
// refuses to send a request that overflowed the slot arena.
//
// Without the wrapper half, the runtime guards are inert: ConvertGridArg would
// hand back an empty grid and the wrapper would ship it as if it were the
// user's data.
func TestGridArgRefusalReachesTheCell(t *testing.T) {
	tmpl, err := templates.Get("xll_main.cpp.tmpl")
	if err != nil {
		t.Fatalf("templates.Get(xll_main.cpp.tmpl): %v", err)
	}

	for _, want := range []string{
		// sync/async wrapper
		"xll::GridArgStatus gridStatus{{$j}} = xll::GridArgStatus::kOk;",
		"auto arg{{$j}} = xll::ConvertGridArg({{.Name}}, builder, &gridStatus{{$j}});",
		"if (gridStatus{{$j}} != xll::GridArgStatus::kOk) {",
		"xll::GridArgStatusText(gridStatus{{$j}})",
		// rtd / rtd-once content-hash ship
		"rcOk = (rcStatus == xll::GridArgStatus::kOk);",
		// slot-arena overflow
		"if (allocator.Overflowed()) {",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("xll_main.cpp.tmpl missing %q", want)
		}
	}

	// The old boolean plumbing must be gone everywhere, or a refusal silently
	// becomes a successful conversion of an empty grid.
	if strings.Contains(tmpl, "gridCoerceOk") || strings.Contains(tmpl, "ConvertGridArg({{.Name}}, rcb, &rcOk)") {
		t.Errorf("xll_main.cpp.tmpl still uses the bool coerceOk plumbing for a grid arg")
	}

	// The overflow check must sit between Finish() and Send(): checking after
	// the send is pointless, and checking before Finish() misses the last grow.
	iFinish := strings.Index(tmpl, "builder.Finish(req);")
	iOverflow := strings.Index(tmpl, "if (allocator.Overflowed()) {")
	iSend := strings.Index(tmpl, "auto res = slot.Send(-((int)builder.GetSize())")
	if iFinish < 0 || iOverflow < 0 || iSend < 0 {
		t.Fatalf("xll_main.cpp.tmpl: could not locate Finish/Overflowed/Send (%d/%d/%d)",
			iFinish, iOverflow, iSend)
	}
	if !(iFinish < iOverflow && iOverflow < iSend) {
		t.Errorf("the allocator.Overflowed() check must sit between builder.Finish() and " +
			"slot.Send(): the request is only complete after Finish, and after Send the " +
			"bytes have already left")
	}
}

// TestSlotArenaSendersCheckOverflow is the completeness gate for the fix above:
// EVERY site that builds a FlatBuffers request into a slot's request buffer and
// then sends it must consult allocator.Overflowed() first.
//
// A missed site is worse than the crash this change removes: the payload lives
// on the heap fallback, so `slot.Send(-size, ...)` would publish whatever
// happens to be in shared memory, with a length that does not describe it.
func TestSlotArenaSendersCheckOverflow(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	tmpl, err := templates.Get("xll_main.cpp.tmpl")
	if err != nil {
		t.Fatalf("templates.Get(xll_main.cpp.tmpl): %v", err)
	}

	sources := map[string]string{
		"xll_main.cpp.tmpl": tmpl,
	}
	for name, content := range m {
		if strings.HasSuffix(name, ".cpp") || strings.HasSuffix(name, ".h") {
			sources[name] = content
		}
	}

	for name, content := range sources {
		code := stripLineComments(content)
		nAlloc := strings.Count(code, "SHMAllocator allocator(")
		if nAlloc == 0 {
			continue
		}
		// Only builders that actually SEND need the guard; a site that builds
		// without sending cannot ship anything.
		if !strings.Contains(code, ".Send(") {
			continue
		}
		nGuard := strings.Count(code, "allocator.Overflowed()")
		if nGuard < nAlloc {
			t.Errorf("%s constructs %d SHMAllocator(s) over a slot arena and sends, but "+
				"checks allocator.Overflowed() only %d time(s): an unchecked site sends "+
				"a length that does not describe the shared-memory bytes",
				name, nAlloc, nGuard)
		}
	}
}

// ---------------------------------------------------------------------------
// Compile + run gate
// ---------------------------------------------------------------------------

// TestGridArgNativeBehavior compiles the EMBEDDED xll_cache.{h,cpp} +
// SHMAllocator.h together with testdata/gridarg_native_test.cpp and runs it.
//
// The harness supplies its own Excel12v (xlCoerce/xlFree only), so the whole
// `grid`-argument path is exercised without Excel:
//
//   - MeasureRefArg over SRef / multi-rect Ref / non-reference / inverted rect.
//   - the control case (a contiguous reference still converts to its values).
//   - the oversized reference is refused WITHOUT calling xlCoerce.
//   - the multi-area reference is refused structurally, and a coerce that
//     answers a non-xltypeMulti is refused instead of becoming a 1x1 grid.
//   - SHMAllocator: an over-capacity build latches Overflowed() instead of
//     handing flatbuffers a nullptr to memcpy into. On the parent commit this
//     case CRASHES the harness process, which is the offline equivalent of the
//     reported Excel death.
//
// Requires g++ plus the shared C++-gate FetchContent cache (flatbuffers) and a
// types checkout; skipped (not failed) when absent, and under -short. Run this
// gate SOLO with -run: the C++ gates share a FetchContent cache and contend.
func TestGridArgNativeBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping C++ compile+run gate in short mode")
	}
	if runtime.GOOS != "windows" {
		t.Skip("xll_cache.cpp is Windows-only (windows.h / xlcall.h)")
	}

	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ not on PATH; skipping grid-arg compile+run gate")
	}

	_, thisFile, ok := callerFile()
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	typesSrc := os.Getenv("XLLGEN_TYPES_SRC")
	if typesSrc == "" {
		typesSrc = filepath.Join(filepath.Dir(repoRoot), "types")
	}
	typesInc := filepath.Join(typesSrc, "include")
	if _, err := os.Stat(filepath.Join(typesInc, "types", "xlcall.h")); err != nil {
		t.Skipf("types headers not found under %s; skipping (set XLLGEN_TYPES_SRC)", typesInc)
	}

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
	// Flat includes (AGENTS.md §16.3).
	for _, name := range []string{"xll_cache.h", "xll_log.h", "xll_excel.h", "SHMAllocator.h"} {
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

	harness := filepath.Join(filepath.Dir(thisFile), "testdata", "gridarg_native_test.cpp")
	if _, err := os.Stat(harness); err != nil {
		t.Fatalf("harness %s missing: %v", harness, err)
	}

	exePath := filepath.Join(dir, "gridarg_native_test.exe")
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
		t.Fatalf("grid-arg native harness failed to compile: %v\n%s", err, out)
	}

	out, err := exec.Command(exePath).CombinedOutput()
	t.Logf("gridarg_native_test output:\n%s", out)
	if err != nil {
		t.Fatalf("grid-arg native harness reported failures (or crashed): %v", err)
	}
	if !strings.Contains(string(out), "0 failures") {
		t.Fatalf("grid-arg native harness did not report 0 failures")
	}
}
