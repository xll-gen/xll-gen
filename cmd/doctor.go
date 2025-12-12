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

		// Check C++ compiler
		checkCompiler()

		// Check flatc
		checkFlatc()

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
