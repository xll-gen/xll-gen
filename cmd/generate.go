package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/generator"
	"gopkg.in/yaml.v3"
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
			fmt.Printf("Error: %v\n", err)
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
	// 1. Read xll.yaml
	data, err := os.ReadFile("xll.yaml")
	if err != nil {
		return fmt.Errorf("failed to read xll.yaml: %w", err)
	}

	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse xll.yaml: %w", err)
	}

	config.ApplyDefaults(&cfg)

	if err := config.Validate(&cfg); err != nil {
		return err
	}

	modName, err := getModuleName()
	if err != nil {
		return err
	}

	opts := generator.Options{
		DisablePidSuffix: disablePidSuffix,
	}

	return generator.Generate(&cfg, modName, opts)
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
