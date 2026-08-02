package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// bounceCfg returns a ribbon-enabled config with the given ribbon.bounce mode.
// Reuses ribbonConnectCfg (gen_ribbon_connect_test.go) so the two test files
// pin the SAME render surface.
func bounceCfg(mode string) *config.Config {
	cfg := ribbonConnectCfg()
	cfg.Ribbon.Bounce = mode
	return cfg
}

// WHERE SOME OF THE OLD ASSERTIONS WENT (2026-08-02). GetActiveWorkbookName,
// ScratchCloseEventSuppressor and RestorePendingEventSuppression moved out of
// xll_main.cpp.tmpl into include/com/scratch_book.h + src/scratch_book.cpp (no
// template variables in them). Their BODY invariants — the GET.DOCUMENT(88)
// lookup, the file-static pending record, the armed-flag-gated restore of the
// captured ORIGINALS, the logged-not-swallowed failed put — are pinned against
// the embedded asset in internal/assets/scratch_book_cpp_test.go
// (TestScratchBookNamesTheActiveWorkbook, TestScratchCloseSuppressorRestoreContract).
//
// What stays here is what is genuinely per-project: WHICH bounce mode emits the
// close at all, that the guard is constructed BEFORE xlcFileClose, and that
// xlAutoOpen replays the idempotent restore after the SEH block.
//
// The negative assertions therefore run on CODE only (stripCppComments): the
// template documents the relocation in prose that names the very symbols the
// keep-open/off modes must not CALL, so a raw substring search false-positives on
// the breadcrumb comment.

// TestRibbonBounceKeepOpen pins ribbon.bounce: keep-open — the DLP
// mitigation mode. The scratch workbook is created (xlcNew) so the EXCEL7
// window materializes and the COMAddIns connect can run at xlAutoOpen, but it
// is NEVER closed: DLP/classification add-ins hook
// WorkbookBeforeClose with a modal classification prompt, and closing an
// unclassified scratch book mid-xlAutoOpen can crash or hang Excel. With no
// close there is no data-loss hazard either, so the close-by-identity
// machinery (GetActiveWorkbookName / xlfGetDocument) must not be emitted.
func TestRibbonBounceKeepOpen(t *testing.T) {
	t.Parallel()
	src := renderCppMain(t, bounceCfg("keep-open"))

	for _, want := range []string{
		// The bounce still creates the scratch workbook...
		"xll::CallExcel(xlcNew, nullptr, 5)",
		// ...and still re-acquires the Application and connects at xlAutoOpen.
		`xll::ribbon::TryConnectRibbon("xlAutoOpen", /*allowBounce=*/true);`,
		// The mode is observable in the log.
		"ribbon.bounce: keep-open",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("xll_main.cpp (bounce keep-open) missing %q", want)
		}
	}

	code := stripCppComments(src)
	for _, gone := range []string{
		// The close CALL must be entirely absent — this is the whole point.
		"xll::CallExcel(xlcFileClose",
		// No close => no close-by-identity machinery. Now that the helper is an
		// asset, the thing that must be absent is the CALL, and the header include
		// with it (full mode is the only mode that needs either).
		"GetActiveWorkbookName",
		"scratch_book.h",
		"xlfGetDocument",
		"scratchName",
		// The scratch book stays visible: keep it a plain 1-sheet Book1.
		"xll::CallExcel(xlcWorkbookInsert",
	} {
		if strings.Contains(code, gone) {
			t.Errorf("xll_main.cpp (bounce keep-open) must not contain %q (the scratch workbook must never be closed)", gone)
		}
	}
}

// TestRibbonBounceOff pins ribbon.bounce: off — the full opt-out for
// environments where even creating a scratch workbook at xlAutoOpen fires
// third-party Workbook event hooks at a hostile time. No xlc* workbook
// commands may be emitted at all; the COMAddIns connect defers to the
// calc-end fallback (first workbook the user opens).
func TestRibbonBounceOff(t *testing.T) {
	t.Parallel()
	src := renderCppMain(t, bounceCfg("off"))

	for _, want := range []string{
		// The helper still exists (registration + direct-acquire path)...
		"static IDispatch* GetExcelApplicationOrBounce()",
		// ...and the calc-end fallback remains the connect path.
		`xll::ribbon::TryConnectRibbon("calc end");`,
		// The opt-out is observable in the log.
		"ribbon.bounce: off",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("xll_main.cpp (bounce off) missing %q", want)
		}
	}

	code := stripCppComments(src)
	for _, gone := range []string{
		// No workbook may be created OR closed in this mode.
		"xll::CallExcel(xlcNew",
		"xll::CallExcel(xlcWorkbookInsert",
		"xll::CallExcel(xlcFileClose",
		"GetActiveWorkbookName",
		"scratch_book.h",
	} {
		if strings.Contains(code, gone) {
			t.Errorf("xll_main.cpp (bounce off) must not contain %q (the bounce is disabled)", gone)
		}
	}
}

