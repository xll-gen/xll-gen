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
	// The only gate left on the SCALAR StoreResult is the grid-once split: a
	// grid-once topic is routed to RtdOnceGridRegistry instead (StoreError),
	// every other topic stores unconditionally.
	if !strings.Contains(src, "if (isGridOnce) {") || !strings.Contains(src, "} else {") {
		t.Errorf("xll_rtd.cpp StoreResult must be gated ONLY by the isGridOnce routing split; " +
			"an extra error term would resurrect the stuck-at-#GETTING_DATA regression")
	}
	if strings.Contains(src, "!isGridOnce && !isError") {
		t.Errorf("xll_rtd.cpp still skips StoreResult for is_error updates " +
			"(`!isGridOnce && !isError`): the cell would stick at #GETTING_DATA forever " +
			"because the topic stays connected and ConnectData never re-fires")
	}
	// The grid-once branch must not be a silent drop: an is_error update for a
	// grid-once topic is the ONLY thing that topic will ever deliver, so it has to
	// land in the grid registry as a transient error. Dropping it leaves the cell
	// frozen on the loading placeholder with no self-heal (the pre-2026-07-26
	// behavior AGENTS.md §19.3 documented as an open follow-up).
	if !strings.Contains(src, "RtdOnceGridRegistry::Instance().StoreError(gridKey, errText)") {
		t.Errorf("xll_rtd.cpp must route an is_error update for a GRID-once topic into " +
			"RtdOnceGridRegistry::StoreError; without it the grid/numgrid cell keeps the " +
			"loading placeholder forever and never retries")
	}
	if !strings.Contains(src, "if (update->is_error()) {") {
		t.Errorf("xll_rtd.cpp grid-once branch must key the error store off update->is_error(); " +
			"storing the readiness token as an error would mask the delivered grid")
	}
}

