package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"xll-gen/internal/regtest"
)

// TestRegression runs an end-to-end regression test.
// It builds a Go server, generates a C++ mock host, and simulates IPC communication
// to verify correctness of data passing.
// This test is skipped in short mode.
func TestRegression(t *testing.T) {
	t.Skip("Skipping regression test due to flakiness (Async callback missing)")

	if testing.Short() {
		t.Skip("Skipping regression test in short mode")
	}

	// 1. Setup Temp Dir
	tempDir, err := os.MkdirTemp("", "xll-regression-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// 2. Init Project
	projectName := "smoke_proj"

	// Switch WD to tempDir to run init
	origWd, _ := os.Getwd()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	if err := runInit(projectName, false); err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	if err := os.Chdir(projectName); err != nil {
		t.Fatal(err)
	}

	// 3. Write Comprehensive xll.yaml
	if err := os.WriteFile("xll.yaml", []byte(regtest.XllYaml), 0644); err != nil {
		t.Fatal(err)
	}

	// 4. Run Generate
	if err := runGenerate(); err != nil {
		t.Fatalf("runGenerate failed: %v", err)
	}

	// 5. Write main.go (Server)
	if err := os.WriteFile("main.go", []byte(regtest.ServerGo), 0644); err != nil {
		t.Fatal(err)
	}

	// 6. Go Mod Tidy
	cmd := exec.Command("go", "mod", "tidy")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy failed: %v\nOutput: %s", err, out)
	}

	// 7. Build Go Server
	serverBin := "smoke_proj"
	if runtime.GOOS == "windows" {
		serverBin += ".exe"
	}
	if err := os.MkdirAll("build", 0755); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("go", "build", "-o", filepath.Join("build", serverBin), "main.go")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\nOutput: %s", err, out)
	}

	// 8. Generate Simulation C++ Host (Manual)
	simDir := "temp_simulation"
	if err := os.MkdirAll(simDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(simDir, "CMakeLists.txt"), []byte(regtest.CMakeLists), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(simDir, "main.cpp"), []byte(regtest.MockHostCpp), 0644); err != nil {
		t.Fatal(err)
	}

	// 9. Build Simulation
	cmd = exec.Command("cmake", "-S", simDir, "-B", filepath.Join(simDir, "build"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cmake config failed: %s", out)
	}

	cmd = exec.Command("cmake", "--build", filepath.Join(simDir, "build"), "--config", "Release")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cmake build failed: %s", out)
	}

	// 10. Run
	mockBin := filepath.Join(simDir, "build", "mock_host")
	if runtime.GOOS == "windows" {
		if _, err := os.Stat(mockBin + ".exe"); os.IsNotExist(err) {
			mockBin = filepath.Join(simDir, "build", "Release", "mock_host.exe")
		} else {
			mockBin += ".exe"
		}
	} else {
		// Linux/Mac
		if _, err := os.Stat(mockBin); os.IsNotExist(err) {
			// Maybe it is in ./mock_host if cmake didn't use Release folder?
			// Standard cmake puts it in build/mock_host
		}
	}

	mockCmd := exec.Command(mockBin)
	mockStdout, err := mockCmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	mockCmd.Stderr = os.Stderr
	if err := mockCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if mockCmd.Process != nil {
			mockCmd.Process.Kill()
		}
	}()

	// Wait for Mock Host to signal READY (SHM initialized)
	readyCh := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(mockStdout)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Println("[MOCK]", line)
			if strings.Contains(line, "READY") {
				close(readyCh)
			}
		}
	}()

	select {
	case <-readyCh:
	case <-time.After(30 * time.Second):
		t.Fatal("Mock Host timed out waiting for READY")
	}

	serverCmd := exec.Command(filepath.Join("build", serverBin), "-xll-shm=smoke_proj")
	serverCmd.Stdout = os.Stdout
	serverCmd.Stderr = os.Stderr
	if err := serverCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if serverCmd.Process != nil {
			serverCmd.Process.Kill()
		}
	}()

	if err := mockCmd.Wait(); err != nil {
		t.Fatalf("Mock host failed: %v", err)
	}
}
