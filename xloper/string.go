package xloper

import (
	"runtime"
	"unsafe"
)

// compiler hints
func _() {
	var x [1]struct{}
	var _ XLOPER = (*String)(nil)
	var _ = x[XlOper12Size-unsafe.Sizeof(String{})]
	var _ = x[XlTypeOffset-unsafe.Offsetof(String{}.typ)]
}

const MaxStringLength = 32767

type PascalString []uint16

func NewPascalString(s string) PascalString {
	if len(s) > MaxStringLength {
		s = s[:MaxStringLength]
	}
	buf := make([]uint16, len(s)+1)
	// Unsafe write to the slice, using the full capacity
	encodedBuf := encodeWTF16(s, buf[1:1:cap(buf)])
	// Set the final length of the slice, and the first element will hold the length of the string in characters.
	buf[0] = uint16(len(encodedBuf))

	return buf
}

func (ps PascalString) String() string {
	if len(ps) == 0 {
		return ""
	}
	strLen := int(ps[0])
	if strLen == 0 {
		return ""
	}
	resBuf := decodeWTF16(ps[1:1+strLen], make([]byte, 0, strLen*3))
	return *(*string)(unsafe.Pointer(&resBuf))
}

func (ps PascalString) Ptr() *uint16 {
	if len(ps) == 0 {
		return nil
	}
	return &ps[0]
}

type String struct {
	// XLOPER12 layout for string
	ptr *uint16
	// Go-managed buffer to keep it alive. This field is not part of the XLOPER12 memory layout.
	stringBuf *PascalString
	_         [XlTypeOffset - ptrSize*2]byte
	typ       XlType
}

func (s *String) Type() XlType {
	return s.typ
}

func (s *String) String() string {
	if s.ptr == nil {
		return ""
	}
	// The first element of the buffer is the length.
	strLen := int((*s.stringBuf)[0])
	if strLen == 0 {
		return ""
	}
	// Decode the string part of the buffer (after the length prefix).
	resBuf := decodeWTF16((*s.stringBuf)[1:1+strLen], make([]byte, 0, strLen*3))
	return *(*string)(unsafe.Pointer(&resBuf))
}

func (s *String) Value() any {
	return s.String()
}

func (s *String) Pin(p runtime.Pinner) {
	p.Pin(s)
	if s.stringBuf != nil {
		p.Pin(s.stringBuf)
	}
}

func SetString(a *Any, val string) {
	strBuf := NewPascalString(val)
	s := (*String)(unsafe.Pointer(a))
	s.typ = TypeStr
	s.ptr = &strBuf[0]
	s.stringBuf = &strBuf
}

func ViewString(ptr unsafe.Pointer) (*String, error) {
	if ptr == nil {
		return nil, ErrInvalid
	}

	s := (*String)(ptr)

	if s.typ.Base() != TypeStr {
		return nil, ErrInvalid
	}

	return s, nil
}

func NewString(s string) *String {
	strBuf := NewPascalString(s)
	return &String{
		ptr:       strBuf.Ptr(),
		typ:       TypeStr,
		stringBuf: &strBuf,
	}
}
