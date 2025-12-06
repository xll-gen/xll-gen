package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	"xll-gen/cmd/templates"
)

var disablePidSuffix bool

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate Go and C++ code from xll.yaml",
	Run: func(cmd *cobra.Command, args []string) {
		if err := runGenerate(); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	generateCmd.Flags().BoolVar(&disablePidSuffix, "no-pid-suffix", false, "Disable appending PID to SHM name")
	rootCmd.AddCommand(generateCmd)
}

type Config struct {
	Project   ProjectConfig `yaml:"project"`
	Gen       GenConfig     `yaml:"gen"`
	Server    ServerConfig  `yaml:"server"`
	Functions []Function    `yaml:"functions"`
	Events    []Event       `yaml:"events"`
}

type Event struct {
	Type        string `yaml:"type"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type ServerConfig struct {
	Timeout string `yaml:"timeout"`
	Workers int    `yaml:"workers"`
}

type ProjectConfig struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

type GenConfig struct {
	Go GoConfig      `yaml:"go"`
	DisablePidSuffix bool `yaml:"disable_pid_suffix"`
}

type GoConfig struct {
	Package string `yaml:"package"`
}

type Function struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Args        []Arg  `yaml:"args"`
	Return      string `yaml:"return"`
	Volatile    bool   `yaml:"volatile"`
	Async       bool   `yaml:"async"`
	Category    string `yaml:"category"`
	Shortcut    string `yaml:"shortcut"`
	HelpTopic   string `yaml:"help_topic"`
	Timeout     string `yaml:"timeout"`
	Caller      bool   `yaml:"caller"`
}

type Arg struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"`
	Description string `yaml:"description"`
}

func runGenerate() error {
	// 1. Read xll.yaml
	data, err := os.ReadFile("xll.yaml")
	if err != nil {
		return fmt.Errorf("failed to read xll.yaml: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("failed to parse xll.yaml: %w", err)
	}

	if err := validateConfig(config); err != nil {
		return err
	}

	fmt.Printf("Generating code for project: %s\n", config.Project.Name)

	// 2. Ensure directories
	genDir := "generated"
	cppDir := filepath.Join(genDir, "cpp")
	if err := os.MkdirAll(cppDir, 0755); err != nil {
		return err
	}

	// 2.1 Write Assets (C++ common files)
	includeDir := filepath.Join(cppDir, "include")
	if err := os.MkdirAll(includeDir, 0755); err != nil {
		return err
	}
	for name, content := range assetsMap {
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
	if err := generateSchema(config, schemaPath); err != nil {
		return err
	}
	fmt.Println("Generated schema.fbs")

	// 4. Get Module Name (Needed for Flatbuffers imports)
	modName, err := getModuleName()
	if err != nil {
		return err
	}
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
	if err := generateInterface(config, genDir, modName); err != nil {
		return err
	}
	fmt.Println("Generated interface.go")

	// 7. Generate server.go
	if err := generateServer(config, genDir, modName); err != nil {
		return err
	}
	fmt.Println("Generated server.go")

	// 8. Generate xll_main.cpp
	shouldAppendPid := !config.Gen.DisablePidSuffix && !disablePidSuffix
	if err := generateCppMain(config, cppDir, shouldAppendPid); err != nil {
		return err
	}
	fmt.Println("Generated xll_main.cpp")

	// 9. Generate CMakeLists.txt
	if err := generateCMake(config, cppDir); err != nil {
		return err
	}
	fmt.Println("Generated CMakeLists.txt")

	// 10. Generate Taskfile.yml
	if err := generateTaskfile(config, "."); err != nil {
		return err
	}
	fmt.Println("Generated Taskfile.yml")

	fmt.Println("Done. Please run 'go mod tidy' to ensure dependencies are installed.")

	return nil
}

func getModuleName() (string, error) {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		return "", fmt.Errorf("failed to read go.mod: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	return "", fmt.Errorf("module name not found in go.mod")
}

func validateConfig(config Config) error {
	seenEvents := make(map[string]bool)
	for _, evt := range config.Events {
		if seenEvents[evt.Type] {
			return fmt.Errorf("duplicate event type: %s", evt.Type)
		}
		seenEvents[evt.Type] = true
	}
	return nil
}

// ----------------------------------------------------------------------------
// Generators
// ----------------------------------------------------------------------------

func generateSchema(config Config, path string) error {
	tmplStr, err := templates.Get("schema.fbs.tmpl")
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

	t, err := template.New("schema").Funcs(funcMap).Parse(tmplStr)
	if err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, config)
}

func generateXlTypes(path string) error {
	content, err := templates.Get("xltypes.fbs.tmpl")
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0644)
}

func generateInterface(config Config, dir string, modName string) error {
	tmplStr, err := templates.Get("interface.go.tmpl")
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

	t, err := template.New("interface").Funcs(funcMap).Parse(tmplStr)
	if err != nil {
		return err
	}

	pkg := config.Gen.Go.Package
	if pkg == "" {
		pkg = "generated"
	}

	data := struct {
		Package   string
		ModName   string
		Functions []Function
		Events    []Event
	}{
		Package:   pkg,
		ModName:   modName,
		Functions: config.Functions,
		Events:    config.Events,
	}

	f, err := os.Create(filepath.Join(dir, "interface.go"))
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, data)
}

