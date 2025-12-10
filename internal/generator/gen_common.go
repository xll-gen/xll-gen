package generator

import (
	"path/filepath"

	"xll-gen/internal/config"
	"xll-gen/version"
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
		Embed       config.EmbedConfig
	}{
		ProjectName: cfg.Project.Name,
		Version:     version.Version,
		Embed:       cfg.Build.Embed,
	}

	return executeTemplate("Taskfile.yml.tmpl", filepath.Join(dir, "Taskfile.yml"), data, nil)
}
