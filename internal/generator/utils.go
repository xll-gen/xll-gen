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

// parseDurationToMs parses a duration string (e.g. "2s", "500ms") and returns milliseconds as int.
// Note: This is also defined in funcmap.go, but needed here for utility usage if any.
// To avoid redeclaration errors, we should check if it's used elsewhere or remove one.
// The error log showed: "parseDurationToMs redeclared in this block".
// So we should remove it from here if it is in funcmap.go and accessible, OR rename it, OR remove it from funcmap.go.
// Since funcmap.go is in the same package, we only need it once.
// I will NOT include parseDurationToMs here to avoid the redeclaration error seen earlier.
