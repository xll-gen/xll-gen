package generator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/xll-gen/xll-gen/internal/assets"
	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/flatc"
	"github.com/xll-gen/xll-gen/internal/ui"
)

// Options contains optional flags for the code generation process.
type Options struct {
	// DisablePidSuffix, if true, overrides the configuration to disable PID suffixes.
	// This is useful for deterministic testing.
	DisablePidSuffix bool

	// DevMode, if true, configures the project to use development versions of dependencies.
	DevMode bool

	// FlatcPath overrides the flatc executable path.
	FlatcPath string
}

// Generate orchestrates the entire code generation process.
// It creates directories, generates schemas, runs flatc, and generates Go and C++ source code.
//
// Parameters:
//   - cfg: The project configuration parsed from xll.yaml.
//   - baseDir: The root directory of the project (where xll.yaml resides).
//   - modName: The Go module name of the project.
//   - opts: Additional generation options.
//
// Returns:
//   - error: An error if any step of the generation fails.
func Generate(cfg *config.Config, baseDir string, modName string, opts Options) error {
	ui.PrintHeader(fmt.Sprintf("Generating code for project: %s", cfg.Project.Name))

	// Resolve baseDir to absolute path to avoid relative path issues when cmd.Dir is set.
	// If baseDir is empty, filepath.Abs returns the current working directory.
	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return fmt.Errorf("failed to resolve absolute path for baseDir: %w", err)
	}
	baseDir = absBaseDir

	genDir := filepath.Join(baseDir, "generated")
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
		} else if strings.HasPrefix(name, "src/") {
			// e.g. src/xll_log.cpp -> generated/cpp/src/xll_log.cpp
			destPath = filepath.Join(cppDir, name)
		} else if strings.HasPrefix(name, "include/") {
			// e.g. include/SHMAllocator.h -> generated/cpp/include/SHMAllocator.h
			// We join with cppDir because 'name' already includes 'include/'
			destPath = filepath.Join(cppDir, name)
		} else {
			// Fallback (though mostly files should be in subdirectories now)
			// default: files in root map to include/ (historical behavior, mostly for .h if any left)
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

	var flatcPath string
	if opts.FlatcPath != "" {
		flatcPath = opts.FlatcPath
	} else {
		var err error
		flatcPath, err = flatc.EnsureFlatc()
		if err != nil {
			return err
		}
	}

	// Generate Go code for schema
	if err := ui.RunSpinner("Generating Flatbuffers Go code...", func() error {
		// We use --no-includes to avoid regenerating Go code for protocol.fbs (which is in pkg/protocol).
		cmd := exec.Command(flatcPath, "--go", "--go-namespace", "ipc", "--go-module-name", goModulePath, "--no-includes", "-o", genDir, schemaPath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("flatc (go) failed: %w\n%s", err, string(out))
		}

		// Post-process generated Go code to fix imports
		if err := fixGoImports(genDir, goModulePath); err != nil {
			return fmt.Errorf("failed to fix imports: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	ui.PrintSuccess("Generated", "Flatbuffers Go code")

		// Generate C++ code
	if err := ui.RunSpinner("Generating Flatbuffers C++ code...", func() error {
		// Generate for protocol.fbs first
		cmdProtocol := exec.Command(flatcPath, "--cpp", "-o", includeDir, protocolPath)
		out, err := cmdProtocol.CombinedOutput()
		if err != nil {
			return fmt.Errorf("flatc (cpp) for protocol.fbs failed: %w\n%s", err, string(out))
		}

		// Then generate for schema.fbs, which includes protocol.fbs
		cmdSchema := exec.Command(flatcPath, "--cpp", "-o", includeDir, schemaPath)
		out, err = cmdSchema.CombinedOutput()
		if err != nil {
			return fmt.Errorf("flatc (cpp) for schema.fbs failed: %w\n%s", err, string(out))
		}

		return nil
	}); err != nil {
		return err
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

	if err := generateTaskfile(cfg, baseDir); err != nil {
		return err
	}
	ui.PrintSuccess("Generated", "Taskfile.yml")

	// Dependencies update
	if err := updateDependencies(baseDir, opts); err != nil {
		return err
	}

	return nil
}
