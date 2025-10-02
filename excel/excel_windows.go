package excel

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/xll-gen/xll-gen/debug"
	"github.com/xll-gen/xll-gen/xloper"
)

var emptyMref = &xloper.Mref{}

type iface struct {
	_, data unsafe.Pointer
}

type Excel struct {
	proc             uintptr
	dllFreeRegistry  sync.Map
	mu               sync.Mutex
	pinner           runtime.Pinner
	functions        map[float64]FunctionOpts
	endedHandlers    []func()
	canceledHandlers []func()
}

func NewExcel() (*Excel, error) {
	proc, err := CallbackProc()
	if err != nil {
		return nil, err
	}

	// Register the event handlers
	selfHandle, err := GetSelfModuleHandle()
	if err != nil {
		return nil, err
	}

	calculationEndedHandlerProc, err := GetProcAddress(selfHandle, "XllGenCalculationEndedHandler")
	if err != nil {
		return nil, err
	}

	calculationCanceledHandlerProc, err := GetProcAddress(selfHandle, "XllGenCalculationCanceledHandler")
	if err != nil {
		return nil, err
	}

	e := &Excel{
		proc:             proc,
		dllFreeRegistry:  sync.Map{},
		mu:               sync.Mutex{},
		pinner:           runtime.Pinner{},
		functions:        make(map[float64]FunctionOpts),
		endedHandlers:    nil,
		canceledHandlers: nil,
	}
	e.Call(XlEventRegister, calculationEndedHandlerProc, int32(XlEventCalculationEnded))
	e.Call(XlEventRegister, calculationCanceledHandlerProc, int32(XlEventCalculationCanceled))

	return e, nil
}

func (e *Excel) Proc() uintptr {
	return e.proc
}

func (e *Excel) Functions() map[float64]FunctionOpts {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.functions
}

func (e *Excel) GetName() (string, error) {

	name, err := e.Call(XlGetName)
	if err != nil {
		return "", fmt.Errorf("failed to get module name: %w", err)
	}

	var nameStr string
	var ok bool

	if nameStr, ok = name.(string); !ok {
		return "", fmt.Errorf("expected string type for module name, got %T", name)
	}

	return nameStr, nil
}

func (e *Excel) RegisterFunction(opts FunctionOpts) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	dllName, err := e.GetName()
	if err != nil {
		return fmt.Errorf("function register failed: %w, %v", err, opts)
	}

	argNames := make([]string, len(opts.Args))
	argDescs := make([]string, len(opts.Args))
	for i, arg := range opts.Args {
		argNames[i] = arg.Name
		argDescs[i] = arg.Desc
	}

	callParams := []any{
		dllName,
		opts.FuncName,
		opts.ParamTypes(),
		opts.FuncName,
		strings.Join(argNames, ","),
		opts.MacroType(),
		opts.Category,
		opts.ShortcutKey,
		opts.HelpTopic,
		opts.FuncDesc,
	}
	for _, desc := range argDescs {
		callParams = append(callParams, desc)
	}

	resId, err := e.Call(XlfRegister, callParams...)
	if err != nil {
		return fmt.Errorf("function register failed: %w, %v", err, opts)
	}
	id, ok := resId.(float64)
	if !ok {
		return fmt.Errorf("function register failed: expected float64 type for register ID, got %T, %v", resId, opts)
	}
	e.functions[id] = opts
	return nil
}

func (e *Excel) RegisterEventHandler(event XlEvent, handler func()) {
	switch event {
	case XlEventCalculationEnded:
		e.endedHandlers = append(e.endedHandlers, handler)
	case XlEventCalculationCanceled:
		e.canceledHandlers = append(e.canceledHandlers, handler)
	default:
	}
}

func (e *Excel) MarkDllFree(oper xloper.XLOPER) {
	p := runtime.Pinner{}
	oper.Pin(p)
	dataPtr := (*iface)(unsafe.Pointer(&oper)).data
	e.dllFreeRegistry.Store(uintptr(dataPtr), &DllFreeEntry{
		XLOPER: oper,
		Pinner: p,
	})
}

func (e *Excel) AutoFree(ptr uintptr) {
	entry, loaded := e.dllFreeRegistry.LoadAndDelete(ptr)
	if !loaded {
		return
	}

	val, ok := entry.(*DllFreeEntry)
	if !ok {
		return
	}
	val.Pinner.Unpin()
}

func (e *Excel) Call(command XlCommand, params ...any) (any, error) {
	return SyscallExcel(e.proc, command, params...)
}

func (e *Excel) XlFree(oper xloper.XLOPER) {
	if oper == nil {
		return
	}
	dataPtr := (*iface)(unsafe.Pointer(&oper)).data
	if dataPtr == nil {
		return
	}
	if !oper.Type().IsXlFree() {
		return
	}

	SysCallExcelRaw(e.proc, XlFree, nil, oper)
}

func (e *Excel) HandleCalculationEnded() {
	for _, handler := range e.endedHandlers {
		handler()
	}
}

func (e *Excel) HandleCalculationCanceled() {
	for _, handler := range e.canceledHandlers {
		handler()
	}
}