// TestRtdOnceGridRegistryTransientRetention is the grid twin of
// TestRtdOnceRegistryTransientRetention: it pins the TRANSIENT (error) entry
// contract in the grid registry header. The behavioral proof is
// TestRtdOnceGridRegistryErrorBehavior (a real compile+run of the header); this
// marker test is the cheap always-on guard that the override branch exists at
// all, and that neither expiry path lost it.
func TestRtdOnceGridRegistryTransientRetention(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["include/xll_rtd_once_grid.h"]
	if !ok {
		t.Fatalf("embedded include/xll_rtd_once_grid.h not found in assets")
	}
	for _, want := range []string{
		// The lookup is three-valued: a bool cannot distinguish "error" from
		// "zero-byte payload", and GetRoot over zero bytes is UB.
		"enum class OnceGridLookup {",
		"kMiss = 0,",
		"kResult,",
		"kError,",
		"void StoreError(const std::wstring& key, const std::wstring& text)",
		"OnceGridLookup TryGet(const std::wstring& key, std::vector<uint8_t>* out,",
		"bool transient = false;",
		"std::wstring errorText;",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("xll_rtd_once_grid.h missing %q", want)
		}
	}
	// ClearNonMemoized: the transient check must come BEFORE the memoize /
	// memoize_ttl branches, otherwise memoize:true wins and the error sticks.
	clearIdx := strings.Index(src, "void ClearNonMemoized()")
	if clearIdx < 0 {
		t.Fatalf("xll_rtd_once_grid.h ClearNonMemoized not found")
	}
	body := src[clearIdx:]
	transIdx := strings.Index(body, "it->second.transient")
	memoIdx := strings.Index(body, "m_memoizeNames.count(fn)")
	if transIdx < 0 {
		t.Fatalf("ClearNonMemoized does not consult Entry::transient; a memoize:true " +
			"grid function would freeze a transient error until XLL reload")
	}
	if memoIdx < 0 {
		t.Fatalf("ClearNonMemoized no longer consults m_memoizeNames")
	}
	if transIdx > memoIdx {
		t.Errorf("ClearNonMemoized checks m_memoizeNames before Entry::transient; the " +
			"transient override must come FIRST or memoize:true keeps the error forever")
	}
	// TryGet: read-side transient miss once the topic is gone.
	getIdx := strings.Index(src, "OnceGridLookup TryGet(")
	if getIdx < 0 {
		t.Fatalf("xll_rtd_once_grid.h TryGet not found")
	}
	if !strings.Contains(src[getIdx:], "it->second.transient && !KeyHasLiveTopic(key)") {
		t.Errorf("TryGet must report a MISS for a transient entry whose topic is gone " +
			"(`it->second.transient && !KeyHasLiveTopic(key)`), else a stale error is " +
			"re-served for an extra recalc cycle when CalculationEnded beats DisconnectData")
	}
	// Store must PROMOTE: a completed payload landing over a transient error
	// restores normal (memoize/ttl) retention.
	storeIdx := strings.Index(src, "void Store(const std::wstring& key, const uint8_t* data, size_t len)")
	if storeIdx < 0 {
		t.Fatalf("xll_rtd_once_grid.h Store not found")
	}
	if !strings.Contains(src[storeIdx:storeIdx+1400], "it->second.transient = false;") {
		t.Errorf("Store must re-stamp transient=false; a retry that SUCCEEDS would otherwise " +
			"keep being reclaimed as if it were still the error")
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

// TestOnTimeMacroNameLiterals pins the two OnTime macro-name literals in
// include/xll_deferred_commands.h (drift guard, item 2e / 2026-07-26).
//
// These accessors are the SINGLE SOURCE OF TRUTH shared by three consumers: the
// template's xlfRegister call, its xlcOnTime schedule/re-arm, and the exported
// C symbol Excel resolves the registered procedure against. Nothing structurally
// couples the string to the export — rename one side and the C++ still compiles,
// every name-grepping generator test still passes, and the only symptom is a
// runtime "cannot resolve ON.TIME macro": the ribbon tab never appears, or the
// deferred calc-end command drain never runs.
//
// This test pins the literals themselves AND the exact accessor shape that
// internal/generator's TestOnTimeMacroNameExportsMatchHeaderLiterals parses to
// derive the expected export symbol. Reshaping the accessor (e.g. to a constexpr
// variable) must update that parser too, so it fails here first.
func TestOnTimeMacroNameLiterals(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["include/xll_deferred_commands.h"]
	if !ok {
		t.Fatalf("embedded include/xll_deferred_commands.h not found in assets")
	}
	for _, tc := range []struct{ accessor, literal string }{
		{"DeferredRunnerMacroName", "__xllgen_RunDeferredCalcEnd"},
		{"RibbonConnectRetryMacroName", "__xllgen_RibbonConnectRetry"},
	} {
		decl := "inline const wchar_t* " + tc.accessor + "() {"
		if !strings.Contains(src, decl) {
			t.Errorf("xll_deferred_commands.h missing the %q accessor in its pinned shape (%q); "+
				"the generator's export cross-check parses exactly this shape", tc.accessor, decl)
			continue
		}
		body := src[strings.Index(src, decl)+len(decl):]
		end := strings.Index(body, "}")
		if end < 0 {
			t.Fatalf("%s(): unterminated body", tc.accessor)
		}
		want := `return L"` + tc.literal + `";`
		if !strings.Contains(body[:end], want) {
			t.Errorf("%s() must return L%q (got body %q). The exported C symbol in "+
				"internal/templates/xll_main.cpp.tmpl is matched against this literal; a "+
				"one-sided rename is invisible to the compiler and to every generator test, "+
				"and shows up only as an unresolvable ON.TIME macro at runtime.",
				tc.accessor, tc.literal, strings.TrimSpace(body[:end]))
		}
	}
}

// TestScheduleOnTimeMacroGuards pins three hardening fixes on the generic
// xlcOnTime scheduler in src/xll_deferred_commands.cpp (2026-07-26):
//
//  1. UNLOAD SELF-GATE (§20). ScheduleOnTimeMacro is exported from the header as
//     general-purpose API. Both of today's callers gate on g_isUnloading, but the
//     function itself did not — so the "never issue an Excel C-API command during
//     teardown" rule rested on an unenforced caller contract. A schedule placed
//     during teardown can only ever become a leaked OnTime dispatch. The gate is
//     now structural and local to the API.
//
//  2. FAILURE LOGS ARE WARN, NOT INFO. A rejected xlcOnTime silently kills any
//     self-re-arming chain built on this helper (the ribbon-connect retry is
//     exactly that), so it is an operator-visible event, not a debug note.
//
//  3. The "xlfNow succeeded but returned a non-numeric operand" branch logged
//     `nowRc` — which is 0 there — rendering as "rc=0 (xlretSuccess)" and reading
//     as if the call had worked. The returned xltype is logged alongside it,
//     because that is the only thing distinguishing the two failure shapes.
//
// And one from 2026-08-03:
//
//  4. THE GATE RETURNS A SENTINEL, NOT xlretFailed. Guard (1) made the teardown
//     no-op look exactly like a genuine Excel rejection, and guard (2) made
//     callers shout about it — so an orderly shutdown that caught a pending
//     ribbon connect logged "the retry chain ENDS here", a WARN describing a bug
//     that was not happening, in the log an operator reads for the real ones.
//     kOnTimeNotScheduledTeardown is negative, so it cannot collide with an
//     xlret (all non-negative bit values) and a caller that forgets to
//     special-case it still reads "not scheduled".
func TestScheduleOnTimeMacroGuards(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["src/xll_deferred_commands.cpp"]
	if !ok {
		t.Fatalf("embedded src/xll_deferred_commands.cpp not found in assets")
	}
	idx := strings.Index(src, "int ScheduleOnTimeMacro(const wchar_t* macroName, double delaySeconds) {")
	if idx < 0 {
		t.Fatalf("ScheduleOnTimeMacro not found in xll_deferred_commands.cpp")
	}
	end := strings.Index(src[idx:], "\nvoid DeferCalcEndCommands(")
	if end < 0 {
		t.Fatalf("could not delimit the ScheduleOnTimeMacro body")
	}
	body := src[idx : idx+end]

	// (1) The unload gate must be the FIRST thing the body does — before any
	//     Excel C-API call (xlfNow is already a C-API call).
	// TeardownStarted() == g_isUnloading || g_isQuiescing. The quiesce half is
	// load-bearing since the 2026-07-29 close-time fix: a schedule placed during
	// the teardown's Phase 1 (which latches g_isQuiescing but deliberately leaves
	// g_isUnloading false so DisconnectData keeps working) is just as certainly a
	// leaked OnTime dispatch as one placed during Phase 2. Gating on g_isUnloading
	// alone would let it through. See xll_lifecycle.h.
	gate := "if (xll::TeardownStarted()) return kOnTimeNotScheduledTeardown;"
	if !strings.Contains(body, gate) {
		t.Errorf("ScheduleOnTimeMacro is missing its own §20 teardown self-gate (%q); "+
			"as public API it must not be able to place an Excel C-API command during "+
			"teardown just because a caller forgot to gate", gate)
	} else if gi, ni := strings.Index(body, gate), strings.Index(body, "xll::CallExcel(xlfNow"); ni >= 0 && gi > ni {
		t.Errorf("ScheduleOnTimeMacro's unload gate comes AFTER the xlfNow C-API call; "+
			"it must precede every Excel call (gate@%d, xlfNow@%d)", gi, ni)
	}
	// (4) The gate must NOT report an Excel status. Reverting to xlretFailed is
	//     the specific regression: it compiles, it still means "not scheduled",
	//     and the only visible symptom is a misleading WARN on a path nobody
	//     runs a unit test over.
	if strings.Contains(body, "TeardownStarted()) return xlretFailed") {
		t.Errorf("ScheduleOnTimeMacro's teardown gate returns xlretFailed again; a deliberate " +
			"no-op must not be indistinguishable from an Excel rejection, because callers warn " +
			"about rejections and that WARN then fires on every orderly shutdown")
	}

	// (2) Both failure paths warn; neither may be INFO.
	if strings.Contains(body, "xll::LogInfo(") {
		t.Errorf("ScheduleOnTimeMacro still logs a scheduling failure at INFO; a rejected " +
			"arm silently ends the caller's re-arm chain and must be LogWarn")
	}
	for _, want := range []string{
		`xll::LogWarn(std::string("ScheduleOnTimeMacro: xlfNow rc=")`,
		`xll::LogWarn(std::string("ScheduleOnTimeMacro: xlcOnTime rc=")`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ScheduleOnTimeMacro missing warn-level failure log %q", want)
		}
	}

	// (3) The xlfNow branch reports the xltype, so rc=0 cannot be misread as success.
	if !strings.Contains(body, `" xltype=" + std::to_string(xNow.get()->xltype)`) {
		t.Errorf("ScheduleOnTimeMacro's xlfNow failure log omits the returned xltype; when " +
			"xlfNow SUCCEEDS but returns a non-numeric operand the line reads " +
			"\"rc=0 (xlretSuccess)\" and is actively misleading")
	}
}

