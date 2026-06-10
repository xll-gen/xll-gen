package regtest

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/platform"
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

	// Locate mock_host (single-config vs multi-config build dirs).
	mockPath, err := platform.FindBuiltExe(filepath.Join(simDir, "build"), "mock_host")
	if err != nil {
		return err
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
		// The server has no stdin shutdown protocol, so force-kill is the only
		// lever. Always Wait() after Kill() to reap the process and release its
		// OS handles — a bare Kill() leaves a zombie/handle leak.
		if serverCmd.Process != nil {
			_ = serverCmd.Process.Kill()
			_ = serverCmd.Wait()
		}
	}()

	// Read remaining output from Host
	// The host should run tests and exit. The channel is buffered (size 1) so
	// the consumer goroutine can always deliver hostCmd.Wait() and exit even
	// if we took the timeout branch below and stopped receiving — otherwise it
	// would block forever on the send and leak.
	exitCode := 0
	done := make(chan error, 1)
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
		// Force-kill the host AND reap it. The deferred Kill() only guards the
		// process pointer; we additionally drain `done` so the consumer
		// goroutine's hostCmd.Wait() completes and the goroutine exits rather
		// than leaking. The buffered channel means the send never blocks even
		// if Wait() returns after this select has moved on.
		if hostCmd.Process != nil {
			_ = hostCmd.Process.Kill()
		}
		<-done
	}

	if exitCode == 0 {
		fmt.Println("Simulation PASSED")
	} else {
		fmt.Println("Simulation FAILED")
		return fmt.Errorf("simulation failed (exit code %d)", exitCode)
	}

	return nil
}
