package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// The DEFINE, not the bare macro name. CMakeLists.txt.tmpl carries a long comment
// block that names XLL_RIBBON_ENABLED while explaining its gate, so a bare-name
// substring check passes on every render and pins nothing.
const (
	rtdMacroDefine    = "target_compile_definitions(${PROJECT_NAME} PRIVATE XLL_RTD_ENABLED)"
	ribbonMacroDefine = "target_compile_definitions(${PROJECT_NAME} PRIVATE XLL_RIBBON_ENABLED)"
)

// Gates for the ONE teardown driver the product has, and for the KNOWN, DELIBERATE
// gap beside it (AGENTS.md §20.2.1 / §23.6 Stage 5, review finding HIGH #1).
//
// THE FACT BEING PINNED. xll::GracefulTeardownOnce is the only entry to Phase 1
// (image PIN + quiesce) and, through it, to Phase 2 (RunDestructiveTeardown). Its
// only two call sites in the whole tree are RibbonAddIn::OnBeginShutdown and
// RibbonAddIn::OnDisconnection (internal/assets/files/src/ribbon_addin.cpp), both
// inside that file's `#ifdef XLL_RIBBON_ENABLED` block. CMakeLists.txt.tmpl defines
// XLL_RIBBON_ENABLED on `.Ribbon.Enabled` — and on nothing else. Therefore:
//
//   - a project WITH a ribbon has the close-time unmap protection, and
//   - a project with `rtd.enabled: true` and NO ribbon has live streaming RTD
//     topics and NO teardown at all — Phase 1 never runs, Phase 2 never runs, and
//     RtdServer::ServerTerminate's trigger stays disarmed because
//     HostShutdownTeardownArmed() is only ever set inside GracefulTeardownOnce.
//
// That second bullet is the 100%-reproducible crash class of §23.6 Stage 5, left
// unmitigated. The 0/22 real-Excel verification that validated v0.8.41 was taken on
// a ribbon+commands project, so it does not transfer to the ribbon-less shape.
//
// WHY A TEST FOR A GAP. The gap is currently reported only by prose (AGENTS.md) and
// a generate-time advisory (cmd/generate.go::rtdWithoutComAddInWarning). Neither can
// catch the failure mode that actually costs time here: someone wiring "half" of a
// COM add-in into RTD-only builds — a class registered but never connected, or a
// teardown hook registered but never reachable — and believing the hazard is closed.
// TestRtdOnlyBuildEmitsNoTeardownDriver states the current shape exactly, so the day
// the minimal-IDTExtensibility2 fix lands, these assertions are the checklist that
// has to flip, all at once, deliberately. Do not "repair" it by deleting it.
func TestRtdOnlyBuildEmitsNoTeardownDriver(t *testing.T) {
	t.Parallel()

	cfg := rtdOnlyTeardownGapConfig()

	// --- CMake: the hazard is compiled in, the protection is not. ---
	cmake := renderCMakeForTeardownGap(t, cfg)
	if !strings.Contains(cmake, rtdMacroDefine) {
		t.Fatalf("rtd-only project must define XLL_RTD_ENABLED (the RTD machinery — worker " +
			"dispatch + the hidden notify window — is what Excel unmaps out from under)")
	}
	if strings.Contains(cmake, ribbonMacroDefine) {
		t.Fatalf("rtd-only project unexpectedly defines XLL_RIBBON_ENABLED. If this is the " +
			"minimal-IDTExtensibility2 fix landing, that is the intended direction — but then " +
			"every other assertion in this test must be flipped in the SAME change, and the " +
			"generate-time advisory in cmd/generate.go must stop firing for this shape. A " +
			"ribbon macro defined without the rest of the wiring is a half-wired teardown.")
	}

	// --- xll_main.cpp: no COM add-in identity, no class object, no connect. ---
	main := stripCppComments(renderCppMain(t, cfg))
	for _, absent := range []struct {
		token string
		why   string
	}{
		{"GetRibbonClsid", "the COM add-in's CLSID accessor — without it DllGetClassObject has no add-in branch"},
		{"RibbonAddIn", "the IDTExtensibility2 implementation that owns OnBeginShutdown/OnDisconnection"},
		{"SetConnectContext", "the publish that lets the connect machinery reach the generated identity"},
		{"ArmConnectRetry", "the bounded xlcOnTime connect retry — no connect attempt exists to retry"},
		{"__xllgen_RibbonConnectRetry", "the exported OnTime retry macro"},
	} {
		if strings.Contains(main, absent.token) {
			t.Errorf("rtd-only xll_main.cpp unexpectedly contains %q (%s); see the flip-together "+
				"note above", absent.token, absent.why)
		}
	}

	// --- The RTD hazard itself IS wired. This is what makes the gap matter: the
	// streaming machinery whose threads and notify window Excel unmaps is present
	// and running in exactly the shape that has no Phase 1 to stop it. ---
	for _, want := range []string{
		"CoRegisterClassObject(GetRtdClsid()",
		"xll::CreateRtdNotifyWindow();",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("rtd-only xll_main.cpp must still wire the RTD machinery (%q missing); "+
				"without it this test would be pinning the absence of a hazard that is not there", want)
		}
	}

	// --- The asymmetry, stated outright: the teardown HOOK is registered, and
	// nothing can ever invoke it. xll::SetGracefulTeardownHook is emitted for
	// `{{if or .Ribbon.Enabled .Commands .Rtd.Enabled}}`, but the only caller of the
	// hook is GracefulTeardownOnce, whose only call sites are compiled out here.
	// A reader who greps for "teardown" in a generated rtd-only project finds this
	// line and concludes the teardown exists. It does not. ---
	if !strings.Contains(main, "xll::SetGracefulTeardownHook(&GracefulComTeardownHook);") {
		t.Errorf("expected the (unreachable) teardown-hook registration to still be emitted for " +
			"an rtd-only project — if it was removed, update this test and the AGENTS.md §23.6 " +
			"HIGH #1 note together")
	}
}