// TestRibbonBounceFullSuppressesEventsAroundClose pins the full-mode (default)
// DLP hardening: the scratch-book close is wrapped in an RAII guard that
// sets Application.EnableEvents=false and .DisplayAlerts=false before
// xlcFileClose and restores the ORIGINAL captured values on destruction —
// so third-party WorkbookBeforeClose hooks (modal classification
// prompts) cannot fire mid-xlAutoOpen. keep-open and off have no close, so
// the guard must not be emitted there at all.
func TestRibbonBounceFullSuppressesEventsAroundClose(t *testing.T) {
	t.Parallel()
	src := renderCppMain(t, bounceCfg("full"))

	for _, want := range []string{
		// The guard's declaration is reachable (asset header, full mode only)...
		`#include "com/scratch_book.h"`,
		// ...and it is instantiated before the close.
		"xll::ribbon::ScratchCloseEventSuppressor suppressEvents(pApp);",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("xll_main.cpp (bounce full) missing %q", want)
		}
	}

	// The guard instantiation must precede the close call site in the render
	// (RAII scope covers the close).
	guardIdx := strings.Index(src, "xll::ribbon::ScratchCloseEventSuppressor suppressEvents(pApp);")
	closeIdx := strings.Index(src, "xll::CallExcel(xlcFileClose, nullptr, false)")
	if guardIdx < 0 || closeIdx < 0 || guardIdx > closeIdx {
		t.Errorf("event-suppressor guard must be instantiated before xlcFileClose (guard@%d, close@%d)", guardIdx, closeIdx)
	}

	// §20.3 belt-and-braces: xlAutoOpen must call the idempotent restore AFTER
	// the SAFE_BLOCK wrapping the connect attempt, so a dtor-skipping SEH
	// unwind from inside the bounce still gets EnableEvents/DisplayAlerts
	// restored. The call must come after the xlAutoOpen connect attempt.
	connectIdx := strings.Index(src, `xll::ribbon::TryConnectRibbon("xlAutoOpen", /*allowBounce=*/true);`)
	restoreIdx := strings.LastIndex(src, "xll::ribbon::RestorePendingEventSuppression();")
	if connectIdx < 0 || restoreIdx < 0 || restoreIdx < connectIdx {
		t.Errorf("xlAutoOpen must invoke RestorePendingEventSuppression() after the connect attempt (connect@%d, restore@%d)", connectIdx, restoreIdx)
	}

	// keep-open / off have no close -> neither the guard nor its header may be
	// rendered. On CODE only: the template's relocation breadcrumb names the type
	// in prose for every mode.
	for _, mode := range []string{"keep-open", "off"} {
		modeCode := stripCppComments(renderCppMain(t, bounceCfg(mode)))
		if strings.Contains(modeCode, "ScratchCloseEventSuppressor") {
			t.Errorf("xll_main.cpp (bounce %s) must not emit ScratchCloseEventSuppressor (no close in this mode)", mode)
		}
	}
}

// TestRibbonBounceDefaultIsFull pins that an UNSET ribbon.bounce renders the
// historical full bounce (create + close-by-identity) — the template must go
// through BounceMode() (which maps "" -> "full"), not the raw .Bounce field,
// because generator tests construct configs directly and skip default
// application. The full-mode contract itself is pinned in detail by
// TestXllMainRibbonDeferredConnect; this is the ""-vs-"full" equivalence.
func TestRibbonBounceDefaultIsFull(t *testing.T) {
	t.Parallel()
	unset := renderCppMain(t, bounceCfg(""))
	full := renderCppMain(t, bounceCfg("full"))
	if unset != full {
		t.Errorf("ribbon.bounce unset must render identically to ribbon.bounce: full")
	}
	if !strings.Contains(full, "xll::CallExcel(xlcFileClose, nullptr, false)") {
		t.Errorf("ribbon.bounce: full must keep the close-by-identity xlcFileClose")
	}
}
