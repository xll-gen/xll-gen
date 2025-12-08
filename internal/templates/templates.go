package templates

import (
	"embed"
	"fmt"
)

// templatesFS embeds all .tmpl files in the current directory.
//
//go:embed *.tmpl
var templatesFS embed.FS

// Get returns the content of the specified template file.
//
// Parameters:
//   - name: The name of the template file (e.g., "xll.yaml.tmpl").
//
// Returns:
//   - string: The content of the template file.
//   - error: An error if the file is not found or cannot be read.
func Get(name string) (string, error) {
	content, err := templatesFS.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("template %s not found: %w", name, err)
	}
	return string(content), nil
}
