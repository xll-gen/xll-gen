//go:build windows

package smoketest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/generator"
	"gopkg.in/yaml.v3"
)

// buildArtifacts produces <projectDir>/<name>.exe (Go server) and
// <projectDir>/generated/cpp/build/<name>.xll (XLL add-in) using the same
// pipeline TestRegression uses, parameterized to allow caller-supplied repo
// roots and FetchContent cache.
type buildArtifacts struct {
	ProjectDir string
	XLLPath    string
	ServerExe  string
}

func prepareProject(projectDir, repoRoot string) error {
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(projectDir, "xll.yaml"), []byte(xllYaml), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(projectDir, "main.go"), []byte(serverMain), 0o644); err != nil {
		return err
	}

	if out, err := runIn(projectDir, "go", "mod", "init", "xll_smoke"); err != nil {
		return fmt.Errorf("go mod init: %w\n%s", err, out)
	}
	if out, err := runIn(projectDir, "go", "mod", "edit",
		"-replace", "github.com/xll-gen/xll-gen="+repoRoot); err != nil {
		return fmt.Errorf("go mod edit replace: %w\n%s", err, out)
	}
	return nil
}

func generateCode(projectDir string) (*config.Config, error) {
	data, err := os.ReadFile(filepath.Join(projectDir, "xll.yaml"))
	if err != nil {
		return nil, err
	}
	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	config.ApplyDefaults(&cfg)
	if err := config.Validate(&cfg); err != nil {
		return nil, err
	}
	if err := generator.Generate(&cfg, projectDir, "xll_smoke", generator.Options{}); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func buildServer(projectDir, name string) (string, error) {
	if out, err := runIn(projectDir, "go", "mod", "tidy"); err != nil {
		return "", fmt.Errorf("go mod tidy: %w\n%s", err, out)
	}
	buildDir := filepath.Join(projectDir, "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return "", err
	}
	exe := filepath.Join(buildDir, name+".exe")
	if out, err := runIn(projectDir, "go", "build", "-o", exe, "main.go"); err != nil {
		return "", fmt.Errorf("go build: %w\n%s", err, out)
	}
	return exe, nil
}

func buildXLL(projectDir, fetchCache string) (string, error) {
	srcDir := filepath.Join(projectDir, "generated", "cpp")
	bldDir := filepath.Join(srcDir, "build")

	cfgArgs := []string{"-S", srcDir, "-B", bldDir}
	if fetchCache != "" {
		cfgArgs = append(cfgArgs, "-DFETCHCONTENT_BASE_DIR="+fetchCache)
	}
	// The generated CMakeLists fetches `types` at the pinned tag (Deps.Types),
	// which does NOT yet ship the date auto-format symbols (CollectDateCells /
	// ScheduleDateFormatsForCaller / IsDateLikeFormat). When XLLGEN_TYPES_SRC
	// points at a local types checkout that has them, redirect the FetchContent
	// source so the smoke XLL compiles against the local source. No-op when the
	// env var is unset. Mirrors cpp_compile_gate_test.go.
	if typesSrc := os.Getenv("XLLGEN_TYPES_SRC"); typesSrc != "" {
		cfgArgs = append(cfgArgs, "-DFETCHCONTENT_SOURCE_DIR_TYPES="+typesSrc)
	}
	if out, err := runIn(projectDir, "cmake", cfgArgs...); err != nil {
		return "", fmt.Errorf("cmake configure: %w\n%s", err, out)
	}
	if out, err := runIn(projectDir, "cmake", "--build", bldDir, "--config", "Release"); err != nil {
		return "", fmt.Errorf("cmake build: %w\n%s", err, out)
	}

	// Output paths differ between single-config (Make/Ninja) and multi-config (MSVC).
	candidates := []string{
		filepath.Join(projectDir, "xll_smoke.xll"),                  // CMAKE_RUNTIME_OUTPUT_DIRECTORY = bldDir/.. = generated/cpp
		filepath.Join(srcDir, "xll_smoke.xll"),                      // same as above, explicit
		filepath.Join(bldDir, "..", "Release", "xll_smoke.xll"),     // MSVC under cpp/Release/
		filepath.Join(bldDir, "xll_smoke.xll"),                      // single-config build dir
		filepath.Join(bldDir, "Release", "xll_smoke.xll"),           // MSVC build subdir
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if _, err := os.Stat(abs); err == nil {
			return abs, nil
		}
	}
	return "", fmt.Errorf("xll_smoke.xll not found after cmake build; searched %v", candidates)
}

// colocateServer makes sure the server exe lives next to the XLL so the
// XLL's LaunchServer logic (xll_main.cpp.tmpl) finds it via GetXllDir().
func colocateServer(xllPath, serverExe string) (string, error) {
	target := filepath.Join(filepath.Dir(xllPath), filepath.Base(serverExe))
	if target == serverExe {
		return target, nil
	}
	in, err := os.ReadFile(serverExe)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(target, in, 0o755); err != nil {
		return "", err
	}
	return target, nil
}

func runIn(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}
