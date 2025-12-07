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

func generateCppMain(cfg *config.Config, dir string, shouldAppendPid bool) error {
	tmplContent, err := templates.Get("xll_main.cpp.tmpl")
	if err != nil {
		return err
	}

	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		"registerCount": func(f config.Function) int {
			c := 10 + len(f.Args)
			if f.Async {
				c++
			}
			return c
		},
		"joinArgNames": func(f config.Function) string {
			var names []string
			for _, a := range f.Args {
				names = append(names, a.Name)
			}
			if f.Async {
				names = append(names, "asyncHandle")
			}
			return strings.Join(names, ",")
		},
		"withDefault": func(val, def string) string {
			if val == "" {
				return def
			}
			return val
		},
		"lookupCppType": func(t string) string {
			m := map[string]string{
				"int":     "int32_t",
				"float":   "double",
				"string":  "LPXLOPER12",
				"bool":    "short",
				"range":   "LPXLOPER12",
				"any":     "LPXLOPER12",
			}
			if v, ok := m[t]; ok { return v }
			return t
		},
		"lookupArgCppType": func(t string) string {
			m := map[string]string{
				"int":     "int32_t",
				"float":   "double",
				"string":  "const wchar_t*",
				"bool":    "short",
				"range":   "LPXLOPER12",
				"any":     "LPXLOPER12",
				"int?":    "int32_t*",
				"float?":  "double*",
				"bool?":   "short*",
				"string?": "const wchar_t*",
			}
			if v, ok := m[t]; ok { return v }
			return t
		},
		"lookupXllType": func(t string) string {
			m := map[string]string{
				"int":     "J",
				"float":   "B",
				"string":  "Q",
				"bool":    "A",
				"range":   "U",
				"any":     "U",
			}
			if v, ok := m[t]; ok { return v }
			return t
		},
		"lookupArgXllType": func(t string) string {
			m := map[string]string{
				"int":     "J",
				"float":   "B",
				"string":  "D%",
				"bool":    "A",
				"range":   "U",
				"any":     "U",
				"int?":    "N",
				"float?":  "E",
				"bool?":   "L",
				"string?": "D%",
			}
			if v, ok := m[t]; ok { return v }
			return t
		},
		"lookupEventId": func(evtType string) int {
			// Returns offset from User Start
			if evtType == "CalculationEnded" { return 1 }
			if evtType == "CalculationCanceled" { return 2 }
			return 0
		},
		"lookupEventCode": func(evtType string) string {
			if evtType == "CalculationEnded" { return "xleventCalculationEnded"; }
			if evtType == "CalculationCanceled" { return "xleventCalculationCanceled"; }
			return "0";
		},
		"hasEvent": func(name string, events []config.Event) bool {
			for _, e := range events {
				if e.Type == name {
					return true
				}
			}
			return false
		},
		"defaultErrorVal": func(t string) string {
			if t == "string" { return "NULL"; }
			return "0";
		},
		"derefBool": func(b *bool) bool {
			if b == nil { return false }
			return *b
		},
	}

	t, err := template.New("cpp").Funcs(funcMap).Parse(tmplContent)
	if err != nil {
		return err
	}

	f, err := os.Create(filepath.Join(dir, "xll_main.cpp"))
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, struct {
		ProjectName     string
		Functions       []config.Function
		Events          []config.Event
		Server          config.ServerConfig
		ShouldAppendPid bool
		Version         string
	}{
		ProjectName:     cfg.Project.Name,
		Functions:       cfg.Functions,
		Events:          cfg.Events,
		Server:          cfg.Server,
		ShouldAppendPid: shouldAppendPid,
		Version:         version.Version,
	})
}

func generateCMake(cfg *config.Config, dir string) error {
	tmplContent, err := templates.Get("CMakeLists.txt.tmpl")
	if err != nil {
		return err
	}

	t, err := template.New("cmake").Parse(tmplContent)
	if err != nil {
		return err
	}

	f, err := os.Create(filepath.Join(dir, "CMakeLists.txt"))
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, struct {
		ProjectName string
		Version     string
	}{
		ProjectName: cfg.Project.Name,
		Version:     version.Version,
	})
}
