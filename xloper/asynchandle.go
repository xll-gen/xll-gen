package xloper

import (
	"runtime"
	"unsafe"
)

// compiler hints
func _() {
	var x [1]struct{}
	var _ XLOPER = (*AsyncHandle)(nil)
	var _ = x[XlOper12Size-unsafe.Sizeof(AsyncHandle{})]
	var _ = x[XlTypeOffset-unsafe.Offsetof(AsyncHandle{}.typ)]
}

// AsyncHandle represents an xltypeBigData XLOPER. This type is used by Excel
// for two main purposes:
//  1. As a return value from an asynchronous User-Defined Function (UDF) to
//     provide a handle before the final result is ready.
//  2. To pass large binary data chunks between Excel and the XLL.
//
// The memory for the data pointed to by the handle is managed by Excel.
type AsyncHandle struct {
	handle unsafe.Pointer
	cbData int32
	_      [XlTypeOffset - ptrSize - unsafe.Sizeof(int32(0))]byte
	typ    XlType
}

// Type returns the XLOPER type constant, which should be TypeBigData.
func (h *AsyncHandle) Type() XlType {
	return h.typ
}

// Handle returns the raw pointer to the Excel-managed data.
func (h *AsyncHandle) Handle() unsafe.Pointer {
	return h.handle
}

// Bytes returns the data pointed to by the handle as a Go byte slice.
// This is a read-only view of the Excel-managed memory.
func (h *AsyncHandle) Bytes() []byte {
	return unsafe.Slice((*byte)(h.handle), h.cbData)
}

// Value returns the underlying data as a byte slice wrapped in an `any` type.
func (h *AsyncHandle) Value() any {
	return h.Bytes()
}

// Pin is a no-op for AsyncHandle. The memory pointed to by the handle is
// managed by Excel, not the Go garbage collector, so no pinning is required.
func (h *AsyncHandle) Pin(p *runtime.Pinner) {
	// no-op: The handle's memory is managed by Excel.
}

// ViewAsyncHandle creates an AsyncHandle view from a pointer to an XLOPER12 struct.
// It returns an error if the pointer is nil or if the XLOPER is not of type TypeBigData.
func ViewAsyncHandle(ptr unsafe.Pointer) (*AsyncHandle, error) {
	if ptr == nil {
		return nil, ErrInvalid
	}
	h := (*AsyncHandle)(ptr)
	if h.typ.Base() != TypeBigData {
		return nil, ErrInvalid
	}
	return h, nil
}
