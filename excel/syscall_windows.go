//go:build windows

package excel

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/xll-gen/xll-gen/debug"
	"github.com/xll-gen/xll-gen/xloper"
	"golang.org/x/sys/windows"
)

const (
	GET_MODULE_HANDLE_EX_FLAG_FROM_ADDRESS       = 0x00000004
	GET_MODULE_HANDLE_EX_FLAG_UNCHANGED_REFCOUNT = 0x00000002
	MAX_PATH                                     = 260 // Standard Windows MAX_PATH
)

type XlCommand uintptr

// Excel C API function number bits and special commands
const (
	xlCmdFlag XlCommand = 0x8000
	xlSpecial XlCommand = 0x4000
	xlIntl    XlCommand = 0x2000
	xlPrompt  XlCommand = 0x1000
)

// Excel C API auxiliary function numbers (xlSpecial group)
const (
	XlFree             XlCommand = 0 | xlSpecial
	XlStack            XlCommand = 1 | xlSpecial
	XlCoerce           XlCommand = 2 | xlSpecial
	XlSet              XlCommand = 3 | xlSpecial
	XlSheetId          XlCommand = 4 | xlSpecial
	XlSheetNm          XlCommand = 5 | xlSpecial
	XlAbort            XlCommand = 6 | xlSpecial
	XlGetInst          XlCommand = 7 | xlSpecial
	XlGetHwnd          XlCommand = 8 | xlSpecial
	XlGetName          XlCommand = 9 | xlSpecial
	XlEnableXLMsgs     XlCommand = 10 | xlSpecial
	XlDisableXLMsgs    XlCommand = 11 | xlSpecial
	XlDefineBinaryName XlCommand = 12 | xlSpecial
	XlGetBinaryName    XlCommand = 13 | xlSpecial
	XlGetFmlaInfo      XlCommand = 14 | xlSpecial
	XlGetMouseInfo     XlCommand = 15 | xlSpecial
	XlAsyncReturn      XlCommand = 16 | xlSpecial
	XlEventRegister    XlCommand = 17 | xlSpecial
	XlRunningOnCluster XlCommand = 18 | xlSpecial
	XlGetInstPtr       XlCommand = 19 | xlSpecial
	XlfCaller          XlCommand = 89
	XlfRegister        XlCommand = 149
	XlfUnregister      XlCommand = 201
	XlfRegisterId      XlCommand = 267
)

type XlEvent int

const (
	XlEventCalculationEnded    XlEvent = 1
	XlEventCalculationCanceled XlEvent = 2
)

// Excel return codes (xlret*)
type XlReturnCode int

const (
	XlretSuccess                XlReturnCode = 0   // success
	XlretAbort                  XlReturnCode = 1   // macro halted
	XlretInvXlfn                XlReturnCode = 2   // invalid function number
	XlretInvCount               XlReturnCode = 4   // invalid number of arguments
	XlretInvXloper              XlReturnCode = 8   // invalid OPER structure
	XlretStackOvfl              XlReturnCode = 16  // stack overflow
	XlretFailed                 XlReturnCode = 32  // command failed
	XlretUncalced               XlReturnCode = 64  // uncalced cell
	XlretNotThreadSafe          XlReturnCode = 128 // not allowed during multi-threaded calc
	XlretInvAsynchronousContext XlReturnCode = 256 // invalid asynchronous function handle
	XlretNotClusterSafe         XlReturnCode = 512 // not supported on cluster
)

// String makes XlReturnCode human-readable.
func (rc XlReturnCode) String() string {
	switch rc {
	case XlretSuccess:
		return "xlretSuccess"
	case XlretAbort:
		return "xlretAbort"
	case XlretInvXlfn:
		return "xlretInvXlfn"
	case XlretInvCount:
		return "xlretInvCount"
	case XlretInvXloper:
		return "xlretInvXloper"
	case XlretStackOvfl:
		return "xlretStackOvfl"
	case XlretFailed:
		return "xlretFailed"
	case XlretUncalced:
		return "xlretUncalced"
	case XlretNotThreadSafe:
		return "xlretNotThreadSafe"
	case XlretInvAsynchronousContext:
		return "xlretInvAsynchronousContext"
	case XlretNotClusterSafe:
		return "xlretNotClusterSafe"
	default:
		return fmt.Sprintf("Unknown XlReturnCode: %d", rc)
	}
}

var kernel32DLL = syscall.NewLazyDLL("kernel32.dll")
var getModuleHandleWProc = kernel32DLL.NewProc("GetModuleHandleW")

