package xloper

import (
	"runtime"
	"unsafe"
)

var _ XLOPER = (*Any)(nil)

type Any struct {
	buf [XlTypeOffset]byte
	typ XlType
}

func (a *Any) Type() XlType {
	return a.typ
}

// Set sets the value of the Any struct from a Go value.
func (a *Any) Set(v any) {
	switch v := v.(type) {
	case string:
		SetString(a, v)
	case int:
		if v < -2147483648 || v > 2147483647 {
			SetNumber(a, float64(v))
		} else {
			SetInt32(a, int32(v))
		}
	case int64:
		SetNumber(a, float64(v))
	case float32:
		SetNumber(a, float64(v))
	case int32:
		SetInt32(a, v)
	case float64:
		SetNumber(a, v)
	case bool:
		SetBool(a, v)
	case nil:
		SetNil(a)
	case XlErrorCode:
		SetError(a, v)
	default:
		SetError(a, XlErrValue)
	}
}

// New creates a new specific XLOPER instance (e.g., *String, *Number) from a
// given Go value. It returns an XLOPER interface, which can be useful when the
// exact type is not known at compile time.
func New(v any) (XLOPER, error) {
	switch v := v.(type) {
	case string:
		return NewString(v), nil
	case int:
		if v < -2147483648 || v > 2147483647 {
			return NewNumber(float64(v)), nil
		} else {
			return NewInt32(int32(v)), nil
		}
	case int64:
		// float64 can represent all int64 values exactly
		return NewNumber(float64(v)), nil
	case float32:
		return NewNumber(float64(v)), nil
	case int32:
		return NewInt32(v), nil
	case float64:
		return NewNumber(v), nil
	case bool:
		return NewBool(v), nil
	case nil:
		return Nil, nil
	case XlErrorCode:
		return newError(v), nil
	case [][]any:
		return NewMulti(v)
	default:
		return nil, ErrInvalid
	}
}

// View inspects an XLOPER12 at the given pointer and returns the corresponding xloper object.
// Returns Nil if ptr is nil or if the type is not supported.
func View(ptr unsafe.Pointer) XLOPER {
	if ptr == nil { // If the pointer is nil, return the Nil singleton.
		return Nil // Nil is a singleton instance that implements xloper.XLOPER.
	}

	// Cast the pointer to an Any type to safely read the XLOPER type.
	typ := (*Any)(ptr).Type().Base()

	// Call the appropriate View function based on the type.
	switch typ {
	case TypeStr:
		if op, err := ViewString(ptr); err == nil {
			return op
		}
	case TypeInt:
		if op, err := ViewInt32(ptr); err == nil {
			return op
		}
	case TypeNum:
		if op, err := ViewNumber(ptr); err == nil {
			return op
		}
	case TypeBool:
		if op, err := ViewBool(ptr); err == nil {
			return op
		}
	case TypeErr:
		if op, err := ViewError(ptr); err == nil {
			return op
		}
	case TypeMulti:
		if op, err := ViewMulti(ptr); err == nil {
			return op
		}
	}

	// Return Nil for TypeNil, TypeMissing, or any other unhandled types.
	return Nil
}

// Value decodes the XLOPER and returns its content as a standard Go `any` type.
// It achieves this by viewing the `Any` struct as a specific XLOPER type and then
// calling the `Value()` method on that specific type.
func (a *Any) Value() any {
	return View(unsafe.Pointer(a)).Value()
}

// Pin delegates the pinning operation to the specific XLOPER type. This is
// necessary to ensure that any Go-managed memory associated with the XLOPER
// (like the buffer for a string or a multi) is not moved by the garbage collector.
func (a *Any) Pin(p *runtime.Pinner) {
	View(unsafe.Pointer(a)).Pin(p)
}

func NewEmpty() *Any {
	return &Any{}
}
