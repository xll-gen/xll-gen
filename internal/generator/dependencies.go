package generator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xll-gen/xll-gen/internal/ui"
	"github.com/xll-gen/xll-gen/internal/versions"
)

// goStep describes one "spinner + go command" step of updateDependencies.
// warnFormat is a fmt format string receiving the error as its single %v
// argument; successLabel/successMsg feed ui.PrintSuccess on the happy path.
type goStep struct {
	spinnerMsg   string
	warnFormat   string
	successLabel string
	successMsg   string
	args         []string
}

// runGoStep runs `go <args...>` in baseDir behind a spinner. Failures are
// warn-don't-fail: the error is printed as a Warning and the step completes
// normally, preserving the historical updateDependencies behavior.
func runGoStep(baseDir string, step goStep) {
	cmd := exec.Command("go", step.args...)
	if baseDir != "" {
		cmd.Dir = baseDir
	}
	if err := ui.RunSpinner(step.spinnerMsg, func() error {
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, string(out))
		}
		return nil
	}); err != nil {
		ui.PrintWarning("Warning", fmt.Sprintf(step.warnFormat, err))
	} else {
		ui.PrintSuccess(step.successLabel, step.successMsg)
	}
}

// updateDependencies runs go get for required dependencies and go mod tidy.
func updateDependencies(baseDir string, opts Options) error {
	ui.PrintHeader("Dependencies:")

	runGoStep(baseDir, goStep{
		spinnerMsg:   "Updating SHM dependency...",
		warnFormat:   "'go get shm' failed: %v",
		successLabel: "Updated",
		successMsg:   "SHM dependency to " + versions.SHM,
		args:         []string{"get", "github.com/xll-gen/shm@" + versions.SHM},
	})

	runGoStep(baseDir, goStep{
		spinnerMsg:   "Updating types dependency...",
		warnFormat:   "'go get types' failed: %v",
		successLabel: "Updated",
		successMsg:   "Types dependency to " + versions.Types,
		args:         []string{"get", "github.com/xll-gen/types@" + versions.Types},
	})

	if opts.DevMode {
		runGoStep(baseDir, goStep{
			spinnerMsg:   "Updating xll-gen dependency...",
			warnFormat:   "'go get xll-gen@main' failed: %v",
			successLabel: "Updated",
			successMsg:   "xll-gen dependency to " + versions.XllGenDev,
			args:         []string{"get", "github.com/xll-gen/xll-gen@" + versions.XllGenDev},
		})
	}

	runGoStep(baseDir, goStep{
		spinnerMsg:   "Running 'go mod tidy'...",
		warnFormat:   "'go mod tidy' failed: %v. You may need to run it manually after checking dependencies.",
		successLabel: "Completed",
		successMsg:   "'go mod tidy'",
		args:         []string{"mod", "tidy"},
	})

	fmt.Println("") // Spacing

	return nil
}

// fixGoImports traverses the generated directory and replaces local protocol imports
// with the correct package path github.com/xll-gen/types/go/protocol.
func fixGoImports(dir string, goModPath string) error {
	targetPkg := "github.com/xll-gen/types/go/protocol"

	// Regex to match:
	// "protocol"  OR  protocol "protocol"
	// "temp_prj/generated/protocol" OR protocol "temp_prj/generated/protocol"
	// We look for patterns like: [optional alias] "path/to/protocol"
	// The pattern handles both short "protocol" and long "some/path/protocol"
	re := regexp.MustCompile(`(protocol\s+)?\"([^"]*/)?protocol\"`)

	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".go") {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			s := string(content)
			s = re.ReplaceAllString(s, fmt.Sprintf("protocol \"%s\"", targetPkg))

			if s != string(content) {
				if err := os.WriteFile(path, []byte(s), 0644); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
