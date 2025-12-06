package generator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"xll-gen/internal/assets"
	"xll-gen/internal/config"
	"xll-gen/internal/templates"
	"xll-gen/version"
)

type Options struct {
	DisablePidSuffix bool
}

func Generate(cfg *config.Config, modName string, opts Options) error {
	fmt.Printf("Generating code for project: %s\n", cfg.Project.Name)

	// 1. Ensure directories
	genDir := "generated"
	cppDir := filepath.Join(genDir, "cpp")
	if err := os.MkdirAll(cppDir, 0755); err != nil {
		return err
	}

	// 2. Write Assets (C++ common files)
	includeDir := filepath.Join(cppDir, "include")
	if err := os.MkdirAll(includeDir, 0755); err != nil {
		return err
	}
	for name, content := range assets.AssetsMap {
		if err := os.WriteFile(filepath.Join(includeDir, name), []byte(content), 0644); err != nil {
			return err
		}
	}

	// 3. Generate xltypes.fbs
	xlTypesPath := filepath.Join(genDir, "xltypes.fbs")
	if err := generateXlTypes(xlTypesPath); err != nil {
		return err
	}
	fmt.Println("Generated xltypes.fbs")

	// 4. Generate schema.fbs
	schemaPath := filepath.Join(genDir, "schema.fbs")
	if err := generateSchema(cfg, schemaPath); err != nil {
		return err
	}
	fmt.Println("Generated schema.fbs")

	goModulePath := modName + "/generated"

	// 5. Run flatc
	flatcPath, err := EnsureFlatc()
	if err != nil {
		return err
	}

	// Generate Go code for xltypes
	cmd := exec.Command(flatcPath, "--go", "--go-module-name", goModulePath, "-o", genDir, xlTypesPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("flatc (go xltypes) failed: %w", err)
	}

	// Generate C++ code for xltypes
	cmd = exec.Command(flatcPath, "--cpp", "-o", cppDir, xlTypesPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("flatc (cpp xltypes) failed: %w", err)
	}

	// Generate Go code for schema
	cmd = exec.Command(flatcPath, "--go", "--go-namespace", "ipc", "--go-module-name", goModulePath, "-o", genDir, schemaPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("flatc (go) failed: %w", err)
	}
	fmt.Println("Generated Flatbuffers Go code")

	// Generate C++ code
	cmd = exec.Command(flatcPath, "--cpp", "-o", cppDir, schemaPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("flatc (cpp) failed: %w", err)
	}
	fmt.Println("Generated Flatbuffers C++ code")

	// 6. Generate interface.go
	if err := generateInterface(cfg, genDir, modName); err != nil {
		return err
	}
	fmt.Println("Generated interface.go")

	// 7. Generate server.go
	if err := generateServer(cfg, genDir, modName); err != nil {
		return err
	}
	fmt.Println("Generated server.go")

	// 8. Generate xll_main.cpp
	shouldAppendPid := !cfg.Gen.DisablePidSuffix && !opts.DisablePidSuffix
	if err := generateCppMain(cfg, cppDir, shouldAppendPid); err != nil {
		return err
	}
	fmt.Println("Generated xll_main.cpp")

	// 9. Generate CMakeLists.txt
	if err := generateCMake(cfg, cppDir); err != nil {
		return err
	}
	fmt.Println("Generated CMakeLists.txt")

	// 10. Generate Taskfile.yml
	if err := generateTaskfile(cfg, "."); err != nil {
		return err
	}
	fmt.Println("Generated Taskfile.yml")

	fmt.Println("Done. Please run 'go mod tidy' to ensure dependencies are installed.")

	return nil
}

// ----------------------------------------------------------------------------
// Generators
// ----------------------------------------------------------------------------

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
				"any":     "ipc.types.Any",
				"int?":    "ipc.types.Int",
				"float?":  "ipc.types.Num",
				"bool?":   "ipc.types.Bool",
				"string?": "ipc.types.Str",
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

func generateXlTypes(path string) error {
	content, err := templates.Get("xltypes.fbs.tmpl")
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0644)
}

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
				"string?": "LPXLOPER12",
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
				"string?": "Q",
			}
			if v, ok := m[t]; ok { return v }
			return t
		},
		"lookupEventId": func(evtType string) int {
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
		ShouldAppendPid bool
		Version         string
	}{
		ProjectName:     cfg.Project.Name,
		Functions:       cfg.Functions,
		Events:          cfg.Events,
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

func generateTaskfile(cfg *config.Config, dir string) error {
	tmplContent, err := templates.Get("Taskfile.yml.tmpl")
	if err != nil {
		return err
	}

	t, err := template.New("taskfile").Parse(tmplContent)
	if err != nil {
		return err
	}

	f, err := os.Create(filepath.Join(dir, "Taskfile.yml"))
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
