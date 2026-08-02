package regtest

import (
	"os"
	"path/filepath"
	"text/template"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/templates"
)

// generateSimMain generates the C++ main file for the simulation host.
func generateSimMain(cfg *config.Config, dir string) error {
	tmplContent, err := templates.Get("regtest_main.cpp.tmpl")
	if err != nil {
		return err
	}

	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
	}

	t, err := template.New("regtest_main").Funcs(funcMap).Parse(tmplContent)
	if err != nil {
		return err
	}

	f, err := os.Create(filepath.Join(dir, "main.cpp"))
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, cfg)
}

// generateSimCMake writes the CMakeLists.txt for the simulation host.
//
// Copied VERBATIM, not rendered: regtest_CMakeLists.txt.tmpl contains no
// template actions at all. Running a static file through text/template bought
// nothing and carried a trap — CMake generator expressions and any future
// `{{` in that file would have been parsed as actions, failing or, worse,
// rendering to empty. It keeps the .tmpl extension so the embed pattern and the
// "everything the generator emits lives in internal/templates" convention hold.
func generateSimCMake(cfg *config.Config, dir string) error {
	_ = cfg // static content; kept in the signature for symmetry with its siblings
	content, err := templates.Get("regtest_CMakeLists.txt.tmpl")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "CMakeLists.txt"), []byte(content), 0o644)
}
