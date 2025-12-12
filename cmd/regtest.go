//go:build regtest

package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"

	"github.com/spf13/cobra"
	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/templates"
	"gopkg.in/yaml.v3"
)

// regtestCmd represents the regtest command.
var regtestCmd = &cobra.Command{
	Use:   "regtest",
	Short: "Run a regression test simulation (Mock Host)",
	Run: func(cmd *cobra.Command, args []string) {
		if err := runRegtest(); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(regtestCmd)
}

// runRegtest orchestrates the regression testing process.
// It builds the Go server, generates a C++ mock host, and runs a simulation to verify IPC.
//
// Returns:
//   - error: An error if any step of the test suite fails.
func runRegtest() error {
	// 1. Check prerequisites
	if _, err := exec.LookPath("cmake"); err != nil {
		return fmt.Errorf("cmake not found. Please install CMake")
	}

	// 2. Run Generate
	fmt.Println("[1/6] Running generate...")
	// Calling runGenerate from generate.go (in same package)
	if err := runGenerate(); err != nil {
		return err
	}

	// 3. Load Config
	data, err := os.ReadFile("xll.yaml")
	if err != nil {
		return fmt.Errorf("failed to read xll.yaml: %w", err)
	}
	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse xll.yaml: %w", err)
	}

	// 4. Build Go Server
	fmt.Println("[2/6] Building Go server...")

	// Ensure dependencies
	if err := exec.Command("go", "mod", "tidy").Run(); err != nil {
		return fmt.Errorf("go mod tidy failed: %w", err)
	}

	serverBinName := cfg.Project.Name
	if runtime.GOOS == "windows" {
		serverBinName += ".exe"
	}
	buildDir := "build"
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return err
	}
	serverPath := filepath.Join(buildDir, serverBinName)

	buildCmd := exec.Command("go", "build", "-o", serverPath, "main.go")
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("failed to build Go server: %w", err)
	}

	// 5. Generate Simulation Host
	fmt.Println("[3/6] Generating Simulation Host...")
	simDir := "temp_regtest"
	if err := os.MkdirAll(simDir, 0755); err != nil {
		return err
	}
	if err := generateSimMain(&cfg, simDir); err != nil {
		return err
	}
	if err := generateSimCMake(&cfg, simDir); err != nil {
		return err
	}

	// 6. Build Simulation Host
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

	// 7. Run Simulation
	fmt.Println("[5/6] Starting Simulation...")

	// Locate mock_host
	mockBinName := "mock_host"
	if runtime.GOOS == "windows" {
		mockBinName += ".exe"
	}
	mockPath := filepath.Join(simDir, "build", mockBinName)
	if _, err := os.Stat(mockPath); os.IsNotExist(err) {
		mockPath = filepath.Join(simDir, "build", "Release", mockBinName)
	}

	// Start Mock Host
	hostCmd := exec.Command(mockPath)
	hostStdout, err := hostCmd.StdoutPipe()
	if err != nil {
		return err
	}
	hostCmd.Stderr = os.Stderr
	if err := hostCmd.Start(); err != nil {
		return fmt.Errorf("failed to start mock host: %w", err)
	}
	defer func() {
		if hostCmd.Process != nil {
			hostCmd.Process.Kill()
		}
	}()

	// Wait for "READY"
	scanner := bufio.NewScanner(hostStdout)
	ready := false
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Println("[MockHost]", line)
		if strings.Contains(line, "READY") {
			ready = true
			break
		}
	}
	if !ready {
		return fmt.Errorf("mock host failed to start or did not signal READY")
	}

	// Start Go Server
	fmt.Println("[6/6] Starting Go Server...")
	serverCmd := exec.Command(serverPath, "-xll-shm="+cfg.Project.Name)
	serverCmd.Stdout = os.Stdout
	serverCmd.Stderr = os.Stderr
	if err := serverCmd.Start(); err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}
	defer func() {
		if serverCmd.Process != nil {
			serverCmd.Process.Kill()
		}
	}()

	// Read remaining output from Host
	// The host should run tests and exit
	exitCode := 0
	done := make(chan error)
	go func() {
		// Consume rest of stdout
		for scanner.Scan() {
			fmt.Println("[MockHost]", scanner.Text())
		}
		done <- hostCmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			// Check if it's an exit code error
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
		}
	case <-time.After(30 * time.Second):
		fmt.Println("Simulation timed out")
		exitCode = 1
		hostCmd.Process.Kill()
	}

	if exitCode == 0 {
		fmt.Println("Simulation PASSED")
	} else {
		fmt.Println("Simulation FAILED")
	}

	return nil
}

// generateSimMain generates the C++ main file for the simulation host.
//
// Parameters:
//   - cfg: The project configuration.
//   - dir: The directory where the file should be generated.
//
// Returns:
//   - error: An error if generation fails.
func generateSimMain(cfg *config.Config, dir string) error {
	tmplContent, err := templates.Get("regtest_main.cpp.tmpl")
	if err != nil {
		return err
	}

	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
	}

	t, err := template.New("regtest_main").Funcs(funcMap).Parse(tmplContent)
	if err != nil {
		return err
	}

	f, err := os.Create(filepath.Join(dir, "main.cpp"))
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, cfg)
}

// generateSimCMake generates the CMakeLists.txt for the simulation host.
func generateSimCMake(cfg *config.Config, dir string) error {
	tmplContent, err := templates.Get("regtest_CMakeLists.txt.tmpl")
	if err != nil {
		return err
	}

	t, err := template.New("regtest_cmake").Parse(tmplContent)
	if err != nil {
		return err
	}

	f, err := os.Create(filepath.Join(dir, "CMakeLists.txt"))
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, cfg)
}
