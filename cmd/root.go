package cmd

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "xll-gen",
	Short: "A tool to generate Excel XLL add-ins",
	Long:  `xll-gen is a command-line tool to help you create Excel XLL add-ins with ease.`,
}

func Execute() error {
	return rootCmd.Execute()
}
