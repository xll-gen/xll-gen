package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

// rootCmd represents the base command when called without any subcommands.
var rootCmd = &cobra.Command{
	Use:   "xll-gen",
	Short: "A tool to generate Excel Add-ins (XLL) from Go code",
	Long: `xll-gen is a CLI tool designed to facilitate the creation of Excel Add-ins (XLL)
using an out-of-process architecture.`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

// init initializes the root command and its flags.
func init() {
	// Flags and configuration settings can be added here
}
