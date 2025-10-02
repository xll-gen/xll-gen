package xloper

import (
	"fmt"
	"runtime"
	"unsafe"
)

// compiler hints
func _() {
	var x [1]struct{}

	var _ XLOPER = (*Sref)(nil)
	var _ Ref = (*Sref)(nil)
	var _ = x[XlOper12Size-unsafe.Sizeof(Sref{})]
	var _ = x[XlTypeOffset-unsafe.Offsetof(Sref{}.typ)]

	var _ XLOPER = (*Mref)(nil)
	var _ Ref = (*Mref)(nil)
	var _ = x[XlOper12Size-unsafe.Sizeof(Mref{})]
	var _ = x[XlTypeOffset-unsafe.Offsetof(Mref{}.typ)]
}

type Ref interface {
	IdSheet() uintptr
	XLREF() XLREF
	Mref() (*Mref, error)
	String() string
}

// XLREF corresponds to the XLREF12 structure, defining the coordinates of a
// single rectangular block of cells. The coordinates are 0-indexed.
type XLREF struct {
	RowFirst int32
	RowLast  int32
	ColFirst int32
	ColLast  int32
}

// BoundsCheck verifies if the reference coordinates are within Excel's valid grid limits.
// Excel's grid is 1,048,576 rows by 16,384 columns.
func (r XLREF) BoundsCheck() error {
	if r.RowFirst < 0 || r.RowLast < 0 || r.ColFirst < 0 || r.ColLast < 0 {
		return ErrInvalid
	}
	if r.RowFirst >= 1048576 || r.RowLast >= 1048576 || r.ColFirst >= 16384 || r.ColLast >= 16384 {
		return ErrOutOfBounds
	}
	return nil
}

// Sref represents an xltypeSRef XLOPER, which is a simple reference to a
// single rectangular area on the currently active sheet.
type Sref struct {
	count uint16
	ref   XLREF
	_     [XlTypeOffset - unsafe.Sizeof(XLREF{}) - 4]byte
	typ   XlType
	_     [4]byte
}

// Type returns the XLOPER type constant, which is TypeSRef.
func (s *Sref) Type() XlType {
	return s.typ
}

// IdSheet returns 0, as Sref does not contain sheet ID information.
func (m *Sref) IdSheet() uintptr {
	return 0
}

// FirstRef returns the first XLREF in the Sref, which is the only one.
func (s *Sref) XLREF() XLREF {
	return s.ref
}

// Ref returns a pointer to the Sref struct itself.
func (s *Sref) Mref() (*Mref, error) {
	return NewMref(0, []XLREF{s.ref}), nil
}

// Value returns the Sref struct itself as an `any` type.
func (s *Sref) Value() any {
	return s
}

func (s *Sref) String() string {
	return fmt.Sprintf("Sref %+v", s.ref)
}

// Pin prevents the garbage collector from moving the Sref struct.
func (s *Sref) Pin(p runtime.Pinner) {
	p.Pin(s)
}

// SetSref populates a generic Any XLOPER with the data for a simple reference (Sref).
// It validates the reference bounds before setting the data.
func SetSref(a *Any, ref XLREF) error {
	if err := ref.BoundsCheck(); err != nil {
		return err
	}
	s := (*Sref)(unsafe.Pointer(a))
	s.typ = TypeSRef
	s.ref = ref
	s.count = 1 // always 1
	return nil
}

// NewSref creates a new Sref XLOPER for the given 0-indexed coordinates.
// It returns nil if the coordinates are out of Excel's valid bounds.
func NewSref(ref XLREF) *Sref {
	if err := ref.BoundsCheck(); err != nil {
		return nil
	}
	return &Sref{
		ref:   ref,
		typ:   TypeSRef,
		count: 1, // always 1
	}
}

// ViewSref creates an Sref view from a pointer to an XLOPER12 struct.
// It returns an error if the pointer is nil or the XLOPER is not of type TypeSRef.
func ViewSref(ptr unsafe.Pointer) (*Sref, error) {
	if ptr == nil {
		return nil, ErrInvalid
	}
	s := (*Sref)(ptr)
	if s.typ.Base() != TypeSRef {
		return nil, ErrInvalid
	}
	if err := s.ref.BoundsCheck(); err != nil {
		return nil, err
	}
	return s, nil
}

// XLMREF corresponds to the XLMREF12 structure. It contains a count of
// references followed by a variable-size array of XLREF12 structures.
type XLMREF []byte

// Ptr returns a pointer to the underlying XLMREF data.
func (m *XLMREF) Ptr() *uint16 {
	if len(*m) == 0 {
		return nil
	}
	return (*uint16)(unsafe.Pointer(&(*m)[0]))
}

// Count returns the number of XLREF structures in the XLMREF.
func (m *XLMREF) Count() uint16 {
	if len(*m) < 2 {
		return 0
	}
	return *(*uint16)(unsafe.Pointer(&(*m)[0]))
}

// setCount sets the number of XLREF structures in the XLMREF.
func (m *XLMREF) setCount(count uint16) {
	if len(*m) < 2 {
		return
	}
	*(*uint16)(unsafe.Pointer(&(*m)[0])) = count
}

