package assets

import (
	"strings"
	"testing"
)

// TestCallExcelKeeperPassThrough guards the xll_excel.h helper that lets a live
// ScopedXLOPER12 lvalue (e.g. the xlfCaller result reused in a follow-up Excel
// call) be passed to xll::CallExcel without taking its address.
//
// Regression context: the caller-aware (caller:true) codegen path passes a
// ScopedXLOPER12 by value into CallExcel. ScopedXLOPER12 is move-only, so the
// generic make_keeper(T&&) -> ScopedXLOPER12(...) wrapper hits the deleted copy
// constructor and the generated xll_main.cpp fails to compile. The dedicated
// make_keeper(ScopedXLOPER12&) overload (which extracts .get()) is what makes
// that path build. Removing it silently breaks every caller-aware add-in.
func TestCallExcelKeeperPassThrough(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["include/xll_excel.h"]
	if !ok {
		t.Fatalf("embedded include/xll_excel.h not found in assets")
	}
	if !strings.Contains(src, "make_keeper(ScopedXLOPER12&") {
		t.Errorf("xll_excel.h missing make_keeper(ScopedXLOPER12&) pass-through; " +
			"a live ScopedXLOPER12 passed to CallExcel would hit the deleted copy ctor")
	}
}

// TestRtdUpdateErrorStoresTransient guards the ProcessRtdUpdate ERROR GATE:
// an RtdUpdate flagged is_error=true must be handed to
// RtdOnceRegistry::StoreResult as a TRANSIENT entry — NOT skipped.
//
// Regression it pins (HIGH, found by review 2026-07-24): an earlier revision
// SKIPPED StoreResult for is_error updates. That looked safe but wedged the cell
// permanently, because the scalar rtd-once wrapper only ever returns a value on
// a TryGetResult HIT — on a miss it calls xlfRtd, discards the synchronous
// result and returns the loading placeholder. With no stored entry every recalc
// missed and re-painted the placeholder while the topic stayed CONNECTED, so
// ConnectData never re-fired and the one-shot handler never re-ran: the cell was
// stuck at #GETTING_DATA forever. Storing it as transient paints the error AND
// lets ClearNonMemoized reclaim it (bypassing memoize/memoize_ttl) so the next
// recalc recomputes. See xll-gen AGENTS.md §19.3 and types RtdUpdate.is_error.
func TestRtdUpdateErrorStoresTransient(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["src/xll_rtd.cpp"]
	if !ok {
		t.Fatalf("embedded src/xll_rtd.cpp not found in assets")
	}
	// is_error must flow into StoreResult's transient parameter, not into the
	// guard that decides whether to call it at all.
	if !strings.Contains(src, "/*transient=*/update->is_error()") {
		t.Errorf("xll_rtd.cpp must pass update->is_error() as StoreResult's transient flag " +
			"(`/*transient=*/update->is_error()`); routing it into the call GUARD instead " +
			"leaves the cell permanently stuck at #GETTING_DATA")
	}
	// The only gate left on StoreResult is the grid-once one.
	if !strings.Contains(src, "if (!isGridOnce) {") {
		t.Errorf("xll_rtd.cpp StoreResult must be gated ONLY by !isGridOnce; " +
			"an extra error term would resurrect the stuck-at-#GETTING_DATA regression")
	}
	if strings.Contains(src, "!isGridOnce && !isError") {
		t.Errorf("xll_rtd.cpp still skips StoreResult for is_error updates " +
			"(`!isGridOnce && !isError`): the cell would stick at #GETTING_DATA forever " +
			"because the topic stays connected and ConnectData never re-fires")
	}
}

// TestRtdOnceRegistryTransientRetention pins the transient RETENTION contract in
// the header: a transient entry must be reclaimed like a plain `once` entry even
// when the function declares memoize:true / memoize_ttl. The behavioral proof is
// TestRtdOnceRegistryTransientBehavior (a real compile+run of the header); this
// marker test is the cheap always-on guard that the override branch exists at
// all, and that neither expiry path lost it.
func TestRtdOnceRegistryTransientRetention(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["include/xll_rtd_once.h"]
	if !ok {
		t.Fatalf("embedded include/xll_rtd_once.h not found in assets")
	}
	if !strings.Contains(src, "bool transient = false;") {
		t.Errorf("xll_rtd_once.h Entry is missing the `transient` flag")
	}
	if !strings.Contains(src, "void StoreResult(const std::wstring& key, const VARIANT& value, bool transient = false)") {
		t.Errorf("xll_rtd_once.h StoreResult must accept a defaulted `bool transient` parameter")
	}
	// ClearNonMemoized: the transient check must come BEFORE the memoize /
	// memoize_ttl branches, otherwise memoize:true wins and the error sticks.
	clearIdx := strings.Index(src, "void ClearNonMemoized()")
	if clearIdx < 0 {
		t.Fatalf("xll_rtd_once.h ClearNonMemoized not found")
	}
	body := src[clearIdx:]
	transIdx := strings.Index(body, "it->second.transient")
	memoIdx := strings.Index(body, "m_memoizeNames.count(fn)")
	if transIdx < 0 {
		t.Fatalf("ClearNonMemoized does not consult Entry::transient; a memoize:true " +
			"function would freeze a transient error until XLL reload")
	}
	if memoIdx < 0 {
		t.Fatalf("ClearNonMemoized no longer consults m_memoizeNames")
	}
	if transIdx > memoIdx {
		t.Errorf("ClearNonMemoized checks m_memoizeNames before Entry::transient; the " +
			"transient override must come FIRST or memoize:true keeps the error forever")
	}
	// TryGetResult: read-side transient miss once the topic is gone.
	getIdx := strings.Index(src, "bool TryGetResult(")
	if getIdx < 0 {
		t.Fatalf("xll_rtd_once.h TryGetResult not found")
	}
	if !strings.Contains(src[getIdx:], "it->second.transient && !KeyHasLiveTopic(key)") {
		t.Errorf("TryGetResult must report a MISS for a transient entry whose topic is gone " +
			"(`it->second.transient && !KeyHasLiveTopic(key)`), else a stale error is " +
			"re-served for an extra recalc cycle when CalculationEnded beats DisconnectData")
	}
}
