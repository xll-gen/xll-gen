package generator

import (
	"os"
	"text/template"

	"xll-gen/internal/config"
	"xll-gen/internal/templates"
)

// generateSchema generates the FlatBuffers schema file (schema.fbs).
// It maps the functions and types defined in xll.yaml to FlatBuffers tables and unions.
//
// Parameters:
//   - cfg: The project configuration.
//   - path: The file path where schema.fbs should be written.
//
// Returns:
//   - error: An error if generation fails.
func generateSchema(cfg *config.Config, path string) error {
	tmplContent, err := templates.Get("schema.fbs.tmpl")
	if err != nil {
		return err
	}

	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"boolToInt": func(b bool) int {
			if b {
				return 1
			}
			return 0
		},
		"lookupSchemaType": func(t string) string {
			m := map[string]string{
				"int":     "int",
				"float":   "double",
				"string":  "string",
				"bool":    "bool",
				"range":   "ipc.types.Range",
				"grid":    "ipc.types.Grid",
				"numgrid": "ipc.types.NumGrid",
				"any":     "ipc.types.Any",
				"int?":    "ipc.types.Int",
				"float?":  "ipc.types.Num",
				"bool?":   "ipc.types.Bool",
			}
			if v, ok := m[t]; ok {
				return v
			}
			return t
		},
	}

	t, err := template.New("schema").Funcs(funcMap).Parse(tmplContent)
	if err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, cfg)
}

// generateXlTypes writes the static xltypes.fbs file.
// This file contains standard Excel type definitions used by the schema.
//
// Parameters:
//   - path: The file path where xltypes.fbs should be written.
//
// Returns:
//   - error: An error if the write fails.
func generateXlTypes(path string) error {
	content, err := templates.Get("xltypes.fbs.tmpl")
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0644)
}
