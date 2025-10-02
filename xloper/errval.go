package xloper

import (
	"fmt"
	"runtime"
	"unsafe"
)

// XlErrorCode defines the type for Excel error codes (e.g., #NULL!, #DIV/0!).
type XlErrorCode uint16 // This is a WORD (uint16)

const (
	// Excel error codes (subset, for val.err when xltype is XlTypeErr) - these are WORDs
	XlErrNull        XlErrorCode = 0    // #NULL!
	XlErrDiv0        XlErrorCode = 7    // #DIV/0!
	XlErrValue       XlErrorCode = 15   // #VALUE!
	XlErrRef         XlErrorCode = 23   // #REF!
	XlErrName        XlErrorCode = 29   // #NAME?
	XlErrNum         XlErrorCode = 36   // #NUM!
	XlErrNA          XlErrorCode = 42   // #N/A
	XlErrGettingData XlErrorCode = 0x2B // #GETTING_DATA (Excel 2010+)
)

// String returns the string representation of the XlErrorCode.
func (e XlErrorCode) String() string {
	switch e {
	case XlErrNull:
		return "#NULL!"
	case XlErrDiv0:
		return "#DIV/0!"
	case XlErrValue:
		return "#VALUE!"
	case XlErrRef:
		return "#REF!"
	case XlErrName:
		return "#NAME?"
	case XlErrNum:
		return "#NUM!"
	case XlErrNA:
		return "#N/A"
	case XlErrGettingData:
		return "#GETTING_DATA"
	default:
		return fmt.Sprintf("#ERR_CODE(%d)", e)
	}
}

var _ XLOPER = (*xlErr)(nil)

type xlErr struct {
	val XlErrorCode
	_   [XlTypeOffset - unsafe.Sizeof(XlErrorCode(0))]byte
	typ XlType
}

func (e *xlErr) Type() XlType {
	return e.typ
}

func (e *xlErr) ErrCode() XlErrorCode {
	return e.val
}

func (e *xlErr) Value() any {
	return e.ErrCode()
}

func (e *xlErr) String() string {
	return e.ErrCode().String()
}

// Pin is a no-op for error instances as it's a global singleton.
func (e *xlErr) Pin(p runtime.Pinner) {
	// no-op
}

func SetError(a *Any, errCode XlErrorCode) {
	(*xlErr)(unsafe.Pointer(a)).typ = TypeErr
	(*xlErr)(unsafe.Pointer(a)).val = errCode
}

func newError(errCode XlErrorCode) *xlErr {
	return &xlErr{
		val: errCode,
		typ: TypeErr,
	}
}

func ViewError(ptr unsafe.Pointer) (*xlErr, error) {
	if ptr == nil {
		return nil, ErrInvalid
	}
	ret := (*xlErr)(ptr)
	if ret.typ != TypeErr {
		return nil, fmt.Errorf("not an error type: %v", ret.typ)
	}
	return ret, nil
}

var (
	NullErr        = newError(XlErrNull)
	Div0Err        = newError(XlErrDiv0)
	ValueErr       = newError(XlErrValue)
	RefErr         = newError(XlErrRef)
	NameErr        = newError(XlErrName)
	NumErr         = newError(XlErrNum)
	NAErr          = newError(XlErrNA)
	GettingDataErr = newError(XlErrGettingData)
)
