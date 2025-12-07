package generator

import (
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"xll-gen/internal/config"
	"xll-gen/internal/templates"
	"xll-gen/version"
)

func generateInterface(cfg *config.Config, dir string, modName string) error {
	tmplContent, err := templates.Get("interface.go.tmpl")
	if err != nil {
		return err
	}

	funcMap := template.FuncMap{
		"lookupGoType": func(t string) string {
			m := map[string]string{
				"int":     "int32",
				"float":   "float64",
				"string":  "string",
				"bool":    "bool",
				"range":   "*types.Range",
				"any":     "*types.Any",
				"grid":    "*types.Grid",
				"numgrid": "*types.NumGrid",
				"int?":    "*int32",
				"float?":  "*float64",
				"bool?":   "*bool",
				"string?": "*string",
			}
			if v, ok := m[t]; ok {
				return v
			}
			return t
		},
	}

	t, err := template.New("interface").Funcs(funcMap).Parse(tmplContent)
	if err != nil {
		return err
	}

	pkg := cfg.Gen.Go.Package
	if pkg == "" {
		pkg = "generated"
	}

	data := struct {
		Package   string
		ModName   string
		Functions []config.Function
		Events    []config.Event
		Version   string
	}{
		Package:   pkg,
		ModName:   modName,
		Functions: cfg.Functions,
		Events:    cfg.Events,
		Version:   version.Version,
	}

	f, err := os.Create(filepath.Join(dir, "interface.go"))
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, data)
}

func generateServer(cfg *config.Config, dir string, modName string) error {
	tmplContent, err := templates.Get("server.go.tmpl")
	if err != nil {
		return err
	}

	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"capitalize": func(s string) string {
			if len(s) == 0 {
				return ""
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		"lookupGoType": func(t string) string {
			m := map[string]string{
				"int":     "int32",
				"float":   "float64",
				"string":  "string",
				"bool":    "bool",
				"range":   "*types.Range",
				"any":     "*types.Any",
				"grid":    "*types.Grid",
				"numgrid": "*types.NumGrid",
				"int?":    "*int32",
				"float?":  "*float64",
				"bool?":   "*bool",
				"string?": "*string",
			}
			if v, ok := m[t]; ok {
				return v
			}
			return t
		},
		"lookupEventId": func(evtType string) int {
			// Returns offset from User Start
			if evtType == "CalculationEnded" {
				return 1
			}
			if evtType == "CalculationCanceled" {
				return 2
			}
			return 0
		},
		"hasEvent": func(name string, events []config.Event) bool {
			for _, e := range events {
				if e.Type == name {
					return true
				}
			}
			return false
		},
	}

	t, err := template.New("server").Funcs(funcMap).Parse(tmplContent)
	if err != nil {
		return err
	}

	pkg := cfg.Gen.Go.Package
	if pkg == "" {
		pkg = "generated"
	}

	data := struct {
		Package       string
		ModName       string
		ProjectName   string
		Functions     []config.Function
		Events        []config.Event
		ServerTimeout string
		ServerWorkers int
		Version       string
	}{
		Package:       pkg,
		ModName:       modName,
		ProjectName:   cfg.Project.Name,
		Functions:     cfg.Functions,
		Events:        cfg.Events,
		ServerTimeout: cfg.Server.Timeout,
		ServerWorkers: cfg.Server.Workers,
		Version:       version.Version,
	}

	f, err := os.Create(filepath.Join(dir, "server.go"))
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, data)
}
