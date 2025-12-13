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

	// 3. Generate protocol.fbs (Static System Types)
	protocolPath := filepath.Join(genDir, "protocol.fbs")
	if err := generateProtocol(protocolPath); err != nil {
		return err
	}
	fmt.Println("Generated protocol.fbs")

	// 4. Generate schema.fbs (User Types)
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

	fmt.Println("Generated Flatbuffers Go code")

	// Generate C++ code
	// We do NOT use --no-includes here because the C++ header for protocol.fbs needs to be available
	// or generated. In the user project, we want flatc to generate everything self-contained.
	// However, we are including protocol.fbs.
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

	// 11. Run go get shm@v0.5.3 (Ensure latest SHM for new features)
	fmt.Println("Updating SHM dependency to v0.5.3...")
	cmdGet := exec.Command("go", "get", "github.com/xll-gen/shm@v0.5.3")
	cmdGet.Stdout = os.Stdout
	cmdGet.Stderr = os.Stderr
	if err := cmdGet.Run(); err != nil {
		fmt.Printf("Warning: 'go get shm' failed: %v\n", err)
	}

	// 12. Run go mod tidy
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
