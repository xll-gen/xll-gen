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
	var _ = x[XlOper12Size-unsafe.Sizeof(Multi{})]
	var _ = x[XlTypeOffset-unsafe.Offsetof(Multi{}.typ)]
}

type Array2D = array2d.Array2D[Any]

// Multi represents an xltypeMulti XLOPER, which is an array of other XLOPERs.
type Multi struct {
	// ptr points to the first element of the array in memory.
	// rows and cols define the dimensions of the array.
	// arraySlice is a Go-managed slice that holds the actual XLOPER data,
	// ensuring it is not garbage collected while in use.
	ptr        *Any
	rows       int32
	cols       int32
	arraySlice *[]Any
	_          [XlTypeOffset - ptrSize*2 - unsafe.Sizeof(int32(0))*2]byte
	typ        XlType
}

// NewMulti creates a new Multi XLOPER from a 2D slice of Go values. It handles
// the conversion of each Go value into its corresponding XLOPER type and arranges
// them in a contiguous memory block suitable for Excel.
func NewMulti(data [][]any) (*Multi, error) {
	res := &Multi{}
	err := res.Set(data)
	return res, err
}

// Set populates the Multi XLOPER from a 2D slice of Go values. It allocates
// the necessary memory and converts each element into an XLOPER. If the rows
// in the input slice have different lengths, they are padded with `xltypeNil`
// to form a rectangular array.
func (m *Multi) Set(data [][]any) error {

	rows := len(data)
	if rows == 0 {
		return fmt.Errorf("data for multi must not be empty")
	}

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
		rowSlice, ok := arr.Row(r)
		if !ok {
			return fmt.Errorf("failed to get row %d from array", r)
		}
		for c, cellData := range rowData {
			rowSlice[c].Set(cellData)
		}
	}

	firstRow, _ := arr.Row(0)

	m.ptr = &firstRow[0]
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

// Slices returns the content of the Multi as a 2D slice of any.
func (m *Multi) Slices() [][]any {
	rows := m.Rows()
	cols := m.Cols()
	arrSlice := unsafe.Slice(m.ptr, rows*cols)
	arr2d, _ := array2d.FromSlice(rows, cols, arrSlice)
	res, _ := array2d.Map(arr2d, func(a Any) (any, error) { return a.Value(), nil })
	return res.ToSlices()
}

// Value returns the content of the Multi as a 2D slice of any.
func (m *Multi) Value() any {
	return m.Slices()
}

// String provides a string representation of the Multi.
func (m *Multi) String() string {
	return fmt.Sprintf("[Multi %dx%d] %v", m.Rows(), m.Cols(), m.Value())
}

// Ptr returns a pointer to the first element of the Multi array.
func (m *Multi) Ptr() *Any {
	return (*Any)(unsafe.Pointer(m.ptr))
}

// Pin prevents the garbage collector from moving the Multi struct and its internal buffer.
func (m *Multi) Pin(p runtime.Pinner) {
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
