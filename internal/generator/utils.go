package generator

import (
	"os"
	"strconv"
	"text/template"
	"time"

	"xll-gen/internal/templates"
)

// parseDurationToMs parses a duration string (e.g. "2s", "500ms") and returns milliseconds as int.
// Returns defaultVal if parsing fails or string is empty.
//
// Parameters:
//   - s: The duration string to parse.
//   - defaultVal: The value to return if parsing fails.
//
// Returns:
//   - int: The duration in milliseconds.
func parseDurationToMs(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}

	// Handle raw numbers as seconds (backward compatibility if needed, though Go usually uses strings)
	if _, err := strconv.Atoi(s); err == nil {
		// If it's just a number, assume seconds? Or ms?
		// Standard xll.yaml uses "10s", so pure number is ambiguous.
		// Let's assume input must be valid duration string.
	}

	d, err := time.ParseDuration(s)
	if err != nil {
		return defaultVal
	}
	return int(d.Milliseconds())
}

// executeTemplate loads a template, parses it with the provided funcMap, and executes it to the output path.
func executeTemplate(tmplName string, outputPath string, data interface{}, funcMap template.FuncMap) error {
	tmplContent, err := templates.Get(tmplName)
	if err != nil {
		return err
	}

	// If funcMap is nil, use empty map
	if funcMap == nil {
		funcMap = template.FuncMap{}
	}

	t, err := template.New(tmplName).Funcs(funcMap).Parse(tmplContent)
	if err != nil {
		return err
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, data)
}
