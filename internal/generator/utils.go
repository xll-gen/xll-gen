package generator

import (
	"fmt"
	"os"
	"text/template"

	"github.com/xll-gen/xll-gen/internal/templates"
)

// executeTemplate parses a template from the templates package and writes it to a file.
func executeTemplate(tmplName string, destPath string, data interface{}, funcMap template.FuncMap) error {
	tmplContent, err := templates.Get(tmplName)
	if err != nil {
		return fmt.Errorf("failed to get template %s: %w", tmplName, err)
	}

	tmpl := template.New(tmplName)
	if funcMap != nil {
		tmpl = tmpl.Funcs(funcMap)
	}

	parsedTmpl, err := tmpl.Parse(string(tmplContent))
	if err != nil {
		return fmt.Errorf("failed to parse template %s: %w", tmplName, err)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", destPath, err)
	}
	defer f.Close()

	if err := parsedTmpl.Execute(f, data); err != nil {
		return fmt.Errorf("failed to execute template %s: %w", tmplName, err)
	}

	return nil
}
