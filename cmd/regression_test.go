package cmd

import (
	"bufio"
	"fmt"
	"github.com/xll-gen/xll-gen/internal/regtest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// --- Helpers ---

// setupMockFlatc creates a dummy flatc binary in the temp dir and adds it to PATH.
// Returns a cleanup function to restore PATH.
func setupMockFlatc(t *testing.T, tempDir string) func() {
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

	// Update PATH
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)

	return func() {
		os.Setenv("PATH", oldPath)
	}
}

// setupGenTest prepares a temporary directory, runs init, and changes WD.
// It returns the tempDir and a cleanup function.
func setupGenTest(t *testing.T, name string) (string, func()) {
	tempDir, err := os.MkdirTemp("", "xll-test-"+name)
	if err != nil {
		t.Fatal(err)
	}

	origWd, _ := os.Getwd()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}

	if err := runInit(name, true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if err := os.Chdir(name); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}

	return tempDir, func() {
		os.Chdir(origWd)
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
	tempDir, cleanup := setupGenTest(t, "repro_project")
	defer cleanup()

	pathCleanup := setupMockFlatc(t, tempDir)
	defer pathCleanup()

	if err := runGenerate(); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	checkContent(t, "generated/cpp/xll_main.cpp",
		[]string{
			"case (shm::MsgType)128:", // MSG_BATCH_ASYNC_RESPONSE
			"return 1;",               // ACK
			"LPXLOPER12 name",         // Correct String Arg Type
		},
		[]string{
			"const XLL_PASCAL_STRING* name", // Incorrect String Arg Type
			"void __stdcall xlAutoFree12",   // Duplicate definition
			"xll::MemoryPool",               // Internal usage
		})

	checkContent(t, "generated/server.go",
		[]string{
			"select {",
			"case jobQueue <- func() {",
			"default:",
		}, nil)
}

// TestRepro_MemoryLeak verifies that memory leak fixes are present.
func TestRepro_MemoryLeak(t *testing.T) {
	tempDir, cleanup := setupGenTest(t, "mem_check")
	defer cleanup()

	pathCleanup := setupMockFlatc(t, tempDir)
	defer pathCleanup()

	if err := runGenerate(); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// 1. xll_mem.cpp (xlAutoFree12 leak)
	checkContent(t, filepath.Join("generated", "cpp", "include", "xll_mem.cpp"),
		[]string{
			"xltypeRef", // Handled
			"delete[] (char*)p->val.mref.lpmref", // Correct deletion
		}, nil)

	// 2. xll_converters.cpp (AnyToXLOPER12 leaks and missing features)
	checkContent(t, filepath.Join("generated", "cpp", "include", "xll_converters.cpp"),
		[]string{
			"case ipc::types::AnyValue_Range:", // Missing feature fixed
		},
		[]string{
			"x->xltype = xltypeInt;",  // Missing xlbitDLLFree
			"x->xltype = xltypeNum;",
			"x->xltype = xltypeBool;",
			"x->xltype = xltypeErr;",
			"x->xltype = xltypeSRef;", // RangeToXLOPER12 leak
		})

	// 3. xll_async.cpp (Range leak in manual cleanup)
	checkContent(t, filepath.Join("generated", "cpp", "include", "xll_async.cpp"),
		[]string{
			"v.xltype & xltypeRef", // Cleanup loop handled
		}, nil)
}

// TestRepro_NestedIPC_Corruption verifies that nested IPC calls do not corrupt the zero-copy slot.
func TestRepro_NestedIPC_Corruption(t *testing.T) {
	tempDir, cleanup := setupGenTest(t, "repro_project")
	defer cleanup()

	pathCleanup := setupMockFlatc(t, tempDir)
	defer pathCleanup()

	if err := runGenerate(); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Verify ConvertAny does not use GetZeroCopySlot
	// We read the file manually to scope the check to ConvertAny function if possible,
	// but simple global check is likely sufficient given the context.
	checkContent(t, filepath.Join("generated", "cpp", "include", "xll_converters.cpp"),
		[]string{
			"g_host.Send(", // Must use Send
		},
		[]string{
			// "g_host.GetZeroCopySlot()", // This might appear in OTHER functions, so we can't globally ban it.
			// We need to check specifically in ConvertAny.
		})

	// Specific check for ConvertAny body
	content, _ := os.ReadFile(filepath.Join("generated", "cpp", "include", "xll_converters.cpp"))
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
	// Custom setup because we need specific xll.yaml
	tempDir, err := os.MkdirTemp("", "xll-gen-string-repro")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	pathCleanup := setupMockFlatc(t, tempDir)
	defer pathCleanup()

	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)
	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}

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
	if err := os.WriteFile("xll.yaml", []byte(xllContent), 0644); err != nil {
		t.Fatal(err)
	}
	// Dummy go.mod
	if err := os.WriteFile("go.mod", []byte("module repro-string-arg\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := runGenerate(); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	checkContent(t, filepath.Join("generated", "cpp", "xll_main.cpp"),
		[]string{
			"ConvertExcelString",
		},
		[]string{
			"WStringToString(msg)",
		})

	checkContent(t, filepath.Join("generated", "cpp", "include", "xll_utility.cpp"),
		[]string{
			"const char* ConvertExcelString(const wchar_t* wstr)",
		}, nil)
}

// TestRegression runs an end-to-end regression test.
func TestRegression(t *testing.T) {
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

	origWd, _ := os.Getwd()
	repoRoot := origWd
	if filepath.Base(repoRoot) == "cmd" {
		repoRoot = filepath.Dir(repoRoot)
	}
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

	editCmd := exec.Command("go", "mod", "edit", "-replace", "github.com/xll-gen/xll-gen="+repoRoot)
	if out, err := editCmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod edit replace failed: %v\nOutput: %s", err, out)
	}

	if err := os.WriteFile("xll.yaml", []byte(regtest.XllYaml), 0644); err != nil {
		t.Fatal(err)
	}

	if err := runGenerate(); err != nil {
		t.Fatalf("runGenerate failed: %v", err)
	}

	if err := os.WriteFile("main.go", []byte(regtest.ServerGo), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "mod", "tidy")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy failed: %v\nOutput: %s", err, out)
	}

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
