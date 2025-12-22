package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"github.com/xll-gen/xll-gen/internal/config"
)

func TestGenCpp_NoSafeBlocks(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "safe_block_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{
				Name:   "TestFunc",
				Return: "int",
				Args:   []config.Arg{{Name: "a", Type: "int"}},
			},
		},
        Server: config.ServerConfig{
            Launch: &config.LaunchConfig{Enabled: new(bool)},
        },
	}
    *cfg.Server.Launch.Enabled = true

	if err := generateCppMain(cfg, tmpDir, false); err != nil {
		t.Fatalf("generateCppMain failed: %v", err)
	}

	contentBytes, err := os.ReadFile(filepath.Join(tmpDir, "xll_main.cpp"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(contentBytes)

	if strings.Contains(content, "XLL_SAFE_BLOCK_BEGIN") {
		t.Fatal("Found XLL_SAFE_BLOCK_BEGIN in generated xll_main.cpp")
	}
	if strings.Contains(content, "XLL_SAFE_BLOCK_END") {
		t.Fatal("Found XLL_SAFE_BLOCK_END in generated xll_main.cpp")
	}
}
