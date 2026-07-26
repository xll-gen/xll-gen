package generator

import (
	"bytes"
	"fmt"
	"os"
	"text/template"

	"github.com/xll-gen/xll-gen/internal/templates"
)

// executeTemplate parses a template from the templates package and writes it to a file.
//
// Rendering goes to a buffer first, and the destination is only touched once the
// whole document exists. os.Create truncates on open, so rendering straight into
// the file means any mid-render failure — a disk-full or AV-lock write error, or
// a template action that errors after output has already been emitted — replaces
// a previously valid xll_main.cpp with a half-written one. The next `task build`
// then fails on a syntax error deep inside generated code, which reads as a
// codegen bug rather than "the last generate did not finish".
//
// Output bytes are unchanged; only the failure mode is.
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

	var buf bytes.Buffer
	if err := parsedTmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute template %s: %w", tmplName, err)
	}

	if err := os.WriteFile(destPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to create file %s: %w", destPath, err)
	}

	return nil
}