// Refs safely slices the variable-size array of XLREF structures from the XLMREF.
func (m *XLMREF) Refs() []XLREF {
	cnt := m.Count()
	if cnt == 0 {
		return nil
	}
	return unsafe.Slice((*XLREF)(unsafe.Pointer(&(*m)[2])), int(cnt))
}

// NewXLMREF creates a new XLMREF byte slice from a slice of XLREF structures.
// This is a helper for creating the C-compatible memory layout for Mref.
func NewXLMREF(refs []XLREF) *XLMREF {
	if len(refs) == 0 {
		return nil
	}
	count := len(refs)
	if count > 32767 {
		refs = refs[:32767]
		count = 32767
	}
	xlMref := make(XLMREF, 2+count*int(unsafe.Sizeof(XLREF{})))
	xlMref.setCount(uint16(count))
	refTblSlice := unsafe.Slice((*XLREF)(unsafe.Pointer(&xlMref[2])), count)
	copy(refTblSlice, refs)
	return &xlMref
}

// Mref is the Go representation of an xltypeRef XLOPER, which represents a
// reference to one or more rectangular areas on a single sheet.
type Mref struct {
	// ptr is a pointer to the count-prefixed array of XLMREF12 structures.
	// idSheet is the ID of the sheet this reference belongs to.
	// mrefBuf is a Go-managed buffer to keep the data pointed to by ptr alive,
	// preventing it from being garbage collected. It is not part of the XLOPER12 layout.
	ptr     *uint16 // Pointer to the count-prefixed array of XLMREF12s
	idSheet uintptr
	mrefBuf *XLMREF // Managed buffer to keep it alive, not part of XLOPER12 layout
	_       [XlTypeOffset - 3*ptrSize]byte
	typ     XlType
}

// Type returns the XLOPER type constant, which is TypeRef.
func (m *Mref) Type() XlType {
	return m.typ
}

// XLREF returns the first XLREF in the Mref.
func (m *Mref) XLREF() XLREF {
	if m.mrefBuf.Count() == 0 {
		return XLREF{}
	}
	return m.mrefBuf.Refs()[0]
}

// XLREFs returns the slice of XLREF structures defining the areas in the multi-reference.
func (m *Mref) XLREFs() []XLREF {
	if m.mrefBuf == nil {
		return nil
	}
	return m.mrefBuf.Refs()
}

// Ref returns a slice of XLREF structures defining the areas in the multi-reference.
func (m *Mref) Mref() (*Mref, error) {
	return m, nil
}

// Value returns the slice of XLREF coordinates as an `any` type.
func (m *Mref) Value() any {
	return m
}

func (m *Mref) String() string {
	return fmt.Sprintf("Mref %d, %+v", m.idSheet, m.XLREFs())
}

// Pin prevents the garbage collector from moving the Mref struct and its internal
// data buffer. This is crucial for passing a stable pointer to C code.
func (m *Mref) Pin(p runtime.Pinner) {
	p.Pin(m)
	if m.mrefBuf != nil {
		p.Pin(m.mrefBuf)
	}
}

// IdSheet returns the sheet ID that this reference belongs to.
func (m *Mref) IdSheet() uintptr {
	return m.idSheet
}

// Set populates the Mref struct with a sheet ID and a slice of XLREF coordinates.
// It allocates and manages the underlying memory buffer required by the XLMREF structure.
func (m *Mref) Set(idSheet uintptr, refs []XLREF) error {
	if idSheet == 0 {
		return ErrInvalid
	}

	if len(refs) == 0 {
		m.ptr = nil
		m.idSheet = idSheet
		m.typ = TypeRef
		return nil
	}

	count := len(refs)
	if count > 32767 {
		return ErrOutOfBounds
	}

	m.typ = TypeRef
	m.idSheet = idSheet
	m.mrefBuf = NewXLMREF(refs)
	m.ptr = m.mrefBuf.Ptr()
	return nil
}

// SetMref populates a generic Any XLOPER with the data for a multi-reference (Mref).
func SetMref(a *Any, idSheet uintptr, refs []XLREF) error {
	m := (*Mref)(unsafe.Pointer(a))
	return m.Set(idSheet, refs)
}

// NewMref creates a new Mref XLOPER from a sheet ID and a slice of XLREF coordinates.
// It returns nil if the number of references is zero or exceeds the Excel limit.
func NewMref(idsheet uintptr, refs []XLREF) *Mref {
	if len(refs) == 0 {
		return nil
	}
	count := len(refs)
	if count > 32767 {
		return nil
	}

	res := &Mref{
		typ: TypeRef,
	}
	_ = res.Set(idsheet, refs)
	return res
}

// ViewMref creates an Mref view from a pointer to an XLOPER12 struct.
// It returns an error if the pointer is nil or the XLOPER is not of type TypeRef.
func ViewMref(ptr unsafe.Pointer) (*Mref, error) {
	if ptr == nil {
		return nil, ErrInvalid
	}
	m := (*Mref)(ptr)
	if m.typ.Base() != TypeRef {
		return nil, ErrInvalid
	}
	return m, nil
}
