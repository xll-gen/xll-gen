package generator

import (
	"os"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/templates"
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
	return executeTemplate("schema.fbs.tmpl", path, cfg, GetCommonFuncMap())
}

// generateProtocol writes the static protocol.fbs file.
// This file contains standard Excel type definitions and system messages.
//
// Parameters:
//   - path: The file path where protocol.fbs should be written.
//
// Returns:
//   - error: An error if the write fails.
func generateProtocol(path string) error {
	// Note: protocol.fbs is stored as a static file, not a .tmpl
	content, err := templates.Get("protocol.fbs")
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0644)
}
