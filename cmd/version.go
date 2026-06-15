package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/xll-gen/xll-gen/internal/versions"
	"github.com/xll-gen/xll-gen/version"
)

// versionCmd prints the xll-gen tool version plus the dependency versions baked
// into generated projects (the C++ FetchContent GIT_TAGs and the Go module
// pins), so a user can see exactly what a `generate` will produce.
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the xll-gen version and the pinned dependency versions",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("xll-gen %s\n", version.Version)
		fmt.Println("Pinned dependencies (baked into generated projects):")
		fmt.Printf("  types        %s\n", versions.Types)
		fmt.Printf("  shm          %s\n", versions.SHM)
		fmt.Printf("  flatbuffers  %s\n", versions.FlatBuffers)
		fmt.Printf("  phmap        %s\n", versions.PHMAP)
		fmt.Printf("  zstd         %s\n", versions.Zstd)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
