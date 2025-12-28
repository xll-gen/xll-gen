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

// updateDependencies runs go get for required dependencies and go mod tidy.
func updateDependencies(baseDir string, opts Options) error {
	ui.PrintHeader("Dependencies:")

	shmCmd := exec.Command("go", "get", "github.com/xll-gen/shm@"+versions.SHM)
	if baseDir != "" {
		shmCmd.Dir = baseDir
	}
	if err := ui.RunSpinner("Updating SHM dependency...", func() error {
		out, err := shmCmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, string(out))
		}
		return nil
	}); err != nil {
		ui.PrintWarning("Warning", fmt.Sprintf("'go get shm' failed: %v", err))
	} else {
		ui.PrintSuccess("Updated", "SHM dependency to "+versions.SHM)
	}

	typesCmd := exec.Command("go", "get", "github.com/xll-gen/types@"+versions.Types)
	if baseDir != "" {
		typesCmd.Dir = baseDir
	}
	if err := ui.RunSpinner("Updating types dependency...", func() error {
		out, err := typesCmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, string(out))
		}
		return nil
	}); err != nil {
		ui.PrintWarning("Warning", fmt.Sprintf("'go get types' failed: %v", err))
	} else {
		ui.PrintSuccess("Updated", "Types dependency to "+versions.Types)
	}

	if opts.DevMode {
		cmdGetXll := exec.Command("go", "get", "github.com/xll-gen/xll-gen@"+versions.XllGenDev)
		if baseDir != "" {
			cmdGetXll.Dir = baseDir
		}
		if err := ui.RunSpinner("Updating xll-gen dependency...", func() error {
			out, err := cmdGetXll.CombinedOutput()
			if err != nil {
				return fmt.Errorf("%w: %s", err, string(out))
			}
			return nil
		}); err != nil {
			ui.PrintWarning("Warning", fmt.Sprintf("'go get xll-gen@main' failed: %v", err))
		} else {
			ui.PrintSuccess("Updated", "xll-gen dependency to "+versions.XllGenDev)
		}
	}

	cmdTidy := exec.Command("go", "mod", "tidy")
	if baseDir != "" {
		cmdTidy.Dir = baseDir
	}
	if err := ui.RunSpinner("Running 'go mod tidy'...", func() error {
		out, err := cmdTidy.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, string(out))
		}
		return nil
	}); err != nil {
		ui.PrintWarning("Warning", fmt.Sprintf("'go mod tidy' failed: %v. You may need to run it manually after checking dependencies.", err))
	} else {
		ui.PrintSuccess("Completed", "'go mod tidy'")
	}

	fmt.Println("") // Spacing

	return nil
}


// fixGoImports traverses the generated directory and replaces local protocol imports
// with the correct package path github.com/xll-gen/types/go/protocol.
func fixGoImports(dir string, goModPath string) error {
	targetPkg := "github.com/xll-gen/types/go/protocol"
	targetLine := fmt.Sprintf("\tprotocol \"%s\"", targetPkg)

	// Regex to match:
	// \t"protocol"  OR  \tprotocol "protocol"
	// And also for fully qualified path.
	reShort := regexp.MustCompile(`(?m)^\s*(protocol\s+)?\"protocol\"$`)
	reLong := regexp.MustCompile(`(?m)^\s*(protocol\s+)?\"` + regexp.QuoteMeta(goModPath+"/protocol") + `\"$`)

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
			s = reShort.ReplaceAllString(s, targetLine)
			s = reLong.ReplaceAllString(s, targetLine)

			if s != string(content) {
				if err := os.WriteFile(path, []byte(s), 0644); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
