package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"time"

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
	start := time.Now()

	if _, err := os.Stat("Taskfile.yml"); os.IsNotExist(err) {
		if _, err := os.Stat("Taskfile.yaml"); os.IsNotExist(err) {
			printError("Error", "Taskfile.yml not found. Are you in the project root?")
			os.Exit(1)
		}
	}

	taskExe := "task"
	if _, err := exec.LookPath("task"); err != nil {
		if _, err := exec.LookPath("go-task"); err == nil {
			taskExe = "go-task"
		} else {
			printError("Error", "'task' command not found.")
			if _, err := exec.LookPath("go"); err == nil {
				printWarning("Action Required", "Run: go install github.com/go-task/task/v3/cmd/task@latest")
			} else {
				printWarning("Action Required", "Install from https://taskfile.dev")
			}
			os.Exit(1)
		}
	}

	taskName := "build"
	if debug {
		taskName = "build-debug"
	}

	printHeader(fmt.Sprintf("Building project using '%s %s'...", taskExe, taskName))

	cmd := exec.Command(taskExe, taskName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		printError("Build", fmt.Sprintf("Failed: %v", err))
		os.Exit(1)
	}

	duration := time.Since(start).Round(time.Millisecond)
	printSuccess("Build", fmt.Sprintf("Completed in %v", duration))
}
