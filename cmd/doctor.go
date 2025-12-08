package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"xll-gen/internal/generator"
)

// doctorCmd represents the doctor command.
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check for necessary dependencies and tools",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Checking environment...")

		// Check C++ compiler
		checkCompiler()

		// Check flatc
		checkFlatc()
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

// checkCompiler verifies if a suitable C++ compiler (MSVC or MinGW) is available in the system PATH.
func checkCompiler() {
	fmt.Print("Checking for C++ compiler... ")

	// Check for cl.exe (MSVC)
	if _, err := exec.LookPath("cl.exe"); err == nil {
		fmt.Println("Found MSVC (cl.exe)")
		return
	}

	// Check for g++ (MinGW/GCC)
	if _, err := exec.LookPath("g++"); err == nil {
		fmt.Println("Found g++")
		return
	}

	if _, err := exec.LookPath("gcc"); err == nil {
		fmt.Println("Found gcc")
		return
	}

	fmt.Println("NOT FOUND")
	fmt.Println("Warning: No C++ compiler found. You will not be able to build the XLL.")

	if runtime.GOOS == "windows" {
		// Check if winget is available
		if _, err := exec.LookPath("winget"); err == nil {
			fmt.Print("Do you want to install MinGW using winget? [Y/n] ")
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
					fmt.Printf("Error installing MinGW: %v\n", err)
				} else {
					fmt.Println("MinGW installed successfully. Please restart your terminal/shell to update PATH.")
				}
				return
			}
		}

		fmt.Println("Tip: Run `winget install -e --id BrechtSanders.WinLibs.POSIX.UCRT` to install MinGW.")
	}
}

// checkFlatc verifies if the FlatBuffers compiler (flatc) is available and downloads it if missing.
func checkFlatc() {
	fmt.Print("Checking for flatc... ")

	path, err := generator.EnsureFlatc()
	if err != nil {
		fmt.Println("NOT FOUND")
		fmt.Printf("Failed to resolve flatc: %v\n", err)
		return
	}
	fmt.Printf("Found (%s)\n", path)
}
