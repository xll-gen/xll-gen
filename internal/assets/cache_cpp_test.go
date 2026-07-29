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
	if !strings.Contains(src, "return FormatHashToken(typeTag, h);") ||
		!strings.Contains(src, "HashXLOPERContentWithRefIdentity(px)") ||
		!strings.Contains(src, ": HashXLOPERContent(px);") {
		t.Errorf("ContentHashToken must hash the XLOPER12 directly (FormatHashToken over " +
			"HashXLOPERContent / HashXLOPERContentWithRefIdentity) rather than materializing " +
			"SerializeXLOPER's string first")
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

// TestRefIdentityFoldedForCoordinatePayloads pins that the digest keying a
// COORDINATE-shaped payload folds the reference identity.
//
// Regression it pins (HIGH, 2026-07-26): a `range` RTD argument's topic token was
// ContentHashToken('r', px), which coerced the reference and hashed only the CELL
// VALUES — but the payload shipped under that token is ConvertRange(px), i.e. the
// SHEET ID + RECT TABLE. Two DISTINCT ranges holding the same numbers therefore
// produced the SAME token, xll::SendRefCachePayloadOnce (xll_ipc.cpp) deduped the
// second ship on its per-cycle g_sentRefCache set, and pkg/server's
// ResolveRangeArg handed the handler the FIRST range's coordinates for the second
// argument — a silent wrong answer. The 'r' tag could not help: the digest never
// looked at coordinates at all. MakeCacheKey had the same defect for cached
// sync/async functions taking a `range`/`any` argument.
//
// The fix folds identity ONLY where the payload is coordinate-shaped ('r', 'a',
// and every reference arg in MakeCacheKey, which cannot see the declared type);
// 'g'/'n' payloads are cell values and stay purely content-addressed so
// AGENTS.md §19.3's "same grid -> same topic" dedup survives.
func TestRefIdentityFoldedForCoordinatePayloads(t *testing.T) {
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
	code := stripLineComments(src)

	for _, want := range []string{
		"uint64_t HashXLOPERWithRefIdentity(uint64_t seed, const XLOPER12* px)",
		"uint64_t HashXLOPERContentWithRefIdentity(const XLOPER12* px)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("xll_cache.cpp missing the identity-folding digest entry point %q", want)
		}
		if !strings.Contains(hdr, strings.TrimSuffix(want, ")")) {
			t.Errorf("xll_cache.h does not declare %q", want)
		}
	}

	// The tag -> digest selection must stay: 'r' (Range) and 'a' (ConvertAny of a
	// reference also emits a Range) ship COORDINATES.
	if !strings.Contains(code, "(typeTag == 'r' || typeTag == 'a')") {
		t.Errorf("ContentHashToken must select the identity-folding digest for the " +
			"coordinate-shaped payload tags ('r' range, 'a' any-of-a-reference): without " +
			"it two distinct ranges holding the same values share one topic token and the " +
			"second payload is deduped away")
	}

	// MakeCacheKey's RefCache computeFn must use the identity-folding digest too.
	if !strings.Contains(code, "return HashXLOPERContentWithRefIdentity(pRef); }") {
		t.Errorf("MakeCacheKey must digest reference args with HashXLOPERContentWithRefIdentity: " +
			"a `range`/`any` arg ships its COORDINATES, so a value-only key serves the result " +
			"computed for a different range whenever two ranges hold the same values")
	}

	// ...and the identity fold must cover BOTH reference shapes: xltypeRef carries
	// (idSheet, rect table), xltypeSRef carries a single rect and NO sheet id.
	if !strings.Contains(code, "uint64_t HashRefIdentityFields(uint64_t h, const XLOPER12* px, DWORD ty)") ||
		!strings.Contains(code, "h = Fnv1aU64(h, (uint64_t)px->val.mref.idSheet);") ||
		!strings.Contains(code, "const XLREF12& r = px->val.sref.ref;") {
		t.Errorf("the reference-identity fold must handle xltypeRef (idSheet + rect array) " +
			"and xltypeSRef (one rect, no sheet id) distinctly")
	}
}

