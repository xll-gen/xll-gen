package cmd

import (
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xll-gen/xll-gen/internal/generator"
	"github.com/xll-gen/xll-gen/internal/regtest"
)

// TestRegression runs an end-to-end regression test.
func TestRegression(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping regression test in short mode")
	}

	// 1. Setup Temp Dir
	tempDir, err := os.MkdirTemp("", "xll-regression-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// 2. Init Project with Unique Name and SHM
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	projectName := "smoke_proj"
	shmName := fmt.Sprintf("smoke_proj_%d", rnd.Intn(100000))

	origWd, _ := os.Getwd()
	repoRoot := origWd
	if filepath.Base(repoRoot) == "cmd" {
		repoRoot = filepath.Dir(repoRoot)
	}

	projectDir := filepath.Join(tempDir, projectName)
	if err := runInit(projectDir, false, false); err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	// Go Mod Replace
	editCmd := exec.Command("go", "mod", "edit", "-replace", "github.com/xll-gen/xll-gen="+repoRoot)
	editCmd.Dir = projectDir
	if out, err := editCmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod edit replace failed: %v\nOutput: %s", err, out)
	}

	if err := os.WriteFile(filepath.Join(projectDir, "xll.yaml"), []byte(regtest.XllYaml), 0644); err != nil {
		t.Fatal(err)
	}

	runGenerateInDir(t, projectDir, generator.Options{})

	if err := os.WriteFile(filepath.Join(projectDir, "main.go"), []byte(regtest.ServerGo), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = projectDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy failed: %v\nOutput: %s", err, out)
	}

	serverBin := projectName
	if runtime.GOOS == "windows" {
		serverBin += ".exe"
	}
	if err := os.MkdirAll(filepath.Join(projectDir, "build"), 0755); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("go", "build", "-o", filepath.Join("build", serverBin), "main.go")
	cmd.Dir = projectDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\nOutput: %s", err, out)
	}

	simDir := filepath.Join(projectDir, "temp_simulation")
	if err := os.MkdirAll(simDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(simDir, "CMakeLists.txt"), []byte(regtest.CMakeLists), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(simDir, "main.cpp"), []byte(regtest.MockHostCpp), 0644); err != nil {
		t.Fatal(err)
	}

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	regtestCache := filepath.Join(cacheDir, "xll-gen", "regtest_cache")
	if err := os.MkdirAll(regtestCache, 0755); err != nil {
		t.Logf("Failed to create regtest cache dir: %v", err)
	}

	cmd = exec.Command("cmake", "-S", simDir, "-B", filepath.Join(simDir, "build"), "-DFETCHCONTENT_BASE_DIR="+regtestCache)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cmake config failed: %s", out)
	}

	cmd = exec.Command("cmake", "--build", filepath.Join(simDir, "build"), "--config", "Release")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cmake build failed: %s", out)
	}

	mockBin := filepath.Join(simDir, "build", "mock_host")
	if runtime.GOOS == "windows" {
		if _, err := os.Stat(mockBin + ".exe"); os.IsNotExist(err) {
			mockBin = filepath.Join(simDir, "build", "Release", "mock_host.exe")
		} else {
			mockBin += ".exe"
		}
	}

	// Run Mock Host with unique SHM name
	mockCmd := exec.Command(mockBin, shmName)
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

	// Run Server with unique SHM name
	// The server reads xll.yaml, but we are launching it directly.
	// The -xll-shm flag overrides the generated SHM name.
	serverCmd := exec.Command(filepath.Join(projectDir, "build", serverBin), "-xll-shm="+shmName)
	serverCmd.Dir = projectDir
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