// TestRibbonBuildWiresTheOnlyTeardownDriver is the positive regression gate: it
// asserts, in ONE place, the full set of wirings the close-time fix depends on.
//
// It is a CHECKLIST, not four unique gates. Measured across the whole package
// (2026-08-03), three of the four tokens are already caught elsewhere if deleted:
// TryConnectRibbon by gen_addin_key_lifetime_test.go / gen_ribbon_bounce_test.go /
// gen_ribbon_connect_test.go, SetGracefulTeardownHook by gen_cancel_quit_test.go /
// gen_ribbon_connect_test.go / TestGolden, SetConnectContext by
// gen_ribbon_connect_test.go::TestXllMainPublishesRibbonConnectContext. Only
// `rtd::ClassFactory<RibbonAddIn>` — the DllGetClassObject branch that makes the
// add-in activatable at all — is covered here and nowhere else. (An earlier draft
// of this comment claimed all four were uniquely load-bearing; that was measured
// with `-run` scoped to this single test, which hid the other tests entirely.)
//
// The value it adds over the scattered coverage is that the four appear together,
// so the day the minimal-IDTExtensibility2 fix lands there is one list to re-derive
// rather than four files to find.
func TestRibbonBuildWiresTheOnlyTeardownDriver(t *testing.T) {
	t.Parallel()

	cfg := ribbonRtdTeardownConfig()

	cmake := renderCMakeForTeardownGap(t, cfg)
	if !strings.Contains(cmake, ribbonMacroDefine) {
		t.Fatalf("ribbon project must define XLL_RIBBON_ENABLED — it is what compiles " +
			"RibbonAddIn::OnBeginShutdown/OnDisconnection, the ONLY xll::GracefulTeardownOnce " +
			"call sites in the tree (src/ribbon_addin.cpp)")
	}

	main := stripCppComments(renderCppMain(t, cfg))
	for _, want := range []struct {
		token string
		why   string
	}{
		{"rtd::ClassFactory<RibbonAddIn>", "DllGetClassObject must be able to hand out the add-in class"},
		{"xll::ribbon::SetConnectContext(ribbonCtx);", "the connect machinery cannot register or connect without the published identity"},
		{"xll::ribbon::TryConnectRibbon(\"xlAutoOpen\"", "the load-time connect is what makes Office deliver OnBeginShutdown at all"},
		{"xll::SetGracefulTeardownHook(&GracefulComTeardownHook);", "the COM half of the teardown (revoke / disconnect) is reached only through this hook"},
	} {
		if !strings.Contains(main, want.token) {
			t.Errorf("ribbon xll_main.cpp missing %q — %s", want.token, want.why)
		}
	}
}