// GetSelfModuleHandle returns the handle of the current process module.
func GetSelfModuleHandle() (syscall.Handle, error) {
	r1, _, _ := syscall.SyscallN(getModuleHandleWProc.Addr(), 0)
	if r1 == 0 {
		lastErr := syscall.GetLastError()
		return 0, fmt.Errorf("GetModuleHandleW failed: %v", lastErr)
	}
	return syscall.Handle(r1), nil
}

// GetProcAddress retrieves the address of an exported function or variable from the specified DLL.
func GetProcAddress(module syscall.Handle, procName string) (uintptr, error) {
	return syscall.GetProcAddress(module, procName)
}

// FreeLibrary frees the loaded dynamic-link library (DLL) module.
func FreeLibrary(module syscall.Handle) error {
	return syscall.FreeLibrary(module)
}

func LoadLibrary(dllPath string) (syscall.Handle, error) {
	handle, err := windows.LoadLibrary(dllPath)
	return syscall.Handle(handle), err
}

func CallbackProc() (uintptr, error) {
	handle, err := GetSelfModuleHandle()
	if err != nil {
		return 0, err
	}
	procPtr, err := GetProcAddress(handle, "Excel12v")
	if err != nil || procPtr == 0 {
		return 0, fmt.Errorf("error getting proc address: %v", err)
	}
	return procPtr, nil
}

// SysCallExcelRaw performs the actual call to the Excel callback function.
// It takes a pointer to the result XLOPER and a variable number of pointers to argument XLOPERs.
// It returns the raw return value from the syscall and any error encountered.
func SysCallExcelRaw(proc uintptr, command XlCommand, pResult xloper.XLOPER, pOpers ...xloper.XLOPER) (XlReturnCode, error) {
	var pinner runtime.Pinner
	defer pinner.Unpin()

	resDataPtr := (*iface)(unsafe.Pointer(&pResult)).data
	if resDataPtr != nil {
		pinner.Pin(pResult)
	}

	numOpers := len(pOpers)
	pOpersArray := make([]unsafe.Pointer, numOpers)
	for i, p := range pOpers {
		ptr := (*iface)(unsafe.Pointer(&p)).data
		pOpersArray[i] = ptr
		pinner.Pin(p)
	}
	pinner.Pin(pOpersArray)

	callRetVal, _, callErr := syscall.SyscallN(
		proc,
		uintptr(command),
		uintptr(unsafe.Pointer(resDataPtr)),
		uintptr(numOpers),
		uintptr(unsafe.Pointer(&pOpersArray[0])),
	)

	if callErr != 0 {
		return 0, syscall.Errno(callErr)
	}
	if callRetVal != 0 {
		return 0, fmt.Errorf("excel call failed with code %d (%v)", callRetVal, XlReturnCode(callRetVal))
	}
	return XlReturnCode(callRetVal), nil
}

// SyscallExcel performs the actual call to the Excel callback function.
func SyscallExcel(proc uintptr, command XlCommand, params ...any) (any, error) {
	numOpers := len(params)
	var argOpers *xloper.Multi
	var pOpersArray unsafe.Pointer
	var pinner runtime.Pinner
	var err error

	defer pinner.Unpin()

	if numOpers > 0 {
		// Create a multi-oper from the params. This creates a 1-row, N-column Multi array
		argOpers, err = xloper.NewMulti([][]any{params})
		if err != nil {
			return nil, err
		}
		argOpers.Pin(pinner)
		pOpersArray = unsafe.Pointer(argOpers.Ptr())
	} else {
		pOpersArray = unsafe.Pointer(nil)
	}

	result := xloper.NewEmpty()
	pinner.Pin(result)
	pResult := unsafe.Pointer(result)

	callRetVal, _, callErr := syscall.SyscallN(
		proc,
		uintptr(command),
		uintptr(pResult),
		uintptr(numOpers),
		uintptr(pOpersArray),
	)

	debug.Debug("SyscallExcel", "command", command, "params", params, "result", result, "callRetVal", callRetVal, "callErr", callErr)

	if callErr != 0 {
		return nil, syscall.Errno(callErr)
	}
	if callRetVal != 0 {
		return nil, fmt.Errorf("excel call failed with code %d (%v)", callRetVal, XlReturnCode(callRetVal))
	}

	retVal := result.Value()

	if result.Type().IsXlFree() {
		// If the result is an XLOPER that needs to be freed, we need to handle it.
		freeRetVal, _, freeErr := syscall.SyscallN(
			proc,
			uintptr(XlFree), // XlFree is a special command
			1,
			uintptr(pResult),
			0,
		)
		if freeErr != 0 {
			return nil, syscall.Errno(freeErr)
		}
		if freeRetVal != 0 {
			return nil, fmt.Errorf("xlFree failed with code %d (%v)", freeRetVal, XlReturnCode(freeRetVal))
		}
	}

	return retVal, nil
}
