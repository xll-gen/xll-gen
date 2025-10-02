package excel

import (
	"runtime"
	"strings"

	"github.com/xll-gen/xll-gen/xloper"
)

var (
	Functions     = make(map[string]FunctionOpts)
	EventHandlers = make(map[XlEvent][]string)
	DefaultExcel  *Excel
)

// FuncArg holds the name and description for a single function argument.
type FuncArg struct {
	Name string
	Type string
	Desc string
}

// FunctionOpts holds all options for registering a User Defined Function (UDF) or macro.
type FunctionOpts struct {
	DllPath                string // Path to the XLL file. Can often be empty if Excel loaded the DLL.
	ProcName               string // Exported name of the function in the DLL (the //export name).
	FuncName               string // Name of the function as it will appear in Excel.
	Args                   []FuncArg
	Category               string // Category in Excel's function wizard.
	ShortcutKey            string // Shortcut key (optional).
	HelpTopic              string // Help topic URL or file path (optional).
	FuncDesc               string // Description of the function.
	IsAsync                bool   // True if the function is asynchronous.
	IsVolatile             bool   // True if the function is volatile.
	IsThreadSafe           bool   // True if the function is thread-safe.
	IsMacroSheetEquivalent bool   // True if it's a macro sheet equivalent function.
}

func (opts FunctionOpts) ParamTypes() string {
	var sb strings.Builder
	if opts.IsAsync {
		sb.WriteString(">")
	}

	for _, arg := range opts.Args {
		sb.WriteString(arg.Type)
	}

	if opts.IsAsync {
		sb.WriteString("X")
	}
	if opts.IsThreadSafe {
		sb.WriteString("$")
	}
	if opts.IsVolatile {
		sb.WriteString("!")
	}
	return sb.String()
}

func (opts FunctionOpts) MacroType() int32 {
	if opts.IsMacroSheetEquivalent {
		return 2
	}
	return 1
}

type DllFreeEntry struct {
	XLOPER xloper.XLOPER
	Pinner runtime.Pinner
}

func RegisterEventHandler(event XlEvent, handlerName string) {
	EventHandlers[event] = append(EventHandlers[event], handlerName)
}

func RegisterFunction(opts FunctionOpts) {
	Functions[opts.FuncName] = opts
}

func AutoOpen() error {
	excel, err := NewExcel()
	if err != nil {
		return err
	}
	DefaultExcel = excel
	return nil
}

func AutoClose() error {
	if DefaultExcel != nil {
		return DefaultExcel.Close()
	}
	return nil
}
