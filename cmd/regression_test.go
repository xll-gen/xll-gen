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

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/generator"
	"github.com/xll-gen/xll-gen/internal/regtest"
	"gopkg.in/yaml.v3"
)

// --- Helpers ---

// setupMockFlatc creates a dummy flatc binary in the temp dir.
// Returns the path to the binary and a cleanup function.
func setupMockFlatc(t *testing.T, tempDir string) (string, func()) {
	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}

	flatcName := "flatc"
	if runtime.GOOS == "windows" {
		flatcName += ".exe"
	}
	flatcPath := filepath.Join(binDir, flatcName)

	// Mock flatc that satisfies EnsureFlatc check if possible, or just exists.
	script := "#!/bin/sh\necho flatc version 25.9.23\n"
	if runtime.GOOS == "windows" {
		script = "@echo off\necho flatc version 25.9.23\n"
	}
	if err := os.WriteFile(flatcPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	// Make executable on unix
	if runtime.GOOS != "windows" {
		os.Chmod(flatcPath, 0755)
	}

	return flatcPath, func() {
		os.Remove(flatcPath)
	}
}

// runGenerateInDir runs the generator in the specified directory.
func runGenerateInDir(t *testing.T, dir string, opts generator.Options) {
	// Read xll.yaml
	cfgPath := filepath.Join(dir, "xll.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("failed to read xll.yaml: %v", err)
	}
	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to parse xll.yaml: %v", err)
	}
	config.ApplyDefaults(&cfg)
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("config validate failed: %v", err)
	}

	// Read go.mod for module name
	goModPath := filepath.Join(dir, "go.mod")
	modData, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatalf("failed to read go.mod: %v", err)
	}
	modName := ""
	for _, line := range strings.Split(string(modData), "\n") {
		if strings.HasPrefix(line, "module ") {
			modName = strings.TrimSpace(strings.TrimPrefix(line, "module "))
			break
		}
	}
	if modName == "" {
		t.Fatalf("module name not found in go.mod")
	}

	if err := generator.Generate(&cfg, dir, modName, opts); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
}

// setupGenTest prepares a temporary directory and runs init.
// It returns the projectDir (inside tempDir) and a cleanup function.
func setupGenTest(t *testing.T, name string) (string, func()) {
	tempDir, err := os.MkdirTemp("", "xll-test-"+name)
	if err != nil {
		t.Fatal(err)
	}

	projectDir := filepath.Join(tempDir, name)
	if err := runInit(projectDir, true, false); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	return projectDir, func() {
		os.RemoveAll(tempDir)
	}
}

func checkContent(t *testing.T, path string, mustContain []string, mustNotContain []string) {
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Could not read %s: %v", path, err)
	}
	sContent := string(content)

	for _, s := range mustContain {
		if !strings.Contains(sContent, s) {
			t.Errorf("%s missing expected content: %q", path, s)
		}
	}
	for _, s := range mustNotContain {
		if strings.Contains(sContent, s) {
			t.Errorf("%s contains forbidden content: %q", path, s)
		}
	}
}

// --- Tests ---

// TestGenerate_Fixes verifies that specific bugs are not present in the generated code.
func TestGenerate_Fixes(t *testing.T) {
	t.Parallel()
	projectDir, cleanup := setupGenTest(t, "repro_project")
	defer cleanup()

	flatcPath, pathCleanup := setupMockFlatc(t, filepath.Dir(projectDir))
	defer pathCleanup()

	runGenerateInDir(t, projectDir, generator.Options{FlatcPath: flatcPath})

	checkContent(t, filepath.Join(projectDir, "generated/cpp/xll_main.cpp"),
		[]string{
			"LPXLOPER12 name", // Correct String Arg Type
		},
		[]string{
			"const XLL_PASCAL_STRING* name", // Incorrect String Arg Type
			"void __stdcall xlAutoFree12",   // Duplicate definition
			"xll::MemoryPool",               // Internal usage
		})

	checkContent(t, filepath.Join(projectDir, "generated", "cpp", "include", "xll_worker.cpp"),
		[]string{
			"if (msgType == (shm::MsgType)MSG_BATCH_ASYNC_RESPONSE)", // MSG_BATCH_ASYNC_RESPONSE
			"return 1;",                                              // ACK
		}, nil)

	checkContent(t, filepath.Join(projectDir, "generated/server.go"),
		[]string{
			"select {",
			"case jobQueue <- func() {",
			"default:",
		}, nil)

	// Check xll_log.cpp fixes
	checkContent(t, filepath.Join(projectDir, "generated", "cpp", "include", "xll_log.cpp"),
		[]string{
			"g_logPath = WideToUtf8(path)", // Check correct assignment
			"g_logLevel = LogLevel::INFO;", // Check default or assignment
		},
		[]string{
			"base + L\"_native\" + ext", // Check bad assignment
		})
}

