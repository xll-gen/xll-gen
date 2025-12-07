package generator

import (
	"os"
	"path/filepath"
	"text/template"

	"xll-gen/internal/config"
	"xll-gen/internal/templates"
	"xll-gen/version"
)

func generateTaskfile(cfg *config.Config, dir string) error {
	tmplContent, err := templates.Get("Taskfile.yml.tmpl")
	if err != nil {
		return err
	}

	t, err := template.New("taskfile").Parse(tmplContent)
	if err != nil {
		return err
	}

	f, err := os.Create(filepath.Join(dir, "Taskfile.yml"))
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, struct {
		ProjectName string
		Version     string
	}{
		ProjectName: cfg.Project.Name,
		Version:     version.Version,
	})
}
