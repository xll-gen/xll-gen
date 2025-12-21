package regtest

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/xll-gen/xll-gen/internal/config"
	"gopkg.in/yaml.v3"
)

// Run orchestrates the regression testing process.
// It builds the Go server, generates a C++ mock host, and runs a simulation to verify IPC.
//
// Returns:
//   - error: An error if any step of the test suite fails.
func Run() error {
	// 1. Check prerequisites
	if _, err := exec.LookPath("cmake"); err != nil {
		return fmt.Errorf("cmake not found. Please install CMake")
	}

	// 2. Run Generate
	fmt.Println("[1/6] Running generate...")
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
	serverPath, err := buildGoServer(&cfg)
	if err != nil {
		return err
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
	if err := buildSimulationHost(simDir); err != nil {
		return err
	}

	// 7. Run Simulation
	return runSimulation(&cfg, simDir, serverPath)
}

func runSimulation(cfg *config.Config, simDir, serverPath string) error {
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
