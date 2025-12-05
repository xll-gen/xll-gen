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

	// 4. Run flatc
	flatcPath, err := EnsureFlatc()
	if err != nil {
		return err
	}

	// Generate Go code
	cmd := exec.Command(flatcPath, "--go", "--go-namespace", "ipc", "-o", genDir, schemaPath)
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

	// 5. Get Module Name
	modName, err := getModuleName()
	if err != nil {
		return err
	}

	// 6. Generate interface.go
	if err := generateInterface(config, genDir); err != nil {
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

// ----------------------------------------------------------------------------
// Generators
// ----------------------------------------------------------------------------

func generateSchema(config Config, path string) error {
	tmpl := `include "xltypes.fbs";

namespace ipc;

{{range .Functions}}
table {{.Name}}Request {
  {{range $i, $arg := .Args}}{{$arg.Name}}:{{lookupSchemaType $arg.Type}} (id: {{$i}});
  {{end}}
  {{if .Async}}async_handle:ulong (id: {{len .Args}});{{end}}
}

table {{.Name}}Response {
  result:{{lookupSchemaType .Return}};
  error:string;
  {{if .Async}}async_handle:ulong;{{end}}
}
{{end}}
`
	funcMap := template.FuncMap{
		"lookupSchemaType": func(t string) string {
			m := map[string]string{
				"int":    "int",
				"float":  "double",
				"string": "string",
				"bool":   "bool",
			}
			if v, ok := m[t]; ok {
				return v
			}
			return t
		},
	}

	t, err := template.New("schema").Funcs(funcMap).Parse(tmpl)
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
	content := `namespace ipc.types;

enum XlError : short {
  Null = 2000,
  Div0 = 2007,
  Value = 2015,
  Ref = 2023,
  Name = 2029,
  Num = 2036,
  NA = 2042,
  GettingData = 2043,
  Spill = 2045,
  Connect = 2046,
  Blocked = 2047,
  Unknown = 2048,
  Field = 2049,
  Calc = 2050
}

table Bool { val: bool; }
table Num { val: double; }
table Int { val: int; }
table Str { val: string; }
table Err { val: XlError = Null; }
table AsyncHandle { val: ulong; }
table Nil { }

union ScalarValue { Bool, Num, Int, Str, Err, AsyncHandle, Nil }

table Scalar {
  val: ScalarValue;
}

table Array {
  rows: int;
  cols: int;
  data: [Scalar];
}

table NumArray {
  rows: int;
  cols: int;
  data: [double];
}

union AnyValue { Bool, Num, Int, Str, Err, AsyncHandle, Nil, Array, NumArray }

table Any {
  val: AnyValue;
}
`
	return os.WriteFile(path, []byte(content), 0644)
}

func generateInterface(config Config, dir string) error {
	tmpl := `package {{.Package}}

import "context"

type XllService interface {
{{range .Functions}}	{{.Name}}(ctx context.Context, {{range .Args}}{{.Name}} {{lookupGoType .Type}}, {{end}}) ({{lookupGoType .Return}}, error)
{{end}}}
`
	funcMap := template.FuncMap{
		"lookupGoType": func(t string) string {
			m := map[string]string{
				"int":    "int32",
				"float":  "float64",
				"string": "string",
				"bool":   "bool",
			}
			if v, ok := m[t]; ok {
				return v
			}
			return t
		},
	}

	t, err := template.New("interface").Funcs(funcMap).Parse(tmpl)
	if err != nil {
		return err
	}

	pkg := config.Gen.Go.Package
	if pkg == "" {
		pkg = "generated"
	}

	data := struct {
		Package   string
		Functions []Function
	}{
		Package:   pkg,
		Functions: config.Functions,
	}

	f, err := os.Create(filepath.Join(dir, "interface.go"))
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, data)
}