// TestOnTimeTeardownSentinelContract pins the sentinel itself and both sides of
// the distinction it exists to make (2026-08-03).
//
// The value has to be NEGATIVE, and that is not a style preference: every Excel12
// xlret is a non-negative bit value (xlretSuccess 0, xlretAbort 1, ...,
// xlretNotClusterSafe 512), so a non-negative sentinel could collide with a real
// status as the SDK grows, and 0 in particular would read as SUCCESS — silently
// converting "we refused to schedule" into "scheduled", which is how a caller
// ends up believing a retry chain is alive after teardown killed it.
func TestOnTimeTeardownSentinelContract(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	hdr, ok := m["include/xll_deferred_commands.h"]
	if !ok {
		t.Fatalf("embedded include/xll_deferred_commands.h not found in assets")
	}
	if want := "constexpr int kOnTimeNotScheduledTeardown = -1;"; !strings.Contains(hdr, want) {
		t.Errorf("xll_deferred_commands.h must declare the teardown sentinel as %q — a negative "+
			"value cannot collide with any xlret, and 0 would read as xlretSuccess", want)
	}

	code, ok := m["src/ribbon_connect.cpp"]
	if !ok {
		t.Fatalf("embedded src/ribbon_connect.cpp not found in assets")
	}
	// Stripped so the rationale comments — which quote the very log levels and
	// sentinel name being asserted, in the opposite arrangement to the code —
	// cannot satisfy an assertion the code does not.
	code = stripCppCommentsAsset(code)

	// Both arm sites must branch on the sentinel BEFORE warning, and must keep a
	// WARN for every other rc: a real Excel rejection still silently ends the
	// chain and still has to be operator-visible. Losing either half defeats the
	// fix — suppressing everything would hide genuine rejections, suppressing
	// nothing restores the misleading shutdown WARN.
	sentinelChecks := strings.Count(code, "xll::kOnTimeNotScheduledTeardown")
	if sentinelChecks != 2 {
		t.Errorf("ribbon_connect.cpp checks the teardown sentinel %d time(s), want 2 "+
			"(ArmConnectRetry's initial arm and RunConnectRetryTick's re-arm); an unchecked "+
			"site logs WARN during orderly shutdown", sentinelChecks)
	}
	for _, want := range []string{
		// The real-rejection WARNs stay.
		`SAFE_LOG_WARN("Ribbon: OnTime connect retry could not be armed (xlcOnTime rc="`,
		`SAFE_LOG_WARN("Ribbon: OnTime connect retry could not re-arm (xlcOnTime rc="`,
		// The teardown path is DEBUG, not WARN.
		`SAFE_LOG_DEBUG("Ribbon: OnTime connect retry not armed`,
		`SAFE_LOG_DEBUG("Ribbon: OnTime connect retry chain ends`,
	} {
		if !strings.Contains(code, want) {
			t.Errorf("ribbon_connect.cpp missing %q", want)
		}
	}

	// Both sites still un-latch g_ribbonRetryArmed on ANY non-success, sentinel
	// included: nothing is in flight either way, and a stuck latch would block a
	// later xlAutoOpen from starting a fresh chain (the false-positive-shutdown
	// then re-enable path v0.8.41 has to survive).
	if got := strings.Count(code, "g_ribbonRetryArmed.store(false, std::memory_order_release);"); got != 2 {
		t.Errorf("ribbon_connect.cpp un-latches g_ribbonRetryArmed %d time(s), want 2 — the "+
			"un-latch must stay OUTSIDE the sentinel branch so a stuck latch cannot survive "+
			"a teardown-suppressed arm", got)
	}
}
