package generator

import (
	"path/filepath"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/version"
)

// generateTaskfile creates a Taskfile.yml for the project.
// It uses the project name and version from the configuration to populate the build tasks.
//
// Parameters:
//   - cfg: The project configuration.
//   - dir: The directory where the file should be generated.
//
// Returns:
//   - error: An error if the file creation or template execution fails.
func generateTaskfile(cfg *config.Config, dir string) error {
	data := struct {
		ProjectName string
		Version     string
		Build       config.BuildConfig
	}{
		ProjectName: cfg.Project.Name,
		Version:     version.Version,
		Build:       cfg.Build,
	}

	return executeTemplate("Taskfile.yml.tmpl", filepath.Join(dir, "Taskfile.yml"), data, nil)
}