func generateServer(config Config, dir string, modName string) error {
	tmpl := `package {{.Package}}

import (
	"context"
	"fmt"
	"time"
	"{{.ModName}}/generated/ipc"
	"github.com/xll-gen/shm/go"
	flatbuffers "github.com/google/flatbuffers/go"
)

func Serve(handler XllService) {
	name := "{{.ProjectName}}"
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "-xll-shm=") {
			name = strings.TrimPrefix(arg, "-xll-shm=")
		}
	}

	client, err := shm.Connect(name)
	if err != nil {
		panic(fmt.Errorf("failed to connect to SHM: %w", err))
	}
	defer client.Close()

	// Configuration
	timeout := 10 * time.Second
	if t, err := time.ParseDuration("{{.ServerTimeout}}"); err == nil && t > 0 {
		timeout = t
	}

	workerCount := 100
	if n := {{.ServerWorkers}}; n > 0 {
		workerCount = n
	}

	// Worker Pool
	jobQueue := make(chan func(), workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			for job := range jobQueue {
				job()
			}
		}()
	}

	client.Handle(func(req []byte, respBuf []byte, msgId uint32) int32 {
		builder := flatbuffers.NewBuilder(0)
		builder.Reset()

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		// Note: We cannot defer cancel() unconditionally because async jobs need the context.

		switch msgId {
{{range $i, $fn := .Functions}}		case {{add 11 $i}}: // {{.Name}}
			{{if .Async}}
			// Async: Hand off to worker
			reqCopy := make([]byte, len(req))
			copy(reqCopy, req)

			jobQueue <- func() {
				defer cancel()
				handle{{.Name}}(ctx, reqCopy, nil, handler, nil, client, msgId)
			}
			return 0
			{{else}}
			// Sync: Run inline
			defer cancel()
			return handle{{.Name}}(ctx, req, respBuf, handler, builder, client, msgId)
			{{end}}
{{end}}		default:
			cancel()
			return 0
		}
	})

	client.Start()
	client.Wait()
}

{{range $i, $fn := .Functions}}
func handle{{.Name}}(ctx context.Context, req []byte, respBuf []byte, handler XllService, b *flatbuffers.Builder, client *shm.Client, msgId uint32) int32 {
	request := ipc.GetRootAs{{.Name}}Request(req, 0)

	// Extract args
	{{range .Args}}
	{{if eq .Type "string"}}
	arg_{{.Name}} := string(request.{{.Name|capitalize}}())
	{{else}}
	arg_{{.Name}} := request.{{.Name|capitalize}}()
	{{end}}
	{{end}}

	{{if .Async}}
	// Async execution
	handle := request.AsyncHandle()

	if ctx.Err() != nil {
		sendAsyncError{{.Name}}(client, msgId, handle, ctx.Err())
		return 0
	}

	// Call handler
	res, err := handler.{{.Name}}(ctx, {{range .Args}}arg_{{.Name}}, {{end}})

	b2 := flatbuffers.NewBuilder(0)
	var errOffset flatbuffers.UOffsetT
	if err != nil {
		errOffset = b2.CreateString(err.Error())
	}

	{{if eq .Return "string"}}
	var resOffset flatbuffers.UOffsetT
	if err == nil {
		resOffset = b2.CreateString(res)
	}
	{{end}}

	ipc.{{.Name}}ResponseStart(b2)
	ipc.{{.Name}}ResponseAddAsyncHandle(b2, handle)
	if err != nil {
		ipc.{{.Name}}ResponseAddError(b2, errOffset)
	} else {
		{{if eq .Return "string"}}
		ipc.{{.Name}}ResponseAddResult(b2, resOffset)
		{{else}}
		ipc.{{.Name}}ResponseAddResult(b2, res)
		{{end}}
	}
	root := ipc.{{.Name}}ResponseEnd(b2)
	b2.Finish(root)

	// Send Guest Call
	if _, err := client.SendGuestCall(b2.FinishedBytes(), msgId); err != nil {
		fmt.Printf("Error sending guest call for {{.Name}}: %v\n", err)
	}
	return 0
	{{else}}
	// Sync execution
	// Call handler
	res, err := handler.{{.Name}}(ctx, {{range .Args}}arg_{{.Name}}, {{end}})

	b.Reset()
	var errOffset flatbuffers.UOffsetT
	if err != nil {
		errOffset = b.CreateString(err.Error())
	}

	{{if eq .Return "string"}}
	var resOffset flatbuffers.UOffsetT
	if err == nil {
		resOffset = b.CreateString(res)
	}
	{{end}}

	ipc.{{.Name}}ResponseStart(b)
	if err != nil {
		ipc.{{.Name}}ResponseAddError(b, errOffset)
	} else {
		{{if eq .Return "string"}}
		ipc.{{.Name}}ResponseAddResult(b, resOffset)
		{{else}}
		ipc.{{.Name}}ResponseAddResult(b, res)
		{{end}}
	}
	root := ipc.{{.Name}}ResponseEnd(b)
	b.Finish(root)

	// Copy to respBuf
	payload := b.FinishedBytes()
	if len(payload) > len(respBuf) {
		return 0 // Error: buffer too small
	}
	copy(respBuf, payload)
	return int32(len(payload))
	{{end}}
}

{{if .Async}}
func sendAsyncError{{.Name}}(client *shm.Client, msgId uint32, handle uint64, err error) {
	b := flatbuffers.NewBuilder(0)
	errOffset := b.CreateString(err.Error())
	ipc.{{.Name}}ResponseStart(b)
	ipc.{{.Name}}ResponseAddAsyncHandle(b, handle)
	ipc.{{.Name}}ResponseAddError(b, errOffset)
	root := ipc.{{.Name}}ResponseEnd(b)
	b.Finish(root)
	client.SendGuestCall(b.FinishedBytes(), msgId)
}
{{end}}
{{end}}
`
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
				"int":    "int32",
				"float":  "float64",
				"string": "string",
				"bool":   "bool",
			}
			if v, ok := m[t]; ok {
				return v
			}
			return t
		},
	}

	t, err := template.New("server").Funcs(funcMap).Parse(tmpl)
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
		ServerTimeout string
		ServerWorkers int
	}{
		Package:       pkg,
		ModName:     modName,
		ProjectName:   config.Project.Name,
		Functions:     config.Functions,
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
	tmpl := `
#include <windows.h>
#include <string>
#include <thread>
#include <atomic>
#include <chrono>
#include "include/xlcall.h"
#include "include/xll_mem.h"
#include "shm/DirectHost.h"
#include "schema_generated.h"

shm::DirectHost g_host;
std::thread g_worker;
std::atomic<bool> g_running{false};

// Utility: String Conversion
std::string WStringToString(const std::wstring& wstr) {
    if (wstr.empty()) return std::string();
    int size_needed = WideCharToMultiByte(CP_UTF8, 0, &wstr[0], (int)wstr.size(), NULL, 0, NULL, NULL);
    std::string strTo(size_needed, 0);
    WideCharToMultiByte(CP_UTF8, 0, &wstr[0], (int)wstr.size(), &strTo[0], size_needed, NULL, NULL);
    return strTo;
}

std::wstring StringToWString(const std::string& str) {
    if (str.empty()) return std::wstring();
    int size_needed = MultiByteToWideChar(CP_UTF8, 0, &str[0], (int)str.size(), NULL, 0);
    std::wstring wstrTo(size_needed, 0);
    MultiByteToWideChar(CP_UTF8, 0, &str[0], (int)str.size(), &wstrTo[0], size_needed);
    return wstrTo;
}

// Guest Call Handler (Async Return)
int32_t GuestHandler(const uint8_t* req, uint8_t* resp, uint32_t msgId) {
    switch (msgId) {
{{range $i, $fn := .Functions}}
    {{if .Async}}
    case {{add 11 $i}}: { // {{.Name}}
        auto response = ipc::Get{{.Name}}Response(req);
        LPXLOPER12 h = (LPXLOPER12)response->async_handle();

        if (response->error() && response->error()->size() > 0) {
            XLOPER12 xErr; xErr.xltype = xltypeErr; xErr.val.err = xlErrValue;
            Excel12(xlAsyncReturn, 0, 2, h, &xErr);
            return 0;
        }

        {{if eq .Return "string"}}
        std::wstring wres = StringToWString(response->result()->str());
        LPXLOPER12 xRes = NewExcelString(wres.c_str());
        Excel12(xlAsyncReturn, 0, 2, h, xRes);
        // NewExcelString sets xlbitDLLFree, so we can use xlAutoFree12 to clean it up.
        // This handles both the string buffer and returning the XLOPER12 to the pool.
        xlAutoFree12(xRes);
        {{else if eq .Return "int"}}
        XLOPER12 xRes; xRes.xltype = xltypeInt; xRes.val.w = response->result();
        Excel12(xlAsyncReturn, 0, 2, h, &xRes);
        {{else if eq .Return "float"}}
        XLOPER12 xRes; xRes.xltype = xltypeNum; xRes.val.num = response->result();
        Excel12(xlAsyncReturn, 0, 2, h, &xRes);
        {{else if eq .Return "bool"}}
        XLOPER12 xRes; xRes.xltype = xltypeBool; xRes.val.xbool = response->result() ? 1 : 0;
        Excel12(xlAsyncReturn, 0, 2, h, &xRes);
        {{end}}
        return 0;
    }
    {{end}}
{{end}}
    default:
        return 0;
    }
}

extern "C" {

int __stdcall xlAutoOpen() {
    // 1024 slots, 1MB size, 16 guest slots
    {{if .ShouldAppendPid}}
    std::string shmName = "{{.ProjectName}}_" + std::to_string(GetCurrentProcessId());
    if (!g_host.Init(shmName.c_str(), 1024, 1024*1024, 16)) {
        return 0;
    }
    {{else}}
    if (!g_host.Init("{{.ProjectName}}", 1024, 1024*1024, 16)) {
        return 0;
    }
    {{end}}

    g_running = true;
    g_worker = std::thread([]{
        while(g_running) {
             int n = g_host.ProcessGuestCalls(GuestHandler);
             if (n == 0) std::this_thread::sleep_for(std::chrono::milliseconds(10));
        }
    });

    static XLOPER12 xDll;
    Excel12(xlGetName, &xDll, 0);

{{range $i, $fn := .Functions}}
    {
        Excel12(xlfRegister, 0, {{registerCount $fn}},
            &xDll,
            TempStr12(L"{{.Name}}"),
            TempStr12(L"{{if .Async}}>{{end}}{{lookupXllType .Return}}{{range .Args}}{{lookupArgXllType .Type}}{{end}}{{if .Async}}X{{end}}$"),
            TempStr12(L"{{.Name}}"),
            TempStr12(L"{{joinArgNames $fn}}"),
            TempStr12(L"1"),
            TempStr12(L"{{withDefault .Category $.ProjectName}}"),
            TempStr12(L"{{.Shortcut}}"),
            TempStr12(L"{{.HelpTopic}}"),
            TempStr12(L"{{.Description}}"){{if .Args}},{{end}}
            {{range $j, $arg := .Args}}TempStr12(L"{{$arg.Description}}"){{if lt $j (sub (len $fn.Args) 1)}},{{end}}{{end}}
            {{if .Async}}{{if .Args}},{{end}}TempStr12(L"Async Handle"){{end}}
        );
    }
{{end}}

    Excel12(xlFree, 0, 1, &xDll);
    return 1;
}

int __stdcall xlAutoClose() {
    g_running = false;
    if (g_worker.joinable()) g_worker.join();
    g_host.Shutdown();
    return 1;
}

{{range $i, $fn := .Functions}}
{{if .Async}}void{{else}}{{lookupCppType .Return}}{{end}} __stdcall {{.Name}}({{range $j, $arg := .Args}}{{lookupArgCppType $arg.Type}} {{$arg.Name}}{{if lt $j (sub (len $fn.Args) 1)}}, {{end}}{{end}}{{if .Async}}{{if .Args}}, {{end}}LPXLOPER12 asyncHandle{{end}}) {

    flatbuffers::FlatBufferBuilder builder;

    {{range .Args}}
    {{if eq .Type "string"}}
    std::wstring wstr_{{.Name}};
    if ({{.Name}}) {
        wstr_{{.Name}}.assign({{.Name}} + 1, (size_t){{.Name}}[0]);
    }
    auto {{.Name}}_off = builder.CreateString(WStringToString(wstr_{{.Name}}));
    {{end}}
    {{end}}

    ipc::{{.Name}}RequestBuilder reqBuilder(builder);
    {{range .Args}}
    {{if eq .Type "string"}}
    reqBuilder.add_{{.Name}}({{.Name}}_off);
    {{else}}
    reqBuilder.add_{{.Name}}({{.Name}});
    {{end}}
    {{end}}
    {{if .Async}}
    reqBuilder.add_async_handle((uint64_t)asyncHandle);
    {{end}}
    auto req = reqBuilder.Finish();
    builder.Finish(req);

    std::vector<uint8_t> response;
    int ok = g_host.Send(builder.GetBufferPointer(), builder.GetSize(), {{add 11 $i}}, response);

    if (ok < 0) {
        {{if .Async}}return;{{else}}return {{defaultErrorVal .Return}};{{end}}
    }

    {{if .Async}}
    return;
    {{else}}
    auto resp = ipc::Get{{.Name}}Response(response.data());
    if (resp->error() && resp->error()->size() > 0) {
        return {{defaultErrorVal .Return}};
    }

    {{if eq .Return "string"}}
    std::wstring wres = StringToWString(resp->result()->str());
    return NewExcelString(wres.c_str());
    {{else}}
    return resp->result();
    {{end}}
    {{end}}
}
{{end}}

} // extern "C"
`
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
				"int":    "int32_t",
				"float":  "double",
				"string": "LPXLOPER12",
				"bool":   "short",
			}
			if v, ok := m[t]; ok { return v }
			return t
		},
		"lookupArgCppType": func(t string) string {
			m := map[string]string{
				"int":    "int32_t",
				"float":  "double",
				"string": "const wchar_t*",
				"bool":   "short",
			}
			if v, ok := m[t]; ok { return v }
			return t
		},
		"lookupXllType": func(t string) string {
			m := map[string]string{
				"int":    "J",
				"float":  "B",
				"string": "Q",
				"bool":   "A",
			}
			if v, ok := m[t]; ok { return v }
			return t
		},
		"lookupArgXllType": func(t string) string {
			m := map[string]string{
				"int":    "J",
				"float":  "B",
				"string": "D%",
				"bool":   "A",
			}
			if v, ok := m[t]; ok { return v }
			return t
		},
		"defaultErrorVal": func(t string) string {
			if t == "string" { return "NULL"; }
			return "0";
		},
	}

	t, err := template.New("cpp").Funcs(funcMap).Parse(tmpl)
	if err != nil { return err }

	f, err := os.Create(filepath.Join(dir, "xll_main.cpp"))
	if err != nil { return err }
	defer f.Close()

	return t.Execute(f, struct {
		ProjectName     string
		Functions       []Function
		ShouldAppendPid bool
	}{
		ProjectName:     config.Project.Name,
		Functions:       config.Functions,
		ShouldAppendPid: shouldAppendPid,
	})
}

