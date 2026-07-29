package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/generator"
)

// disablePidSuffix controls whether the PID is appended to the shared memory name.
// This is set via the --no-pid-suffix flag.
var disablePidSuffix bool

// generateCmd represents the generate command.
var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate Go and C++ code from xll.yaml",
	Run: func(cmd *cobra.Command, args []string) {
		if err := runGenerate(); err != nil {
			printError("Generation", fmt.Sprintf("%v", err))
			os.Exit(1)
		}
	},
}

func init() {
	generateCmd.Flags().BoolVar(&disablePidSuffix, "no-pid-suffix", false, "Disable appending PID to SHM name")
	rootCmd.AddCommand(generateCmd)
}

// runGenerate parses the xll.yaml configuration and executes the code generation process.
// It validates the configuration, determines the module name, and invokes the generator.
//
// Returns:
//   - error: An error if generation fails at any step.
func runGenerate() error {
	// 1. Read + strictly parse xll.yaml (unknown/misspelled keys fail here).
	cfg, err := config.Load("xll.yaml")
	if err != nil {
		return err
	}

	config.ApplyDefaults(cfg)

	if err := config.Validate(cfg); err != nil {
		return err
	}

	warnRtdWithoutComAddIn(cfg)

	modName, err := getModuleName()
	if err != nil {
		return err
	}

	opts := generator.Options{
		DisablePidSuffix: disablePidSuffix,
	}

	return generator.Generate(cfg, ".", modName, opts)
}

// warnRtdWithoutComAddIn warns when a project enables RTD but has no ribbon AND no
// commands, because that combination silently opts out of the close-time
// use-after-unload protection.
//
// WHY (AGENTS.md §20.2.1 / §23.6 Stage 5). The whole graceful teardown —
// xll::GracefulTeardownOnce, and therefore Phase 1's image PIN and quiesce and
// Phase 2 — is reached ONLY from RibbonAddIn::OnBeginShutdown / OnDisconnection,
// which are compiled under XLL_RIBBON_ENABLED. The CMake template defines that macro
// only when the project has BOTH commands and ribbon.enabled. XLL_RTD_ENABLED is
// independent. So `rtd.enabled: true` with no ribbon and no commands produces an XLL
// with live streaming RTD topics and NO teardown at all: Excel unmaps the image while
// the worker thread and the hidden notify window are still live, which is exactly the
// 100%-reproducible close-time crash that the pin fixes for ribbon/command projects.
//
// Advisory (warn, not an error): the shape is legal, the crash needs live streaming
// topics at close, and the real fix — registering a minimal IDTExtensibility2 for
// every RTD build so the teardown exists regardless of the ribbon — is a bigger
// change (tracked in AGENTS.md §23.6). The point here is that the gap must not be
// SILENT: a user hitting it would see an Excel crash on close with no hint.
func warnRtdWithoutComAddIn(cfg *config.Config) {
	if msg := rtdWithoutComAddInWarning(cfg); msg != "" {
		printWarning("RTD close-time protection", msg)
	}
}

// rtdWithoutComAddInWarning returns the warning text, or "" when the project is not
// in the affected shape. Split from the printing so it can be unit-tested.
func rtdWithoutComAddInWarning(cfg *config.Config) string {
	if cfg == nil || !cfg.Rtd.Enabled {
		return ""
	}
	if len(cfg.Commands) > 0 && cfg.Ribbon.Enabled() {
		return ""
	}
	return "rtd.enabled is true but this project has no ribbon AND no commands, so no COM add-in " +
		"(IDTExtensibility2) is registered. The graceful teardown — including the image pin that " +
		"prevents Excel from unmapping the XLL while RTD threads are still live — is only reached " +
		"from those COM shutdown events, so it will NOT run. With live streaming RTD topics at " +
		"close this can crash Excel (AGENTS.md §20.2.1 / §23.6). Workaround: declare a ribbon " +
		"(ribbon.tab or ribbon.xml) AND at least one command."
}

// getModuleName extracts the Go module name from the go.mod file in the current directory.
//
// Returns:
//   - string: The module name.
//   - error: An error if go.mod is missing or cannot be parsed.
func getModuleName() (string, error) {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		return "", fmt.Errorf("failed to read go.mod: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	return "", fmt.Errorf("module name not found in go.mod")
}
