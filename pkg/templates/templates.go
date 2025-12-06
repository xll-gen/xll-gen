package templates

import (
	"embed"
	"fmt"
)

//go:embed *.tmpl
var templatesFS embed.FS

// Get returns the content of the specified template file.
func Get(name string) (string, error) {
	content, err := templatesFS.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("template %s not found: %w", name, err)
	}
	return string(content), nil
}
