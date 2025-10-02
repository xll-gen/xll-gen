package excel

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/xll-gen/xll-gen/xloper"
)

// Range represents a single rectangular area on a worksheet (e.g., "Sheet1!A1:B10").
type Range struct {
	SheetName string
	xlRefs    []xloper.XLREF
}

func (r Range) First() Range {
	return Range{
		SheetName: r.SheetName,
		xlRefs:    r.xlRefs[:1],
	}
}

func (r Range) String() string {
	var sb strings.Builder
	count := len(r.xlRefs)
	if count == 0 {
		return ""
	}

	for i, xr := range r.xlRefs {
		if r.SheetName != "" {
			sb.WriteString(quoteSheetName(r.SheetName))
			sb.WriteRune('!')
		}

		// Top-left cell
		colFirst, err := Ctoa(xr.ColFirst)
		if err != nil {
			return xloper.RefErr.String()
		}
		sb.WriteString(colFirst)
		sb.WriteString(strconv.FormatInt(int64(xr.RowFirst+1), 10))

		// If it's a multi-cell range, add the bottom-right cell.
		// A single-cell range has RowFirst == RowLast and ColFirst == ColLast.
		if xr.RowFirst != xr.RowLast || xr.ColFirst != xr.ColLast {
			sb.WriteRune(':')
			colLast, err := Ctoa(xr.ColLast)
			if err != nil {
				return xloper.RefErr.String()
			}
			sb.WriteString(colLast)
			sb.WriteString(strconv.FormatInt(int64(xr.RowLast+1), 10))
		}

		if i < count-1 {
			sb.WriteRune(',')
		}
	}
	return sb.String()
}

func (r Range) XLREF() xloper.XLREF {
	if len(r.xlRefs) == 0 {
		return xloper.XLREF{}
	}
	return r.xlRefs[0]
}

func (r Range) XLREFs() []xloper.XLREF {
	return r.xlRefs
}

func (r Range) WithExcel(e *Excel) RangeWithExcel {
	return RangeWithExcel{
		Range: r,
		excel: e,
	}
}

func NewRange(sheetName string, xlRefs []xloper.XLREF) Range {
	return Range{
		SheetName: sheetName,
		xlRefs:    xlRefs,
	}
}

type RangeWithExcel struct {
	Range
	excel *Excel
}

func (re RangeWithExcel) IdSheet() (uintptr, error) {
	var id uintptr
	var err error

	if re.SheetName == "" {
		id, err = re.excel.CurrentSheetId()
		if err != nil {
			return 0, fmt.Errorf("failed to get current sheet ID: %w", err)
		}
		return id, nil
	}

	id, err = re.excel.SheetId(re.SheetName)
	if err != nil {
		return 0, fmt.Errorf("failed to get sheet ID for range: %w, %v", err, re)
	}
	return id, nil
}

func (re RangeWithExcel) Mref() (*xloper.Mref, error) {
	if len(re.xlRefs) == 0 {
		return nil, xloper.ErrInvalid
	}
	idSheet, err := re.IdSheet()
	if err != nil {
		return nil, fmt.Errorf("failed to get sheet ID for range: %w, %v", err, re)
	}
	if idSheet == 0 {
		return nil, xloper.ErrInvalid
	}

	return xloper.NewMref(idSheet, re.xlRefs), nil
}

// ErrInvalidColumnName indicates an invalid Excel column name.
var ErrInvalidColumnName = errors.New("excel: invalid column name")

// Ctoa converts a 0-based column index to an Excel column name (e.g., 0 -> "A", 26 -> "AA").
func Ctoa(col int32) (string, error) {
	if col < 0 || col >= 16384 {
		return "", xloper.ErrOutOfBounds
	}
	var result []byte
	// Use 1-based index for calculation
	n := col + 1
	for n > 0 {
		// Find remainder
		rem := (n - 1) % 26
		// Prepend character to result
		result = append([]byte{byte('A' + rem)}, result...)
		// Update n with integer division
		n = (n - 1) / 26
	}
	return string(result), nil
}

// Atoc converts an Excel column name to a 0-based column index (e.g., "A" -> 0, "AA" -> 26).
// This function is case-insensitive.
func Atoc(col string) (int32, error) {
	if col == "" {
		return -1, ErrInvalidColumnName
	}
	var result int32
	for _, r := range col {
		char := r
		// Convert to uppercase
		if 'a' <= char && char <= 'z' {
			char = char - 'a' + 'A'
		}
		if char < 'A' || char > 'Z' {
			return -1, ErrInvalidColumnName // Invalid character in column name
		}
		result = result*26 + int32(char-'A'+1)
	}
	// The result is 1-based, so convert to 0-based.
	colIndex := result - 1
	if colIndex >= 16384 {
		return -1, xloper.ErrOutOfBounds
	}
	return colIndex, nil
}

// quoteSheetName adds single quotes around a sheet name if required by Excel's A1 notation.
func quoteSheetName(name string) string {
	if name == "" {
		return ""
	}
	// Excel requires quotes for names that contain spaces, non-alphanumeric characters,
	// or could be mistaken for a cell reference. A simple check for spaces or single
	// quotes (which need escaping) handles most common cases.
	if strings.ContainsAny(name, " '[]*?/:\\") {
		// Existing single quotes are escaped by doubling them.
		return "'" + strings.ReplaceAll(name, "'", "''") + "'"
	}
	return name
}