// TestRepro_MemoryLeak verifies that memory leak fixes are present.
func TestRepro_MemoryLeak(t *testing.T) {
	t.Parallel()
	projectDir, cleanup := setupGenTest(t, "mem_check")
	defer cleanup()

	flatcPath, pathCleanup := setupMockFlatc(t, filepath.Dir(projectDir))
	defer pathCleanup()

	runGenerateInDir(t, projectDir, generator.Options{FlatcPath: flatcPath})

	// 1. xll_mem.cpp (xlAutoFree12 leak)
	checkContent(t, filepath.Join(projectDir, "generated", "cpp", "include", "xll_mem.cpp"),
		[]string{
			"xltypeRef",                          // Handled
			"delete[] (char*)p->val.mref.lpmref", // Correct deletion
		}, nil)

	// 2. xll_converters.cpp (AnyToXLOPER12 leaks and missing features)
	checkContent(t, filepath.Join(projectDir, "generated", "cpp", "include", "xll_converters.cpp"),
		[]string{
			"case protocol::AnyValue::Range:", // Missing feature fixed
			"new char[sizeof(XLMREF12)",       // Correct Allocation for Ref
		},
		[]string{
			"x->xltype = xltypeInt;",  // Missing xlbitDLLFree
			"x->xltype = xltypeNum;",
			"x->xltype = xltypeBool;",
			"x->xltype = xltypeErr;",
			"x->xltype = xltypeSRef;", // RangeToXLOPER12 leak
		})

	// 3. xll_async.cpp (Use safe cleanup)
	checkContent(t, filepath.Join(projectDir, "generated", "cpp", "include", "xll_async.cpp"),
		[]string{
			"xlAutoFree12(pxResult)", // Safe cleanup used
		}, nil)
}

// TestRepro_NestedIPC_Corruption verifies that nested IPC calls do not corrupt the zero-copy slot.
func TestRepro_NestedIPC_Corruption(t *testing.T) {
	t.Parallel()
	projectDir, cleanup := setupGenTest(t, "repro_project")
	defer cleanup()

	flatcPath, pathCleanup := setupMockFlatc(t, filepath.Dir(projectDir))
	defer pathCleanup()

	runGenerateInDir(t, projectDir, generator.Options{FlatcPath: flatcPath})

	// Verify ConvertAny does not use GetZeroCopySlot
	checkContent(t, filepath.Join(projectDir, "generated", "cpp", "include", "xll_converters.cpp"),
		[]string{
			"g_host.Send(", // Must use Send
		},
		[]string{
			// "g_host.GetZeroCopySlot()", // This might appear in OTHER functions, so we can't globally ban it.
		})

	// Specific check for ConvertAny body
	content, _ := os.ReadFile(filepath.Join(projectDir, "generated", "cpp", "include", "xll_converters.cpp"))
	sContent := string(content)
	start := "flatbuffers::Offset<ipc::types::Any> ConvertAny"
	idx := strings.Index(sContent, start)
	if idx != -1 {
		body := sContent[idx:]
		if strings.Contains(body, "g_host.GetZeroCopySlot()") {
			t.Error("ConvertAny uses g_host.GetZeroCopySlot() (Corruption Risk)")
		}
	}
}

// TestGenerateStringArgRepro verifies that string arguments are correctly handled.
func TestGenerateStringArgRepro(t *testing.T) {
	t.Parallel()
	// Custom setup because we need specific xll.yaml
	tempDir, err := os.MkdirTemp("", "xll-gen-string-repro")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	flatcPath, pathCleanup := setupMockFlatc(t, tempDir)
	defer pathCleanup()

	xllContent := `project:
  name: "repro-string-arg"
  version: "0.1.0"
gen:
  go:
    package: "generated"
functions:
  - name: "Echo"
    description: "Echoes a string"
    args:
      - name: "msg"
        type: "string"
    return: "string"
`
	if err := os.WriteFile(filepath.Join(tempDir, "xll.yaml"), []byte(xllContent), 0644); err != nil {
		t.Fatal(err)
	}
	// Dummy go.mod
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte("module repro-string-arg\n"), 0644); err != nil {
		t.Fatal(err)
	}

	runGenerateInDir(t, tempDir, generator.Options{FlatcPath: flatcPath})

	checkContent(t, filepath.Join(tempDir, "generated", "cpp", "xll_main.cpp"),
		[]string{
			"ConvertExcelString",
		},
		[]string{
			"WStringToString(msg)",
		})

	checkContent(t, filepath.Join(tempDir, "generated", "cpp", "include", "xll_utility.cpp"),
		[]string{
			"std::string ConvertExcelString(const wchar_t* wstr)",
		}, nil)
}

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