func (e *Excel) SheetName(idSheet uintptr) (string, error) {
	var res any
	var err error
	if idSheet == 0 {
		res, err = e.Call(XlSheetNm, emptyMref)
		if err != nil {
			return "", err
		}
	} else {
		mref := xloper.NewMref(idSheet, []xloper.XLREF{{}}) // Dummy ref to get sheet name
		if mref == nil {
			return "", fmt.Errorf("failed to create Mref for sheet ID: %v", idSheet)
		}
		res, err = e.Call(XlSheetNm, mref)
		if err != nil {
			return "", err
		}
	}
	sheetName, ok := res.(string)
	if !ok {
		return "", fmt.Errorf("expected string type for sheet name, got %T", res)
	}
	return sheetName, nil
}

func (e *Excel) SheetId(sheetNm string) (uintptr, error) {
	if sheetNm == "" {
		return 0, xloper.ErrInvalid
	}

	res, err := e.Call(XlSheetId, sheetNm)
	if err != nil {
		return 0, err
	}

	mref, ok := res.(*xloper.Mref)
	if !ok {
		return 0, fmt.Errorf("expected Mref type for current sheet ID got %T", res)
	}
	return mref.IdSheet(), nil
}

func (e *Excel) CurrentSheetId() (uintptr, error) {
	resStr := xloper.String{}
	resMref := xloper.Mref{}
	defer e.XlFree(&resStr)
	defer e.XlFree(&resMref)

	callRes, err := SysCallExcelRaw(e.proc, XlSheetNm, &resStr, emptyMref) // Refresh the current sheet info
	if err != nil {
		return 0, fmt.Errorf("failed to get current sheet name: %w", err)
	}
	if callRes != XlretSuccess {
		return 0, fmt.Errorf("failed to get current sheet name, excel returned %d (%v)", callRes, callRes)
	}

	callRes, err = SysCallExcelRaw(e.proc, XlSheetId, &resMref, &resStr) // Get the sheet ID for the current sheet name
	if err != nil {
		return 0, fmt.Errorf("failed to get current sheet ID: %w", err)
	}
	if callRes != XlretSuccess {
		return 0, fmt.Errorf("failed to get current sheet ID, excel returned %d (%v)", callRes, callRes)
	}
	if resMref.Type().Base() != xloper.TypeRef {
		return 0, fmt.Errorf("expected Mref type for current sheet ID, got %v", resMref.Type())
	}
	return resMref.IdSheet(), nil
}

func (e *Excel) RangeFromRef(r xloper.Ref) (Range, error) {
	sheetName, err := e.SheetName(r.IdSheet())
	if err != nil {
		return Range{}, fmt.Errorf("failed to get sheet name for range: %w, %v", err, r)
	}
	if sheetName == "" {
		return Range{}, fmt.Errorf("cannot get range without sheet name: %v", r)
	}
	mref, err := r.Mref()
	if err != nil {
		return Range{}, fmt.Errorf("failed to get Mref for range: %w, %v", err, r)
	}

	return Range{
		SheetName: sheetName,
		xlRefs:    mref.XLREFs(),
	}, nil
}

func (e *Excel) Caller() (Range, error) {
	res, err := e.Call(XlfCaller)
	if err != nil {
		return Range{}, fmt.Errorf("failed to get caller: %w", err)
	}

	ref, ok := res.(xloper.Ref)
	if !ok {
		return Range{}, fmt.Errorf("expected Ref type for caller, got %T", res)
	}

	mref, err := ref.Mref()
	if err != nil {
		return Range{}, fmt.Errorf("failed to get Mref for caller: %w, %v", err, ref)
	}

	return e.RangeFromRef(mref)
}

func (e *Excel) Coerce(r xloper.Ref) (any, error) {
	if r == nil {
		return nil, nil
	}

	mref, err := r.Mref()
	if err != nil {
		return nil, fmt.Errorf("failed to get Mref for Ref: %w, %v", err, r)
	}

	if len(mref.XLREFs()) == 0 {
		return nil, xloper.ErrInvalid
	}

	if mref.IdSheet() == 0 {
		idSheet, err := e.CurrentSheetId()
		mref.Set(idSheet, mref.XLREFs())
		if err != nil {
			return nil, fmt.Errorf("failed to get current sheet ID: %w", err)
		}
	}

	return e.Call(XlCoerce, mref)
}

func (e *Excel) Close() error {
	var err error
	e.mu.Lock()
	defer e.mu.Unlock()
	e.dllFreeRegistry.Range(func(key, value any) bool {
		if k, ok := key.(uintptr); ok {
			e.AutoFree(k)
		}
		return true
	})

	// Unregister event handlers
	_, err = e.Call(XlEventRegister, nil, int32(XlEventCalculationEnded))
	if err != nil {
		debug.Debug("Failed to unregister calculation ended event", "error", err)
	}
	_, err = e.Call(XlEventRegister, nil, int32(XlEventCalculationCanceled))
	if err != nil {
		debug.Debug("Failed to unregister calculation canceled event", "error", err)
	}

	// Unregister functions
	for regId, fn := range e.functions {
		// Unregister using the obtained ID
		_, err = e.Call(XlfUnregister, regId)
		if err != nil {
			debug.Debug("Failed to unregister function", "function", fn.FuncName, "error", err)
		}
	}

	e.functions = nil
	e.endedHandlers = nil
	e.canceledHandlers = nil
	e.pinner.Unpin()
	e.proc = 0

	return nil
}
