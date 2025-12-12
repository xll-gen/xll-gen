package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var debugBuild bool

// buildCmd represents the build command.
var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build the XLL project using Taskfile",
	Long:  `Executes 'task build' to build the Go server and C++ XLL. It requires the 'task' (or 'go-task') command to be available in the system PATH.`,
	Run: func(cmd *cobra.Command, args []string) {
		runBuildCommand(debugBuild)
	},
}

func init() {
	rootCmd.AddCommand(buildCmd)
	buildCmd.Flags().BoolVar(&debugBuild, "debug", false, "Build in debug mode (task build-debug)")
}

// runBuildCommand checks for the presence of a Taskfile and executes the 'task build' command.
// It searches for 'task' or 'go-task' executables in the system PATH.
// If the build fails, it exits the process with a non-zero status code.
func runBuildCommand(debug bool) {
	// 1. Check for Taskfile.yml or Taskfile.yaml
	if _, err := os.Stat("Taskfile.yml"); os.IsNotExist(err) {
		if _, err := os.Stat("Taskfile.yaml"); os.IsNotExist(err) {
			fmt.Println("Error: Taskfile.yml not found. Are you in the project root?")
			os.Exit(1)
		}
	}

	// 2. Determine the task runner command
	taskExe := "task"
	if _, err := exec.LookPath("task"); err != nil {
		if _, err := exec.LookPath("go-task"); err == nil {
			taskExe = "go-task"
		} else {
			fmt.Println("Error: 'task' command not found. Please install Taskfile runner (https://taskfile.dev).")
			os.Exit(1)
		}
	}

	// 3. Execute 'task build'
	taskName := "build"
	if debug {
		taskName = "build-debug"
	}

	fmt.Printf("Building project using '%s %s'...\n", taskExe, taskName)
	cmd := exec.Command(taskExe, taskName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Printf("Build failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Build completed successfully.")
}