func generateCMake(config Config, dir string) error {
	tmpl := `cmake_minimum_required(VERSION 3.14)
project({{ .ProjectName }} LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

include(FetchContent)

# Flatbuffers
FetchContent_Declare(
  flatbuffers
  GIT_REPOSITORY https://github.com/google/flatbuffers.git
  GIT_TAG v25.9.23
)
FetchContent_MakeAvailable(flatbuffers)

# SHM
FetchContent_Declare(
  shm
  GIT_REPOSITORY https://github.com/xll-gen/shm.git
  GIT_TAG main
)
FetchContent_MakeAvailable(shm)

if(NOT TARGET shm)
  add_library(shm INTERFACE)
  target_include_directories(shm INTERFACE ${shm_SOURCE_DIR}/include)
endif()

file(GLOB SOURCES
    "${CMAKE_CURRENT_SOURCE_DIR}/xll_main.cpp"
    "${CMAKE_CURRENT_SOURCE_DIR}/include/*.cpp"
)

add_library(${PROJECT_NAME} SHARED ${SOURCES})

target_include_directories(${PROJECT_NAME} PRIVATE
  ${CMAKE_CURRENT_SOURCE_DIR}/include
)

target_link_libraries(${PROJECT_NAME} PRIVATE
  shm
  flatbuffers::flatbuffers
)

if(NOT MSVC)
  target_compile_options(${PROJECT_NAME} PRIVATE
    $<$<CONFIG:Release>:-O3>
    $<$<CONFIG:Release>:-march=native>
    $<$<CONFIG:Release>:-flto>
  )
  target_link_options(${PROJECT_NAME} PRIVATE $<$<CONFIG:Release>:-flto>)
endif()

set_target_properties(${PROJECT_NAME} PROPERTIES SUFFIX ".xll")
`
	t, err := template.New("cmake").Parse(tmpl)
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
	tmpl := `version: '3'

tasks:
  default:
    cmds:
      - task: build

  build:
    desc: Build both Go server and C++ XLL (Release)
    cmds:
      - task: build-go
      - task: build-cpp

  build-go:
    desc: Build Go server
    cmds:
      - go build -o build/{{.ProjectName}}.exe main.go

  build-cpp:
    desc: Build C++ XLL (Release)
    cmds:
      - cmake -S generated/cpp -B build/cpp -DCMAKE_BUILD_TYPE=Release
      - cmake --build build/cpp --config Release
      - cmd: cmake -E copy build/cpp/Release/{{.ProjectName}}.xll build/{{.ProjectName}}.xll
        ignore_error: true
      - cmd: cmake -E copy build/cpp/{{.ProjectName}}.xll build/{{.ProjectName}}.xll
        ignore_error: true

  clean:
    desc: Clean build artifacts
    cmds:
      - cmd: cmake -E remove_directory build
        ignore_error: true
      - cmd: cmake -E remove_directory generated
        ignore_error: true
`
	t, err := template.New("taskfile").Parse(tmpl)
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
