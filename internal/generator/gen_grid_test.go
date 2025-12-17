package generator

import (
	"os"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

func TestGenGrid(t *testing.T) {
	t.Parallel()
	// Create a temp dir
	tmpDir, err := os.MkdirTemp("", "repro_grid")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a config with grid and numgrid
	cfg := &config.Config{
		Project: config.ProjectConfig{
			Name:    "TestProject",
			Version: "0.1.0",
		},
		Gen: config.GenConfig{
			Go: config.GoConfig{
				Module: "testmod",
			},
		},
		Server: config.ServerConfig{
			Launch: &config.LaunchConfig{Enabled: new(bool)},
		},
		Build: config.BuildConfig{
			Singlefile: "xll",
			TempDir:    "temp_%PROJECT%",
		},
		Functions: []config.Function{
			{
				Name:        "GridFunc",
				Description: "Tests grid",
				Args: []config.Arg{
					{Name: "g", Type: "grid"},
				},
				Return: "string",
			},
			{
				Name:        "NumGridFunc",
				Description: "Tests numgrid",
				Args: []config.Arg{
					{Name: "ng", Type: "numgrid"},
				},
				Return: "float",
			},
		},
	}

	// Set default launch enabled
	*cfg.Server.Launch.Enabled = true

	// Run Generate
	err = Generate(cfg, tmpDir, "testmod", Options{})

	if err != nil {
		t.Fatalf("Generate failed for grid/numgrid type: %v", err)
	}
}
