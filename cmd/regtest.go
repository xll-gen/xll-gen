//go:build regtest

package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/xll-gen/xll-gen/internal/regtest"
)

// regtestCmd represents the regtest command.
var regtestCmd = &cobra.Command{
	Use:   "regtest",
	Short: "Run a regression test simulation (Mock Host)",
	Run: func(cmd *cobra.Command, args []string) {
		if err := regtest.Run(); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(regtestCmd)
}
