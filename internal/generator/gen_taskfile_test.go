package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// TestGenerateTaskfile_ReleaseBuildsGoServer guards against the regression where
// the Release `build-cpp` task built the C++ XLL but NOT the Go server exe (only
// the Debug variant did), so `xll-gen build` produced an XLL with no server to
// launch. The Release build must build the server in BOTH modes:
//   - standalone (singlefile ""): via `task: build-go` (-> <name>.exe)
//   - singlefile "xll":           via the build-go-server dep (-> go_server.exe,
//     which the C++ embed step compresses into the XLL)
func TestGenerateTaskfile_ReleaseBuildsGoServer(t *testing.T) {
	cases := []struct {
		name       string
		singlefile string
		wantInRel  string
	}{
		{"standalone", "", "task: build-go"},
		{"singlefile", "xll", "build-go-server"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := &config.Config{
				Project: config.ProjectConfig{Name: "TestProj", Version: "0.1.0"},
				Build:   config.BuildConfig{Singlefile: c.singlefile},
			}
			if err := generateTaskfile(cfg, dir); err != nil {
				t.Fatalf("generateTaskfile: %v", err)
			}
			b, err := os.ReadFile(filepath.Join(dir, "Taskfile.yml"))
			if err != nil {
				t.Fatalf("read Taskfile: %v", err)
			}
			content := string(b)

			// Isolate the Release `build-cpp:` block (up to `build-cpp-debug:`)
			// so we assert the RELEASE path builds the server, not the debug one.
			rel := content
			if i := strings.Index(content, "build-cpp:"); i >= 0 {
				rest := content[i:]
				if j := strings.Index(rest, "build-cpp-debug:"); j >= 0 {
					rel = rest[:j]
				}
			}
			if !strings.Contains(rel, c.wantInRel) {
				t.Fatalf("singlefile=%q: Release build-cpp must build the Go server (want %q in the build-cpp block); got:\n%s",
					c.singlefile, c.wantInRel, rel)
			}
		})
	}
}