// TestDigestPathBoundsTheCoercedArea pins the size PRE-FILTER on the digest path
// (HIGH, 2026-07-29).
//
// Regression it pins: HashXLOPERIntoDepth's xltypeRef/xltypeSRef branch coerced
// whatever reference Excel handed it to cell VALUES with NO area bound, and so did
// GetOrComputeRefHash's per-rect loop. kMaxGridArgCells was applied only in
// ConvertGridArg — which the generated wrapper reaches AFTER the digest
// (MakeCacheKey before ConvertGridArg for sync/async; ContentHashToken before the
// payload build for rtd / rtd-once). So `=CachedSumGrid($P:$R)` and
// `=RtdComposite($P:$R, ...)` asked Excel to materialize a 3,145,728-cell XLOPER12
// (~100 MB) and hashed all of it, once per cell using the range and once per
// recalculation, for an argument that was refused a few lines later. The rtd token
// is computed ABOVE the per-cycle `rcAlreadySent` dedup, so it paid that
// unconditionally on every recalc. AGENTS.md §19.5 names exactly this cost as the
// REASON the cheap pre-filter exists ("before xlCoerce is asked to materialize
// ~100 MB of XLOPER12 for an answer that is going to be thrown away"); it was
// simply never applied here.
//
// Two properties are asserted, both structural:
//   - the measurement happens BEFORE the coerce (otherwise it saves nothing), and
//   - the refusal folds HashRefIdentity — the SAME well-defined branch a coerce
//     failure takes — so two distinct oversized references still hash apart and
//     cannot alias one RefCache entry or one RTD topic.
//
// Behavioral coverage (zero Excel calls, the boundary, the union whose rects are
// individually small, distinctness) is in
// internal/assets/testdata/cache_native_test.cpp::TestOversizedRefIsNotCoerced.
func TestDigestPathBoundsTheCoercedArea(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["src/xll_cache.cpp"]
	if !ok {
		t.Fatalf("embedded src/xll_cache.cpp not found in assets")
	}
	code := stripLineComments(src)

	// 1. The streaming digest measures, then refuses, then (only otherwise)
	//    coerces. Assert the ORDER, since a check placed after the coerce would
	//    pass a naive substring search while saving nothing.
	iMeasure := strings.Index(code, "MeasureRefArg(px, &refCells, nullptr);")
	iRefuse := strings.Index(code, "if (refCells > kMaxGridArgCells) {")
	iCoerce := strings.Index(code, "xll::CallExcel(xlCoerce, &xVal, px, &xType)")
	if iMeasure < 0 || iRefuse < 0 {
		t.Fatalf("HashXLOPERIntoDepth does not pre-filter the reference area against " +
			"kMaxGridArgCells; a whole-column argument still makes Excel materialize " +
			"~100 MB of XLOPER12 for a digest that is thrown away")
	}
	if iCoerce < 0 {
		t.Fatalf("the xlCoerce call site moved; update this test")
	}
	if !(iMeasure < iRefuse && iRefuse < iCoerce) {
		t.Errorf("the area pre-filter must run BEFORE xlCoerce (measure=%d refuse=%d coerce=%d)",
			iMeasure, iRefuse, iCoerce)
	}

	// 2. GetOrComputeRefHash measures the WHOLE reference. Its loop hands ONE
	//    single-rect temporary per area to computeFn, so the per-rect filter above
	//    cannot see a union whose rects are each under the bound but whose total is
	//    far over it.
	if !strings.Contains(code, "MeasureRefArg(pRef, &totalCells, nullptr);") ||
		!strings.Contains(code, "if (totalCells > kMaxGridArgCells) {") {
		t.Errorf("GetOrComputeRefHash must measure the whole reference before its rect loop: " +
			"it splits an xltypeRef into one single-rect temporary per area, so a union of " +
			"individually-small rects otherwise slips past the per-rect bound")
	}

	// 3. The refusal folds the reference IDENTITY, reusing the coerce-failure
	//    branch. Anything less (a constant, or nothing) would collapse every
	//    oversized reference onto one digest — one RefCache entry, one RTD topic.
	for _, want := range []string{
		"h = HashRefIdentity(h, px, ty);",
		"return HashRefIdentity(kFnvBasis, pRef, refTy);",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("the oversized-reference fallback must fold the reference identity via %q", want)
		}
	}
}