func generateServer(config Config, dir string, modName string) error {
	tmplStr, err := templates.Get("server.go.tmpl")
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
		"hasEvent": func(name string, events []Event) bool {
			for _, e := range events {
				if e.Type == name {
					return true
				}
			}
			return false
		},
	}

	t, err := template.New("server").Funcs(funcMap).Parse(tmplStr)
	if err != nil {
		return err
	}

	pkg := config.Gen.Go.Package
	if pkg == "" {
		pkg = "generated"
	}

	data := struct {
		Package       string
		ModName       string
		ProjectName   string
		Functions     []Function
		Events        []Event
		ServerTimeout string
		ServerWorkers int
	}{
		Package:       pkg,
		ModName:       modName,
		ProjectName:   config.Project.Name,
		Functions:     config.Functions,
		Events:        config.Events,
		ServerTimeout: config.Server.Timeout,
		ServerWorkers: config.Server.Workers,
	}

	f, err := os.Create(filepath.Join(dir, "server.go"))
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, data)
}

func generateCppMain(config Config, dir string, shouldAppendPid bool) error {
	tmplStr, err := templates.Get("xll_main.cpp.tmpl")
	if err != nil {
		return err
	}

	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		"registerCount": func(f Function) int {
			c := 10 + len(f.Args)
			if f.Async {
				c++
			}
			return c
		},
		"joinArgNames": func(f Function) string {
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
			if evtType == "CalculationEnded" { return 1 }
			if evtType == "CalculationCanceled" { return 2 }
			return 0
		},
		"lookupEventCode": func(evtType string) string {
			if evtType == "CalculationEnded" { return "xleventCalculationEnded"; }
			if evtType == "CalculationCanceled" { return "xleventCalculationCanceled"; }
			return "0";
		},
		"hasEvent": func(name string, events []Event) bool {
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

	t, err := template.New("cpp").Funcs(funcMap).Parse(tmplStr)
	if err != nil { return err }

	f, err := os.Create(filepath.Join(dir, "xll_main.cpp"))
	if err != nil { return err }
	defer f.Close()

	return t.Execute(f, struct {
		ProjectName     string
		Functions       []Function
		Events          []Event
		ShouldAppendPid bool
	}{
		ProjectName:     config.Project.Name,
		Functions:       config.Functions,
		Events:          config.Events,
		ShouldAppendPid: shouldAppendPid,
	})
}

func generateCMake(config Config, dir string) error {
	tmplStr, err := templates.Get("CMakeLists.txt.tmpl")
	if err != nil {
		return err
	}

	t, err := template.New("cmake").Parse(tmplStr)
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
	}{
		ProjectName: config.Project.Name,
	})
}

func generateTaskfile(config Config, dir string) error {
	tmplStr, err := templates.Get("Taskfile.yml.tmpl")
	if err != nil {
		return err
	}
	t, err := template.New("taskfile").Parse(tmplStr)
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
	}{
		ProjectName: config.Project.Name,
	})
}
