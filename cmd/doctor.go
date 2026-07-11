package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/xll-gen/xll-gen/internal/flatc"
)

// Minimum tool versions xll-gen requires. Reported as a FAIL by `doctor` when a
// present-but-too-old tool is detected.
const (
	minGoVersion    = "1.24"
	minCMakeVersion = "3.24"
)

// doctorCmd represents the doctor command.
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check for necessary dependencies and tools",
	Run: func(cmd *cobra.Command, args []string) {
		printHeader("🩺 Running System Diagnosis...")

		checkSystem()
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

func checkSystem() {
	printSuccess("System", fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH))
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

	// No compiler on PATH. On Windows, cl.exe is not on PATH in a plain shell even
	// when Visual Studio is installed — report that as a WARN (with the fix) rather
	// than a flat NOT FOUND that would send the user chasing a non-existent problem.
	if runtime.GOOS == "windows" && detectVisualStudio() {
		printWarning("C++ Compiler", "Visual Studio found but cl.exe not on PATH — run from a Developer Command Prompt or use MinGW")
		return
	}

	printError("C++ Compiler", "NOT FOUND")
	printWarning("Action Required", "No C++ compiler found. You will not be able to build the XLL.")

	if runtime.GOOS == "windows" {
		// Only prompt interactively when stdin is a terminal; a non-interactive
		// shell (CI, piped input) gets the winget command printed as a suggestion.
		if _, err := exec.LookPath("winget"); err == nil && stdinIsTerminal() {
			fmt.Printf("\n%s?%s Do you want to install MinGW using winget? [Y/n] ", colorCyan, colorReset)
			reader := bufio.NewReader(os.Stdin)
			response, err := reader.ReadString('\n')
			if err != nil {
				// Assume 'no' on error (e.g. non-interactive shell)
				response = "n"
			}
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

// detectVisualStudio uses vswhere (shipped with VS 2017+ at a fixed location) to
// report whether a Visual Studio installation with the VC C++ toolset is present,
// even when cl.exe is not on PATH.
func detectVisualStudio() bool {
	pf := os.Getenv("ProgramFiles(x86)")
	if pf == "" {
		pf = `C:\Program Files (x86)`
	}
	vswhere := filepath.Join(pf, "Microsoft Visual Studio", "Installer", "vswhere.exe")
	if _, err := os.Stat(vswhere); err != nil {
		return false
	}
	cmd := exec.Command(vswhere,
		"-latest", "-products", "*",
		"-requires", "Microsoft.VisualStudio.Component.VC.Tools.x86.x64",
		"-property", "installationPath")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// stdinIsTerminal reports whether stdin is a character device (interactive
// terminal) rather than a pipe/file/CI capture.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// checkFlatc verifies if the FlatBuffers compiler (flatc) is available and downloads it if missing.
func checkFlatc() {
	path, err := flatc.EnsureFlatc()
	if err != nil {
		printError("Flatbuffers", "NOT FOUND")
		fmt.Printf("      %v\n", err)
		return
	}
	printSuccess("Flatbuffers", fmt.Sprintf("Found (%s)", path))
}

func checkGo() {
	checkTool("Go", "go", []string{"version"}, "Install Go: https://go.dev/dl/", minGoVersion, func(out string) string {
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
	checkTool("CMake", "cmake", []string{"--version"}, fix, minCMakeVersion, func(out string) string {
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

	checkTool("Task", exe, []string{"--version"}, fix, "", func(out string) string {
		parts := strings.Fields(out)
		for i, p := range parts {
			if p == "version:" && i+1 < len(parts) {
				return parts[i+1]
			}
		}
		return ""
	})
}

// checkTool locates exe on PATH, parses its version, and (when minVersion is
// non-empty) gates it against that minimum. A present-but-too-old tool is
// reported as a FAIL; an unparseable version degrades to a WARN rather than a
// false FAIL.
func checkTool(label, exe string, args []string, fixMessage, minVersion string, parser func(string) string) {
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

	if version == "" {
		printSuccess(label, "Found")
		return
	}

	if minVersion != "" {
		ok, parsed := versionAtLeast(version, minVersion)
		if !parsed {
			printSuccess(label, fmt.Sprintf("Found %s(%s)", colorCyan, version))
			printWarning("Version", fmt.Sprintf("could not parse version; xll-gen needs >= %s", minVersion))
			return
		}
		if !ok {
			printError(label, fmt.Sprintf("%s is too old (xll-gen requires >= %s)", version, minVersion))
			if fixMessage != "" {
				printWarning("Action Required", fixMessage)
			}
			return
		}
	}

	printSuccess(label, fmt.Sprintf("Found %s(%s)", colorCyan, version))
}

// parseVersion extracts a dotted numeric version from s, tolerating a leading
// non-digit prefix (e.g. "go1.24.3" -> [1 24 3], "v3.24" -> [3 24]) and trailing
// junk (e.g. "3.24.1-rc2" -> [3 24 1]). Returns ok=false when no numeric version
// can be found so callers can degrade gracefully.
func parseVersion(s string) ([]int, bool) {
	start := strings.IndexFunc(s, func(r rune) bool { return r >= '0' && r <= '9' })
	if start < 0 {
		return nil, false
	}
	s = s[start:]
	if end := strings.IndexFunc(s, func(r rune) bool {
		return !((r >= '0' && r <= '9') || r == '.')
	}); end >= 0 {
		s = s[:end]
	}
	var nums []int
	for _, p := range strings.Split(s, ".") {
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		nums = append(nums, n)
	}
	if len(nums) == 0 {
		return nil, false
	}
	return nums, true
}

// versionAtLeast reports whether got >= min (both parsed by parseVersion). The
// second return is false when either version could not be parsed.
func versionAtLeast(got, min string) (ok bool, parsed bool) {
	g, gok := parseVersion(got)
	m, mok := parseVersion(min)
	if !gok || !mok {
		return false, false
	}
	return compareVersions(g, m) >= 0, true
}

// compareVersions returns -1, 0, or 1 comparing two dotted version numbers
// component-wise; missing trailing components are treated as zero.
func compareVersions(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}
