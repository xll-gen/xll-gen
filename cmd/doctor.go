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
	"github.com/xll-gen/xll-gen/internal/platform"
)

// Minimum tool versions xll-gen requires. Reported as a FAIL by `doctor` when a
// present-but-too-old tool is detected.
//
// minCMakeVersion MUST track the `cmake_minimum_required(VERSION ...)` in
// internal/templates/CMakeLists.txt.tmpl. doctor's contract is "block before the
// build does"; a lower gate here reports green and then hands the user CMake's
// own hard error at configure time, without xll-gen's remediation hint.
// TestDoctorCMakeMinMatchesTemplate fails if the two drift.
const (
	minGoVersion    = "1.24"
	minCMakeVersion = "3.28"
)

// doctorCmd represents the doctor command.
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check for necessary dependencies and tools",
	Run: func(cmd *cobra.Command, args []string) {
		printHeader("🩺 Running System Diagnosis...")

		checkSystem()
		checkExcelTrustedLocation()
		checkCompiler()
		checkFlatc()
		checkGo()
		checkCMake()
		checkTask()
		checkExcelSecurityAddins()

		fmt.Println("")
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

func checkSystem() {
	printSuccess("System", fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH))
}

// checkExcelSecurityAddins scans the registered Excel COM add-ins for
// DLP / document-classification suites. These hook Workbook events — often
// WorkbookBeforeClose with a modal classification prompt — which is exactly
// the surface the ribbon temp-workbook bounce touches during xlAutoOpen, and
// the combination can crash or hang Excel at startup. Advisory only (WARN),
// never a gate: the detection is substring-based and the conflict only
// applies to ribbon projects with the default bounce.
func checkExcelSecurityAddins() {
	if runtime.GOOS != "windows" {
		return // registry scan is meaningless on non-Windows dev hosts
	}
	detected := platform.DetectExcelSecurityAddins()
	if len(detected) == 0 {
		printSuccess("Excel add-ins", "No known DLP/classification add-ins detected")
		return
	}
	printWarning("Excel add-ins", fmt.Sprintf("DLP/classification add-in(s) detected: %s", strings.Join(detected, "; ")))
	printWarning("Recommendation", "These add-ins hook workbook close with modal prompts and can crash Excel during XLL load. If your project uses a ribbon, set `ribbon.bounce: keep-open` (or `off`) in xll.yaml. See README \"Deploying alongside DLP/classification add-ins\".")
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

// checkExcelTrustedLocation warns when the project directory is not covered by
// an Excel Trusted Location, because that is what makes Excel refuse the built
// XLL *silently* — no dialog, no warning, no native log, no server process. The
// only symptom is "Excel is up but there is no server exe", which reads exactly
// like a product defect and has already cost one debugging session.
//
// THIS CHECK REPLACED A PATH-LENGTH WARNING THAT NAMED THE WRONG CAUSE. The old
// one warned above 150 characters and told the user to move the project
// somewhere shorter. Its evidence was a 2026-07-29 A/B in which a ~175-character
// path failed and a 55-character path worked — but that A/B moved TWO variables
// at once: the short path was also inside the trusted root. A 2x2 on 2026-08-03
// that varied the two independently (same XLL bytes, RequireAddinSig=0, empty
// DisabledItems) settled it:
//
//	 31 chars, outside trusted -> did NOT load
//	157 chars, outside trusted -> did NOT load
//	168 chars, INSIDE trusted  -> loaded
//	 76 chars, INSIDE trusted  -> loaded   (control)
//
// Length is exonerated; membership decides. A user-facing diagnostic that names
// the wrong cause is worse than none — it sends someone who needs to add a
// trusted location off to shorten paths instead. Do not reinstate the length
// warning for XLL LOADING. (MAX_PATH headroom can still break the cmake BUILD,
// via `build\cpp\_deps\<dep>-src\include\...`, but that fails loudly with a
// build error, which is a different failure mode and needs no advisory.)
//
// Advisory, never a gate: this reads only the per-user hive, so a location
// trusted by machine policy reads as a miss. The check is therefore biased
// toward a false NEGATIVE and never toward a false positive.
func checkExcelTrustedLocation() {
	if runtime.GOOS != "windows" {
		return
	}
	wd, err := os.Getwd()
	if err != nil {
		return // nothing actionable; other checks carry the diagnosis
	}
	if entry, ok := platform.ExcelTrustedLocationFor(wd); ok {
		printSuccess("Excel trusted location", fmt.Sprintf("covered by %q", entry))
		return
	}
	printWarning("Excel trusted location", "this project directory is not covered by a per-user Excel Trusted Location — Excel may refuse to load the built XLL SILENTLY (no dialog, no log, no server process)")
	printWarning("Recommendation", "If the add-in appears not to load and the native log is missing entirely, add this folder in Excel: File > Options > Trust Center > Trust Center Settings > Trusted Locations (tick 'Subfolders of this location are also trusted'), before looking for a product defect.")
}
