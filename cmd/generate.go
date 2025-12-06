package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	"xll-gen/pkg/config"
	"xll-gen/pkg/generator"
)

var disablePidSuffix bool

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
