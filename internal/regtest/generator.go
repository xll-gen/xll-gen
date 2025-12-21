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

// generateSimCMake generates the CMakeLists.txt for the simulation host.
func generateSimCMake(cfg *config.Config, dir string) error {
	tmplContent, err := templates.Get("regtest_CMakeLists.txt.tmpl")
	if err != nil {
		return err
	}

	t, err := template.New("regtest_cmake").Parse(tmplContent)
	if err != nil {
		return err
	}

	f, err := os.Create(filepath.Join(dir, "CMakeLists.txt"))
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, cfg)
}
