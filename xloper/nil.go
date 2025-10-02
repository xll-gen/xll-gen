package xloper

import (
	"runtime"
	"unsafe"
)

var _ XLOPER = (*nilType)(nil)
var Nil *nilType

func init() {
	Nil = NewNil()
}

// nilType represents an XLOPER with TypeNil.
type nilType struct {
	_   [XlTypeOffset]byte
	typ XlType
}

// Type returns the XLOPER type for Nil.
func (n *nilType) Type() XlType {
	return TypeNil
}

// Value returns nil.
func (n *nilType) Value() any {
	return nil
}

func (n *nilType) String() string {
	return "nil"
}

// Pin is a no-op for NilInstance as it's a global singleton.
func (n *nilType) Pin(p runtime.Pinner) {
	// No-op
}

func NewNil() *nilType {
	return Nil
}

func SetNil(a *Any) {
	(*nilType)(unsafe.Pointer(a)).typ = TypeNil
}
