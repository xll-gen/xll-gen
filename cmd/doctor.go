package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/xll-gen/xll-gen/internal/generator"
)

// doctorCmd represents the doctor command.
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check for necessary dependencies and tools",
	Run: func(cmd *cobra.Command, args []string) {
		printHeader("🩺 Running System Diagnosis...")

		checkCompiler()
		checkFlatc()
		checkGo()
		checkCMake()
		checkTask()

		fmt.Println("")
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

// checkCompiler verifies if a suitable C++ compiler (MSVC or MinGW) is available in the system PATH.
func checkCompiler() {
	// Check for cl.exe (MSVC)
	if _, err := exec.LookPath("cl.exe"); err == nil {
		printSuccess("C++ Compiler", "Found MSVC")
		return
	}

	// Check for g++ (MinGW/GCC)
	if _, err := exec.LookPath("g++"); err == nil {
		printSuccess("C++ Compiler", "Found g++")
		return
	}

	if _, err := exec.LookPath("gcc"); err == nil {
		printSuccess("C++ Compiler", "Found gcc")
		return
	}

	printError("C++ Compiler", "NOT FOUND")
	printWarning("Action Required", "No C++ compiler found. You will not be able to build the XLL.")

	if runtime.GOOS == "windows" {
		// Check if winget is available
		if _, err := exec.LookPath("winget"); err == nil {
			fmt.Printf("\n%s?%s Do you want to install MinGW using winget? [Y/n] ", colorCyan, colorReset)
			reader := bufio.NewReader(os.Stdin)
			response, _ := reader.ReadString('\n')
			response = strings.TrimSpace(response)

			if response == "" || strings.EqualFold(response, "y") || strings.EqualFold(response, "yes") {
				fmt.Println("Running: winget install -e --id BrechtSanders.WinLibs.POSIX.UCRT")
				cmd := exec.Command("winget", "install", "-e", "--id", "BrechtSanders.WinLibs.POSIX.UCRT")
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				cmd.Stdin = os.Stdin
				if err := cmd.Run(); err != nil {
					printError("Installation", fmt.Sprintf("Error installing MinGW: %v", err))
				} else {
					printSuccess("Installation", "MinGW installed successfully. Please restart your terminal.")
				}
				return
			}
		}

		fmt.Println("Tip: Run `winget install -e --id BrechtSanders.WinLibs.POSIX.UCRT` to install MinGW.")
	}
}

// checkFlatc verifies if the FlatBuffers compiler (flatc) is available and downloads it if missing.
func checkFlatc() {
	path, err := generator.EnsureFlatc()
	if err != nil {
		printError("Flatbuffers", "NOT FOUND")
		fmt.Printf("      %v\n", err)
		return
	}
	printSuccess("Flatbuffers", fmt.Sprintf("Found (%s)", path))
}

func checkGo() {
	checkTool("Go", "go", []string{"version"}, "Install Go: https://go.dev/dl/", func(out string) string {
		parts := strings.Fields(out)
		if len(parts) >= 3 && parts[0] == "go" && parts[1] == "version" {
			return parts[2]
		}
		return ""
	})
}

func checkCMake() {
	fix := "Install CMake: https://cmake.org/download/"
	if runtime.GOOS == "windows" {
		fix = "Run: winget install Kitware.CMake"
	}
	checkTool("CMake", "cmake", []string{"--version"}, fix, func(out string) string {
		lines := strings.Split(out, "\n")
		if len(lines) > 0 {
			parts := strings.Fields(lines[0])
			if len(parts) >= 3 && parts[0] == "cmake" && parts[1] == "version" {
				return parts[2]
			}
		}
		return ""
	})
}

func checkTask() {
	exe := "task"
	if _, err := exec.LookPath("task"); err != nil {
		if _, err := exec.LookPath("go-task"); err == nil {
			exe = "go-task"
		}
	}

	fix := "Install Task: https://taskfile.dev/installation/"
	if _, err := exec.LookPath("go"); err == nil {
		fix = "Run: go install github.com/go-task/task/v3/cmd/task@latest"
	}

	checkTool("Task", exe, []string{"--version"}, fix, func(out string) string {
		parts := strings.Fields(out)
		for i, p := range parts {
			if p == "version:" && i+1 < len(parts) {
				return parts[i+1]
			}
		}
		return ""
	})
}

func checkTool(label, exe string, args []string, fixMessage string, parser func(string) string) {
	path, err := exec.LookPath(exe)
	if err != nil {
		printError(label, "NOT FOUND")
		if fixMessage != "" {
			printWarning("Action Required", fixMessage)
		}
		return
	}

	version := ""
	if parser != nil && len(args) > 0 {
		cmd := exec.Command(path, args...)
		out, _ := cmd.Output()
		version = parser(string(out))
	}

	if version != "" {
		printSuccess(label, fmt.Sprintf("Found (%s)", version))
	} else {
		printSuccess(label, "Found")
	}
}
