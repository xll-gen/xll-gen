package regtest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/generator"
	"gopkg.in/yaml.v3"
)

// runGenerate replicates the logic of `cmd.runGenerate` to avoid package cycles.
func runGenerate() error {
	data, err := os.ReadFile("xll.yaml")
	if err != nil {
		return fmt.Errorf("failed to read xll.yaml: %w", err)
	}

	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse xll.yaml: %w", err)
	}

	config.ApplyDefaults(&cfg)

	if err := config.Validate(&cfg); err != nil {
		return err
	}

	modName, err := getModuleName()
	if err != nil {
		return err
	}

	// regtest always uses default options (PidSuffix allowed)
	opts := generator.Options{
		DisablePidSuffix: false,
	}

	return generator.Generate(&cfg, ".", modName, opts)
}

func getModuleName() (string, error) {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		return "", fmt.Errorf("failed to read go.mod: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	return "", fmt.Errorf("module name not found in go.mod")
}

func buildGoServer(cfg *config.Config) (string, error) {
	fmt.Println("[2/6] Building Go server...")

	// Ensure dependencies
	if err := exec.Command("go", "mod", "tidy").Run(); err != nil {
		return "", fmt.Errorf("go mod tidy failed: %w", err)
	}

	serverBinName := cfg.Project.Name
	if runtime.GOOS == "windows" {
		serverBinName += ".exe"
	}
	buildDir := "build"
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return "", err
	}
	serverPath := filepath.Join(buildDir, serverBinName)

	buildCmd := exec.Command("go", "build", "-o", serverPath, "main.go")
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to build Go server: %w", err)
	}
	return serverPath, nil
}

func buildSimulationHost(simDir string) error {
	fmt.Println("[4/6] Building Simulation Host...")
	// cmake -S temp_regtest -B temp_regtest/build
	cmakeConfig := exec.Command("cmake", "-S", simDir, "-B", filepath.Join(simDir, "build"))
	// Quiet output unless error
	if out, err := cmakeConfig.CombinedOutput(); err != nil {
		fmt.Println(string(out))
		return fmt.Errorf("cmake config failed: %w", err)
	}

	// cmake --build temp_regtest/build --config Release
	cmakeBuild := exec.Command("cmake", "--build", filepath.Join(simDir, "build"), "--config", "Release")
	if out, err := cmakeBuild.CombinedOutput(); err != nil {
		fmt.Println(string(out))
		return fmt.Errorf("cmake build failed: %w", err)
	}
	return nil
}