// TestIterativeCalcGateWired pins the source-level wiring of the RefCache's
// iterative-calculation gate.
//
// Regression it pins (HIGH, correctness, 2026-07-26): the per-cycle RefCache is
// keyed on COORDINATES ((sheetId, rect)) and valued with the range's VALUE
// digest, and the ONLY place that clears it is HandleCalculationEnded. Measured
// against Excel 16.0.20131.20154, xleventCalculationEnded fires exactly ONCE per
// calculation cycle no matter how many ITERATIONS that cycle runs
// (MaxIterations=5 -> 5 UDF invocations + 1 event; a converging circular formula
// -> 14 invocations + 1 event; a plain non-circular recalc -> 1 invocation + 1
// event). So inside an iterative (circular-reference) calculation the coordinates
// of a range keep resolving to the FIRST pass's value digest even though the
// cells changed, MakeCacheKey returns a stale key, and a cache-enabled function
// is served its own first-pass result for the rest of the cycle.
//
// The gate is queried at calc end via GET.DOCUMENT(15) — macro-sheet only, which
// is why it has to sit in the event callback (a command context) and not in the
// `$`-registered wrapper — and bypasses only the MEMOIZATION, never the digest.
func TestIterativeCalcGateWired(t *testing.T) {
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
	events, ok := m["src/xll_events.cpp"]
	if !ok {
		t.Fatalf("embedded src/xll_events.cpp not found in assets")
	}
	code := stripLineComments(src)
	eventCode := stripLineComments(events)

	for _, want := range []string{
		"void RefreshIterativeCalcMode();",
		"void ForceRefreshIterativeCalcMode();",
		"void SetIterativeCalcMode(bool on);",
		"bool IterativeCalcMode() const;",
		"std::atomic<bool> iterativeCalc_{false};",
		"std::atomic<bool> refPathUsed_{false};",
	} {
		if !strings.Contains(hdr, want) {
			t.Errorf("xll_cache.h must declare %q (the iterative-calculation gate for the "+
				"per-cycle RefCache)", want)
		}
	}

	// The query itself: GET.DOCUMENT type_num 15 is the iteration flag.
	if !strings.Contains(code, "xll::CallExcel(xlfGetDocument, &xRes, 15)") {
		t.Errorf("RefreshIterativeCalcMode must read Excel's iteration flag with " +
			"GET.DOCUMENT(15); 16/17 are MaxIterations/MaxChange and are NOT the gate")
	}

	// The bypass must skip the MAP, not the hash: the key bytes have to stay
	// identical in both modes, otherwise flipping the gate silently invalidates
	// every cache entry and every RTD topic token derived from a ref argument.
	if !strings.Contains(code, "const bool bypassMemo = iterativeCalc_.load(std::memory_order_acquire);") ||
		!strings.Contains(code, "if (!bypassMemo) refCache_.insert_or_assign(key, hashVal);") ||
		!strings.Contains(code, "if (!bypassMemo) {") {
		t.Errorf("GetOrComputeRefHash must gate the RefCache lookup AND the store on " +
			"bypassMemo while still folding computeFn's digest into the accumulator " +
			"(the gate drops the memoization, it must never change the digest)")
	}

	// The "ref path was used" flag has to be raised even while bypassing, or the
	// calc-end query stops running and the gate can never be turned back off.
	if !strings.Contains(code, "refPathUsed_.store(true, std::memory_order_release);") {
		t.Errorf("GetOrComputeRefHash must mark the RefCache path as used before the " +
			"bypass check, otherwise RefreshIterativeCalcMode stops querying Excel once " +
			"the gate is on and it can never be cleared again")
	}
	if !strings.Contains(code, "if (!refPathUsed_.exchange(false, std::memory_order_acquire)) return;") {
		t.Errorf("RefreshIterativeCalcMode must consume the refPathUsed_ flag so projects " +
			"that never route a reference argument through the RefCache pay no extra " +
			"Excel round-trip at calc end")
	}

	// Wiring + ORDER in the calc-end handler.
	//
	// NOTE ON WHAT THIS ORDER ASSERTION MEANS. The two calls are currently
	// INDEPENDENT: ClearRefCache() only empties refCache_ — it does not touch
	// refPathUsed_ or iterativeCalc_ — so swapping them would not change
	// behavior. (An earlier version of this comment, and of the one in
	// xll_events.cpp, claimed the clear "consumes" the flag; it does not.) The
	// assertion is kept as an INTENT pin, not a correctness proof: the refresh
	// is the decision about the cycle just observed and the clear is the cycle
	// boundary, so the refresh belongs before it. If a future change ever makes
	// ClearRefCache reset the gate flags, this ordering becomes load-bearing —
	// which is the other reason to freeze it now.
	refresh := strings.Index(eventCode, "CacheManager::Instance().RefreshIterativeCalcMode();")
	clear := strings.Index(eventCode, "CacheManager::Instance().ClearRefCache();")
	if refresh < 0 {
		t.Errorf("HandleCalculationEnded must call CacheManager::RefreshIterativeCalcMode(): " +
			"it is the only command context available once per calculation cycle, and " +
			"GET.DOCUMENT cannot be called from the thread-safe UDF wrappers")
	}
	if clear < 0 {
		t.Fatalf("HandleCalculationEnded no longer clears the RefCache")
	}
	if refresh >= 0 && refresh > clear {
		t.Errorf("RefreshIterativeCalcMode must run BEFORE ClearRefCache in " +
			"HandleCalculationEnded (intent pin: refresh decides on the cycle just " +
			"observed, the clear ends it)")
	}
}

