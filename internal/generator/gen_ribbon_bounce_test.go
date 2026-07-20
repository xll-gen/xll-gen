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

// TestRibbonBounceKeepOpen pins ribbon.bounce: keep-open — the DLP/Titus
// mitigation mode. The scratch workbook is created (xlcNew) so the EXCEL7
// window materializes and the COMAddIns connect can run at xlAutoOpen, but it
// is NEVER closed: DLP/classification add-ins (e.g. Titus) hook
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
		`TryConnectRibbon("xlAutoOpen", /*allowBounce=*/true);`,
		// The mode is observable in the log.
		"ribbon.bounce: keep-open",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("xll_main.cpp (bounce keep-open) missing %q", want)
		}
	}

	for _, gone := range []string{
		// The close CALL must be entirely absent — this is the whole point.
		// (Asserted on the call site, not the bare token: explanatory comments
		// in other rendered paths may legitimately mention the opcode name.)
		"xll::CallExcel(xlcFileClose",
		// No close => no close-by-identity machinery.
		"GetActiveWorkbookName",
		"xlfGetDocument",
		"scratchName",
		// The scratch book stays visible: keep it a plain 1-sheet Book1.
		"xll::CallExcel(xlcWorkbookInsert",
	} {
		if strings.Contains(src, gone) {
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
		`TryConnectRibbon("calc end");`,
		// The opt-out is observable in the log.
		"ribbon.bounce: off",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("xll_main.cpp (bounce off) missing %q", want)
		}
	}

	for _, gone := range []string{
		// No workbook may be created OR closed in this mode. (Asserted on the
		// call sites — comments elsewhere may mention the opcode names.)
		"xll::CallExcel(xlcNew",
		"xll::CallExcel(xlcWorkbookInsert",
		"xll::CallExcel(xlcFileClose",
		"GetActiveWorkbookName",
	} {
		if strings.Contains(src, gone) {
			t.Errorf("xll_main.cpp (bounce off) must not contain %q (the bounce is disabled)", gone)
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
