package generator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/xll-gen/xll-gen/internal/assets"
	"github.com/xll-gen/xll-gen/internal/config"
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
	fmt.Printf("Generating code for project: %s\n", cfg.Project.Name)

	// 1. Ensure directories
	genDir := "generated"
	cppDir := filepath.Join(genDir, "cpp")
	if err := os.MkdirAll(cppDir, 0755); err != nil {
		return err
	}

	// 2. Write Assets (C++ common files)
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

	// 3. Generate xltypes.fbs
	xlTypesPath := filepath.Join(genDir, "xltypes.fbs")
	if err := generateXlTypes(xlTypesPath); err != nil {
		return err
	}
	fmt.Println("Generated xltypes.fbs")

	// 4. Generate schema.fbs
	schemaPath := filepath.Join(genDir, "schema.fbs")
	if err := generateSchema(cfg, schemaPath); err != nil {
		return err
	}
	fmt.Println("Generated schema.fbs")

	goModulePath := modName + "/generated"

	// 5. Run flatc
	flatcPath, err := EnsureFlatc()
	if err != nil {
		return err
	}

	// Generate Go code for xltypes
	cmd := exec.Command(flatcPath, "--go", "--go-module-name", goModulePath, "-o", genDir, xlTypesPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("flatc (go xltypes) failed: %w", err)
	}

	// Generate C++ code for xltypes
	cmd = exec.Command(flatcPath, "--cpp", "-o", cppDir, xlTypesPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("flatc (cpp xltypes) failed: %w", err)
	}

	// Generate Go code for schema
	cmd = exec.Command(flatcPath, "--go", "--go-namespace", "ipc", "--go-module-name", goModulePath, "-o", genDir, schemaPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("flatc (go) failed: %w", err)
	}
	fmt.Println("Generated Flatbuffers Go code")

	// Generate C++ code
	cmd = exec.Command(flatcPath, "--cpp", "-o", cppDir, schemaPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("flatc (cpp) failed: %w", err)
	}
	fmt.Println("Generated Flatbuffers C++ code")

	// 6. Generate interface.go
	if err := generateInterface(cfg, genDir, modName); err != nil {
		return err
	}
	fmt.Println("Generated interface.go")

	// 7. Generate server.go
	if err := generateServer(cfg, genDir, modName); err != nil {
		return err
	}
	fmt.Println("Generated server.go")

	// 8. Generate xll_main.cpp
	shouldAppendPid := !cfg.Gen.DisablePidSuffix && !opts.DisablePidSuffix
	if err := generateCppMain(cfg, cppDir, shouldAppendPid); err != nil {
		return err
	}
	fmt.Println("Generated xll_main.cpp")

	// 9. Generate CMakeLists.txt
	if err := generateCMake(cfg, cppDir); err != nil {
		return err
	}
	fmt.Println("Generated CMakeLists.txt")

	// 10. Generate Taskfile.yml
	if err := generateTaskfile(cfg, "."); err != nil {
		return err
	}
	fmt.Println("Generated Taskfile.yml")

	// 11. Run go mod tidy
	fmt.Println("Running 'go mod tidy'...")
	cmdTidy := exec.Command("go", "mod", "tidy")
	cmdTidy.Stdout = os.Stdout
	cmdTidy.Stderr = os.Stderr
	if err := cmdTidy.Run(); err != nil {
		fmt.Printf("Warning: 'go mod tidy' failed: %v. You may need to run it manually after checking dependencies.\n", err)
	}

	fmt.Println("Done.")

	return nil
}