// TestRibbonMacroGateIsRibbonEnabledAlone pins the CMake gate that the two tests
// above read, and corrects a claim that outlived its code.
//
// Until 2026-08-02 XLL_RIBBON_ENABLED was nested inside `.Commands`, so the macro
// really did need BOTH commands and a ribbon. It no longer does — CMakeLists.txt.tmpl
// gates it on `.Ribbon.Enabled` ALONE, deliberately, so a commands-less ribbon config
// built directly in a test renders a no-op instead of a link error. The
// "needs BOTH" wording survived in cmd/generate.go's rationale comment long after the
// code stopped saying it; this test is what keeps the corrected statement honest.
//
// config.Validate independently rejects a ribbon with no commands, so the shape below
// is unreachable through `xll-gen generate` — that is precisely why the CMake gate
// needed a test of its own rather than an inference from the advisory's condition.
func TestRibbonMacroGateIsRibbonEnabledAlone(t *testing.T) {
	t.Parallel()

	cfg := ribbonRtdTeardownConfig()
	cfg.Commands = nil
	cfg.Ribbon.Groups = nil

	cmake := renderCMakeForTeardownGap(t, cfg)
	if !strings.Contains(cmake, ribbonMacroDefine) {
		t.Fatalf("XLL_RIBBON_ENABLED must be gated on .Ribbon.Enabled ALONE (CMakeLists.txt.tmpl, " +
			"2026-08-02). Re-nesting it under .Commands turns a commands-less ribbon render into a " +
			"link failure against src/ribbon_addin.cpp, src/ribbon_connect.cpp and src/scratch_book.cpp")
	}

	// Commands are independent of the macro: a commands-only project gets neither
	// the macro nor the teardown, which is the other half of the advisory's shape.
	noRibbon := rtdOnlyTeardownGapConfig()
	noRibbon.Commands = []config.Command{{Name: "DoThing", Handler: "DoThing"}}
	if strings.Contains(renderCMakeForTeardownGap(t, noRibbon), ribbonMacroDefine) {
		t.Errorf("commands WITHOUT a ribbon must not define XLL_RIBBON_ENABLED — commands reach " +
			"the Go server through xll::ribbon::SendCommandInvoke, which is compiled ungated; " +
			"they bring no IDTExtensibility2 and therefore no teardown")
	}
}

// rtdOnlyTeardownGapConfig is the affected shape: streaming RTD, no ribbon, no
// commands. A plain `rtd`-mode function is included so the render exercises the
// streaming path whose worker thread and notify window are the unmap victims.
func rtdOnlyTeardownGapConfig() *config.Config {
	return &config.Config{
		Project: config.ProjectConfig{Name: "GapProj", Version: "0.1"},
		Logging: config.LoggingConfig{Level: "info", Dir: "logs"},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: boolPtr(true)},
		},
		Rtd: config.RtdConfig{
			Enabled:     true,
			ProgID:      "GapProj.Rtd",
			Clsid:       "{11111111-2222-3333-4444-555555555555}",
			Description: "Gap RTD",
		},
		Functions: []config.Function{
			{Name: "Ticker", Mode: "rtd", Return: "float",
				Args: []config.Arg{{Name: "sym", Type: "string"}}},
		},
	}
}

// ribbonRtdTeardownConfig is the measured-clean shape: the same streaming RTD plus
// the ribbon COM add-in that carries the teardown.
func ribbonRtdTeardownConfig() *config.Config {
	cfg := rtdOnlyTeardownGapConfig()
	cfg.Commands = []config.Command{{Name: "DoThing", Description: "Does a thing", Handler: "DoThing"}}
	cfg.Ribbon = config.RibbonConfig{
		Tab:    "Gap",
		ProgID: "GapProj.Ribbon",
		Clsid:  "{99999999-8888-7777-6666-555555555555}",
		Groups: []config.RibbonGroup{{
			Label:   "Actions",
			Buttons: []config.RibbonButton{{Label: "Do", Command: "DoThing"}},
		}},
	}
	return cfg
}

// renderCMakeForTeardownGap runs the real generateCMake into a temp dir and returns
// the rendered CMakeLists.txt. Deliberately named for this file so it cannot collide
// with a helper another test file adds.
func renderCMakeForTeardownGap(t *testing.T, cfg *config.Config) string {
	t.Helper()
	dir := t.TempDir()
	if err := generateCMake(cfg, dir); err != nil {
		t.Fatalf("generateCMake failed: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "CMakeLists.txt"))
	if err != nil {
		t.Fatalf("read generated CMakeLists.txt: %v", err)
	}
	return string(b)
}
