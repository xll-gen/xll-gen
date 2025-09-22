package xloper

import (
	"runtime"
	"unsafe"
)

// Ensure Number implements the XLOPER interface.
var _ XLOPER = (*Number)(nil)

// Number represents an XLOPER with a float64 value (TypeNum).
type Number struct {
	val float64
	_   [XlTypeOffset - unsafe.Sizeof(float64(0))]byte
	typ XlType
}

// Type returns the XLOPER type for Number.
func (n *Number) Type() XlType {
	return n.typ
}

func (n *Number) Set(val float64) {
	n.val = val
}

// Float64 returns the float64 value of the Number.
func (n *Number) Float64() float64 {
	return n.val
}

// Value returns the float64 value as an any type.
func (n *Number) Value() any {
	return n.Float64()
}

func (n *Number) Pin(p *runtime.Pinner) {
	p.Pin(n)
}

// NewNumber creates a new Number XLOPER from a float64 value.
func NewNumber(val float64) *Number {
	return &Number{
		val: val,
		typ: TypeNum,
	}
}

func SetNumber(a *Any, val float64) {
	(*Number)(unsafe.Pointer(a)).typ = TypeNum
	(*Number)(unsafe.Pointer(a)).val = val
}

// ViewNumber creates a Number view from a pointer to an XLOPER12 struct.
func ViewNumber(ptr unsafe.Pointer) (ret *Number, err error) {
	if ptr == nil {
		err = ErrInvalid
		return
	}
	ret = (*Number)(ptr)
	if (*Number)(unsafe.Pointer(ptr)).typ != TypeNum {
		err = ErrInvalid
		return
	}
	return
}

// Ensure Integer implements the XLOPER interface.
var _ XLOPER = (*Int32)(nil)

// Int32 represents an XLOPER with an int32 value (TypeInt).
type Int32 struct {
	val int32
	_   [XlTypeOffset - unsafe.Sizeof(int32(0))]byte
	typ XlType
}

// Type returns the XLOPER type for Integer.
func (i *Int32) Type() XlType {
	return i.typ
}

// Int32 returns the int32 value of the Integer.
func (i *Int32) Int32() int32 {
	return i.val
}

// Value returns the int32 value as an any type.
func (i *Int32) Value() any {
	return i.Int32()
}

func (i *Int32) Pin(p *runtime.Pinner) {
	p.Pin(i)
}

func SetInt32(a *Any, val int32) {
	(*Int32)(unsafe.Pointer(a)).typ = TypeInt
	(*Int32)(unsafe.Pointer(a)).val = val
}

// NewInt32 creates a new Integer XLOPER from an int32 value.
func NewInt32(val int32) *Int32 {
	return &Int32{
		val: val,
		typ: TypeInt,
	}
}

// ViewInt32 creates an Integer view from a pointer to an XLOPER12 struct.
func ViewInt32(ptr unsafe.Pointer) (*Int32, error) {
	if ptr == nil {
		return nil, ErrInvalid
	}
	ret := (*Int32)(ptr)
	// The type check is important for safety when viewing external memory.
	if ret.typ != TypeInt {
		return nil, ErrInvalid
	}
	return ret, nil
}

// Ensure Bool implements the XLOPER interface.
var _ XLOPER = (*Bool)(nil)

// Bool represents an XLOPER with a bool value (TypeBool).
type Bool struct {
	val int32 // Excel uses a 32-bit integer for booleans in XLOPER12
	_   [XlTypeOffset - unsafe.Sizeof(int32(0))]byte
	typ XlType
}

// Type returns the XLOPER type for Bool.
func (b *Bool) Type() XlType {
	return b.typ
}

// Bool returns the bool value of the Bool XLOPER.
func (b *Bool) Bool() bool {
	return b.val != 0
}

// Value returns the bool value as an any type.
func (b *Bool) Value() any {
	return b.Bool()
}

func (b *Bool) Pin(p *runtime.Pinner) {
	p.Pin(b)
}

var (
	True  = NewBool(true)
	False = NewBool(false)
)

func SetBool(a *Any, val bool) {
	(*Bool)(unsafe.Pointer(a)).typ = TypeBool
	if val {
		(*Bool)(unsafe.Pointer(a)).val = 1
	} else {
		(*Bool)(unsafe.Pointer(a)).val = 0
	}
}

// NewBool creates a new Bool XLOPER from a bool value.
func NewBool(val bool) *Bool {
	var intVal int32
	if val {
		intVal = 1
	}
	return &Bool{
		val: intVal,
		typ: TypeBool,
	}
}

// ViewBool creates a Bool view from a pointer to an XLOPER12 struct.
func ViewBool(ptr unsafe.Pointer) (*Bool, error) {
	if ptr == nil {
		return nil, ErrInvalid
	}
	ret := (*Bool)(ptr)
	// The type check is important for safety when viewing external memory.
	if ret.typ != TypeBool {
		return nil, ErrInvalid
	}
	return ret, nil
}
