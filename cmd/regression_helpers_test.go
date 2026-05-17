package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/generator"
	"gopkg.in/yaml.v3"
)

// --- Helpers ---

var (
	fakeFlatcOnce sync.Once
	fakeFlatcPath string
	fakeFlatcErr  error
)

// setupMockFlatc returns the path to a real PE that stands in for the flatc
// compiler. The stub is built once per `go test` invocation from
// cmd/testdata/fakeflatc/main.go and cached in the user cache dir.
//
// Cross-platform notes: the previous implementation wrote a /bin/sh or batch
// script to a file named "flatc.exe", which Windows refused to load (it
// validates the PE header before falling back to shell interpretation).
// Building a real PE via `go build` works on every supported OS; the trade
// is a one-time ~500ms compile per test process. The returned path can be
// passed straight to generator.Options{FlatcPath: ...}.
//
// tempDir is accepted for API compatibility with the prior signature but
// is no longer used — the stub is shared, so per-test cleanup is a no-op.
func setupMockFlatc(t *testing.T, tempDir string) (string, func()) {
	t.Helper()
	_ = tempDir
	fakeFlatcOnce.Do(buildFakeFlatc)
	if fakeFlatcErr != nil {
		t.Fatalf("setupMockFlatc: %v", fakeFlatcErr)
	}
	return fakeFlatcPath, func() {}
}

func buildFakeFlatc() {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		fakeFlatcErr = fmt.Errorf("user cache dir: %w", err)
		return
	}
	binDir := filepath.Join(cacheDir, "xll-gen", "test-fake-flatc")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		fakeFlatcErr = fmt.Errorf("mkdir cache: %w", err)
		return
	}
	binName := "fake-flatc"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(binDir, binName)

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		fakeFlatcErr = fmt.Errorf("runtime.Caller failed")
		return
	}
	srcDir := filepath.Join(filepath.Dir(thisFile), "testdata", "fakeflatc")

	cmd := exec.Command("go", "build", "-o", binPath, srcDir)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		fakeFlatcErr = fmt.Errorf("build fake flatc: %w\n%s", err, out)
		return
	}
	fakeFlatcPath = binPath
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
