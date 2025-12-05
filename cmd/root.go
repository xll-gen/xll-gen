package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "xll-gen",
	Short: "A tool to generate Excel Add-ins (XLL) from Go code",
	Long: `xll-gen is a CLI tool designed to facilitate the creation of Excel Add-ins (XLL)
using an out-of-process architecture.`,
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	// Flags and configuration settings can be added here
}