// TestIterativeCalcGatePrimedAtLoad pins that the iterative-calculation gate is
// primed ONCE at xlAutoOpen, not only at calc end.
//
// Regression it pins (MED, 2026-07-26): the gate was refreshed exclusively in
// HandleCalculationEnded, so a workbook SAVED with iterative calculation enabled
// opened with the gate OFF and its FIRST recalculation memoized pass-1 reference
// digests across every iteration of the cycle — the exact stale-value symptom
// the gate exists to eliminate — and the wrong result then survived for the rest
// of the session unless the cell was dirtied again. xlAutoOpen is a valid
// command context for the macro-sheet-only GET.DOCUMENT, and a failed query
// leaves the gate untouched (the previous behavior), so priming there adds no
// risk.
func TestIterativeCalcGatePrimedAtLoad(t *testing.T) {
	tmpl, err := templates.Get("xll_main.cpp.tmpl")
	if err != nil {
		t.Fatalf("templates.Get(xll_main.cpp.tmpl): %v", err)
	}
	if !strings.Contains(tmpl, "ForceRefreshIterativeCalcMode()") {
		t.Fatalf("xll_main.cpp.tmpl must prime the iterative-calculation gate with " +
			"CacheManager::Instance().ForceRefreshIterativeCalcMode() at xlAutoOpen; " +
			"refreshing only at calc end loses the first calculation cycle of a " +
			"workbook saved with iteration enabled")
	}
	open := strings.Index(tmpl, "int __stdcall xlAutoOpen()")
	prime := strings.Index(tmpl, "ForceRefreshIterativeCalcMode()")
	if open < 0 {
		t.Fatalf("xll_main.cpp.tmpl no longer defines xlAutoOpen")
	}
	if prime < open {
		t.Errorf("the gate prime must sit INSIDE xlAutoOpen (a valid command context " +
			"for GET.DOCUMENT), not before it")
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
