package xloper

import (
	"fmt"
	"runtime"
	"slices"
	"unsafe"

	"github.com/xll-gen/array2d"
)

// compiler hints
func _() {
	var x [1]struct{}
	var _ XLOPER = (*Multi)(nil)
	var _ = x[32-unsafe.Sizeof(Multi{})]
	var _ = x[24-unsafe.Offsetof(Multi{}.typ)]
}

type Array2D = array2d.Array2D[Any]

// Multi represents an xltypeMulti XLOPER, which is an array of other XLOPERs.
type Multi struct {
	ptr        *Any
	rows       int32
	cols       int32
	arraySlice *[]Any
	_          [24 - ptrSize*2 - unsafe.Sizeof(int32(0))*2]byte
	typ        XlType
}

// NewMulti creates a new Multi XLOPER from a 2D slice of Go values.
func NewMulti(data [][]any) (*Multi, error) {
	res := &Multi{}
	err := res.Set(data)
	return res, err
}

// Set populates a Multi XLOPER from a 2D slice of Go values.
// Note: This implementation pads shorter rows to match the longest row.
func (m *Multi) Set(data [][]any) error {

	if len(data) == 0 {
		return fmt.Errorf("data for multi must not be empty")
	}

	rows := len(data)
	lenRows := make([]int, rows)
	for i, row := range data {
		lenRows[i] = len(row)
	}
	cols := slices.Max(lenRows)

	if cols == 0 {
		return fmt.Errorf("data for multi must not be empty")
	}

	arrSlice := make([]Any, rows*cols)
	arr, _ := array2d.FromSlice(rows, cols, arrSlice)
	for r, rowData := range data {
		rowSlice := arr.Row(r)
		for c, cellData := range rowData {
			rowSlice[c].Set(cellData)
		}
	}

	m.ptr = &arr.Row(0)[0]
	m.rows = int32(rows)
	m.cols = int32(cols)
	m.typ = TypeMulti
	m.arraySlice = &arrSlice
	return nil
}

// Type returns the XLOPER type constant.
func (m *Multi) Type() XlType {
	return m.typ
}

// Rows returns the number of rows in the array.
func (m *Multi) Rows() int {
	return int(m.rows)
}

// Cols returns the number of columns in the array.
func (m *Multi) Cols() int {
	return int(m.cols)
}

// Value returns the content of the Multi as a 2D slice of any.
func (m *Multi) Value() any {
	rows := m.Rows()
	cols := m.Cols()
	arrSlice := unsafe.Slice(m.ptr, rows*cols)
	arr2d, _ := array2d.FromSlice(rows, cols, arrSlice)
	res := make([][]any, rows)

	for r := 0; r < rows; r++ {
		res[r] = make([]any, cols)
		rowSlice := arr2d.Row(r)
		for c := range rowSlice {
			res[r][c] = rowSlice[c].Value()
		}
	}
	return res
}

// String provides a string representation of the Multi.
func (m *Multi) String() string {
	return fmt.Sprintf("[Multi %dx%d] %v", m.Rows(), m.Cols(), m.Value())
}

func (m *Multi) Ptr() unsafe.Pointer {
	return unsafe.Pointer(m.ptr)
}

// Pin prevents the garbage collector from moving the Multi struct and its internal buffer.
func (m *Multi) Pin(p *runtime.Pinner) {
	p.Pin(m)
	if m.arraySlice != nil {
		p.Pin(m.arraySlice)
	}
}

// ViewMulti creates a Multi view from a pointer to an XLOPER12 struct.
func ViewMulti(ptr unsafe.Pointer) (*Multi, error) {
	if ptr == nil {
		return nil, ErrInvalid
	}
	m := (*Multi)(ptr)

	if m.typ.Base() != TypeMulti {
		return nil, ErrInvalid
	}
	return m, nil
}
