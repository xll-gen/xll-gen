package xloper

import (
	"runtime"
	"unsafe"
)

type XlType uint32

const (
	XlOper12Size = 32
	XlTypeOffset = 24
	ptrSize      = unsafe.Sizeof(uintptr(0))
)

const (
	// XLOPER12 type constants (xltype)
	TypeNum     XlType = 0x0001 // Number (double)
	TypeStr     XlType = 0x0002 // String (LPWSTR, Pascal-style)
	TypeBool    XlType = 0x0004 // Boolean (BOOL)
	TypeRef     XlType = 0x0008 // Reference (XLREF12*)
	TypeErr     XlType = 0x0010 // Error (WORD)
	TypeFlow    XlType = 0x0020 // Flow control (not for UDFs)
	TypeMulti   XlType = 0x0040 // Multi (XLOPER12 array)
	TypeMissing XlType = 0x0080 // Missing argument
	TypeNil     XlType = 0x0100 // Empty value
	TypeSRef    XlType = 0x0400 // Simple reference (XLMREF12*)
	TypeInt     XlType = 0x0800 // Integer (val.w, a 32-bit signed integer)
	TypeBigData XlType = TypeStr | TypeInt

	// Flags for xltype (can be ORed with base type)
	BitXLFree  XlType = 0x1000 // Excel is responsible for freeing memory pointed to by XLOPER12 members (e.g., str, multi).
	BitDLLFree XlType = 0x4000 // DLL is responsible for freeing memory. Excel will call xlAutoFree12 (for XLOPER12).
)

// Base returns the fundamental type of the XLOPER by stripping any memory management flags.
func (t XlType) Base() XlType {
	return t &^ (BitXLFree | BitDLLFree)
}

// IsXlFree checks if the BitXLFree flag is set, indicating Excel manages the memory.
func (t XlType) IsXlFree() bool {
	return t&BitXLFree != 0
}

// IsDLLFree checks if the BitDLLFree flag is set, indicating the DLL manages the memory.
func (t XlType) IsDLLFree() bool {
	return t&BitDLLFree != 0
}

// XLOPER is an interface for Go representations of XLOPER12.
type XLOPER interface {
	Type() XlType // Returns the Excel type (e.g., TypeNum)
	Value() any
	Pin(p *runtime.Pinner)
}
