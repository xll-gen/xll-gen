package cmd

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// TestRtdWithoutComAddInWarning pins the advisory added for the xll-cpp-reviewer
// HIGH #1 finding (AGENTS.md §20.2.1 / §23.6 Stage 5).
//
// The close-time image PIN and the Phase-1 quiesce live in xll::GracefulTeardownOnce,
// whose ONLY call sites are RibbonAddIn::OnBeginShutdown / OnDisconnection — compiled
// under XLL_RIBBON_ENABLED, which CMakeLists.txt.tmpl defines on `.Ribbon.Enabled`
// ALONE (corrected 2026-08-03; it was nested under `.Commands` until 2026-08-02, and
// the "needs BOTH" wording outlived that change here and in cmd/generate.go).
// XLL_RTD_ENABLED is independent. So `rtd.enabled: true` with no ribbon and no
// commands produces an XLL that has live streaming RTD topics and NO graceful teardown
// at all — the unmap hazard is fully unmitigated there, and the 0/22 real-Excel
// verification was obtained on a ribbon+commands project.
//
// The `len(Commands) > 0` term in the gate is BELT-AND-BRACES, not the macro's
// condition: config.Validate rejects a ribbon with no commands, so the two spellings
// select the same reachable projects. The macro gate itself is pinned separately by
// internal/generator/gen_rtd_teardown_gap_test.go::TestRibbonMacroGateIsRibbonEnabledAlone,
// which is where a re-nesting regression would surface.
//
// The generator cannot fix that shape cheaply (the real fix is registering a minimal
// IDTExtensibility2 for every RTD build), but it must not be SILENT about it.
func TestRtdWithoutComAddInWarning(t *testing.T) {
	t.Parallel()

	cmds := []config.Command{{Name: "DoThing"}}

	cases := []struct {
		name string
		cfg  *config.Config
		warn bool
	}{
		{
			name: "rtd with ribbon and commands is protected",
			cfg: &config.Config{
				Rtd:      config.RtdConfig{Enabled: true},
				Commands: cmds,
				Ribbon:   config.RibbonConfig{Tab: "Demo"},
			},
			warn: false,
		},
		{
			name: "rtd with NO ribbon and NO commands is unprotected",
			cfg: &config.Config{
				Rtd: config.RtdConfig{Enabled: true},
			},
			warn: true,
		},
		{
			name: "rtd with commands but no ribbon is unprotected (commands bring no IDTExtensibility2)",
			cfg: &config.Config{
				Rtd:      config.RtdConfig{Enabled: true},
				Commands: cmds,
			},
			warn: true,
		},
		{
			name: "rtd with ribbon but no commands: warned (config.Validate rejects this shape anyway)",
			cfg: &config.Config{
				Rtd:    config.RtdConfig{Enabled: true},
				Ribbon: config.RibbonConfig{Tab: "Demo"},
			},
			warn: true,
		},
		{
			name: "no rtd: nothing to warn about",
			cfg: &config.Config{
				Commands: cmds,
			},
			warn: false,
		},
		{
			name: "nil config is not a crash",
			cfg:  nil,
			warn: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rtdWithoutComAddInWarning(tc.cfg)
			if tc.warn && got == "" {
				t.Fatalf("expected a warning for this shape, got none")
			}
			if !tc.warn && got != "" {
				t.Fatalf("expected NO warning for this shape, got: %s", got)
			}
			if tc.warn {
				// The message has to name the actual consequence and the workaround, or
				// it is noise the user will skip.
				for _, want := range []string{"IDTExtensibility2", "unmapping", "23.6", "ribbon"} {
					if !strings.Contains(got, want) {
						t.Errorf("warning must mention %q; got: %s", want, got)
					}
				}
			}
		})
	}
}
