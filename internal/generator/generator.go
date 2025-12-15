package generator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xll-gen/xll-gen/internal/assets"
	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/ui"
)

// Options contains optional flags for the code generation process.
type Options struct {
	// DisablePidSuffix, if true, overrides the configuration to disable PID suffixes.
	// This is useful for deterministic testing.
	DisablePidSuffix bool
}

// Generate orchestrates the entire code generation process.
// It creates directories, generates schemas, runs flatc, and generates Go and C++ source code.
//
// Parameters:
//   - cfg: The project configuration parsed from xll.yaml.
//   - modName: The Go module name of the project.
//   - opts: Additional generation options.
//
// Returns:
//   - error: An error if any step of the generation fails.
func Generate(cfg *config.Config, modName string, opts Options) error {
	ui.PrintHeader(fmt.Sprintf("Generating code for project: %s", cfg.Project.Name))

	genDir := "generated"
	cppDir := filepath.Join(genDir, "cpp")
	if err := os.MkdirAll(cppDir, 0755); err != nil {
		return err
	}

	// We handle subdirectory structures (e.g., tools/) by checking the asset name.
	// Default is include/, but tools/ goes to cpp/tools/.
	includeDir := filepath.Join(cppDir, "include")
	if err := os.MkdirAll(includeDir, 0755); err != nil {
		return err
	}

	for name, content := range assets.AssetsMap {
		var destPath string
		if strings.HasPrefix(name, "tools/") {
			// e.g. tools/compressor.cpp -> generated/cpp/tools/compressor.cpp
			destPath = filepath.Join(cppDir, name)
		} else {
			// default: include/
			destPath = filepath.Join(includeDir, name)
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(destPath, []byte(content), 0644); err != nil {
			return err
		}
	}

	protocolPath := filepath.Join(genDir, "protocol.fbs")
	if err := generateProtocol(protocolPath); err != nil {
		return err
	}
	ui.PrintSuccess("Generated", "protocol.fbs")

	schemaPath := filepath.Join(genDir, "schema.fbs")
	if err := generateSchema(cfg, schemaPath); err != nil {
		return err
	}
	ui.PrintSuccess("Generated", "schema.fbs")

	goModulePath := modName + "/generated"

	flatcPath, err := EnsureFlatc()
	if err != nil {
		return err
	}

	// Generate Go code for schema
	// We use --no-includes to avoid regenerating Go code for protocol.fbs (which is in pkg/protocol).
	cmd := exec.Command(flatcPath, "--go", "--go-namespace", "ipc", "--go-module-name", goModulePath, "--no-includes", "-o", genDir, schemaPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("flatc (go) failed: %w", err)
	}

	// Post-process generated Go code to fix imports
	if err := fixGoImports(genDir, goModulePath); err != nil {
		return fmt.Errorf("failed to fix imports: %w", err)
	}

	ui.PrintSuccess("Generated", "Flatbuffers Go code")

	// Generate C++ code
	// We use --no-includes here because protocol_generated.h is shipped as a static asset in include/.
	// flatc will generate #include "protocol_generated.h" in schema_generated.h, which matches
	// the file in include/ (assuming include/ is in include path).
	cmd = exec.Command(flatcPath, "--cpp", "--no-includes", "-o", cppDir, schemaPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("flatc (cpp) failed: %w", err)
	}
	ui.PrintSuccess("Generated", "Flatbuffers C++ code")

	if err := generateInterface(cfg, genDir, modName); err != nil {
		return err
	}
	ui.PrintSuccess("Generated", "interface.go")

	if err := generateServer(cfg, genDir, modName); err != nil {
		return err
	}
	ui.PrintSuccess("Generated", "server.go")

	shouldAppendPid := !cfg.Gen.DisablePidSuffix && !opts.DisablePidSuffix
	if err := generateCppMain(cfg, cppDir, shouldAppendPid); err != nil {
		return err
	}
	ui.PrintSuccess("Generated", "xll_main.cpp")

	if err := generateCMake(cfg, cppDir); err != nil {
		return err
	}
	ui.PrintSuccess("Generated", "CMakeLists.txt")

	if err := generateTaskfile(cfg, "."); err != nil {
		return err
	}
	ui.PrintSuccess("Generated", "Taskfile.yml")

	ui.PrintHeader("Dependencies:")

	ui.PrintSuccess("Updating", "SHM dependency to v0.5.4")
	cmdGet := exec.Command("go", "get", "github.com/xll-gen/shm@v0.5.4")
	cmdGet.Stdout = os.Stdout
	cmdGet.Stderr = os.Stderr
	if err := cmdGet.Run(); err != nil {
		ui.PrintWarning("Warning", fmt.Sprintf("'go get shm' failed: %v", err))
	}

	ui.PrintSuccess("Running", "'go mod tidy'")
	cmdTidy := exec.Command("go", "mod", "tidy")
	cmdTidy.Stdout = os.Stdout
	cmdTidy.Stderr = os.Stderr
	if err := cmdTidy.Run(); err != nil {
		ui.PrintWarning("Warning", fmt.Sprintf("'go mod tidy' failed: %v. You may need to run it manually after checking dependencies.", err))
	}

	fmt.Println("") // Spacing

	return nil
}

// fixGoImports traverses the generated directory and replaces local protocol imports
// with the correct package path github.com/xll-gen/xll-gen/pkg/protocol.
func fixGoImports(dir string, goModPath string) error {
	targetPkg := "github.com/xll-gen/xll-gen/pkg/protocol"
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
