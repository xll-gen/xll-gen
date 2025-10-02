package excel

import (
	"reflect"
	"testing"

	"github.com/xll-gen/xll-gen/xloper"
)

func BenchmarkCtoa(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = Ctoa(16383) // "XFD"
	}
}

func BenchmarkCtoa_Single(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = Ctoa(0) // "A"
	}
}

func BenchmarkCtoaNaive(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = CtoaNaive(16383) // "XFD"
	}
}

func BenchmarkCtoaNaive_Single(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = CtoaNaive(0) // "A"
	}
}

func BenchmarkAtoc(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = Atoc("XFD")
	}
}

func BenchmarkAtoc_Single(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = Atoc("A")
	}
}

func TestCtoa(t *testing.T) {
	testCases := []struct {
		name    string
		col     int32
		want    string
		wantErr error
	}{
		{"First column", 0, "A", nil},
		{"Last single-letter column", 25, "Z", nil},
		{"First double-letter column", 26, "AA", nil},
		{"Middle column", 701, "ZZ", nil},
		{"First triple-letter column", 702, "AAA", nil},
		{"Max column", 16383, "XFD", nil},
		{"Negative column", -1, "", xloper.ErrOutOfBounds},
		{"Out of bounds column", 16384, "", xloper.ErrOutOfBounds},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Ctoa(tc.col)
			if err != tc.wantErr {
				t.Errorf("Ctoa(%d) error = %v, wantErr %v", tc.col, err, tc.wantErr)
				return
			}
			if got != tc.want {
				t.Errorf("Ctoa(%d) = %q, want %q", tc.col, got, tc.want)
			}
		})
	}
}

func TestAtoc(t *testing.T) {
	testCases := []struct {
		name    string
		col     string
		want    int32
		wantErr error
	}{
		{"First column uppercase", "A", 0, nil},
		{"First column lowercase", "a", 0, nil},
		{"Last single-letter column", "Z", 25, nil},
		{"First double-letter column", "AA", 26, nil},
		{"Double-letter lowercase", "aa", 26, nil},
		{"Middle column", "ZZ", 701, nil},
		{"First triple-letter column", "AAA", 702, nil},
		{"Max column uppercase", "XFD", 16383, nil},
		{"Max column lowercase", "xfd", 16383, nil},
		{"Out of bounds", "XFE", -1, xloper.ErrOutOfBounds},
		{"Empty string", "", -1, ErrInvalidColumnName},
		{"Invalid character number", "A1", -1, ErrInvalidColumnName},
		{"Invalid character symbol", "A@", -1, ErrInvalidColumnName},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Atoc(tc.col)
			if err != tc.wantErr {
				t.Errorf("Atoc(%q) error = %v, wantErr %v", tc.col, err, tc.wantErr)
				return
			}
			if got != tc.want {
				t.Errorf("Atoc(%q) = %d, want %d", tc.col, got, tc.want)
			}
		})
	}
}

func TestRange_String(t *testing.T) {
	testCases := []struct {
		name string
		r    Range
		want string
	}{
		{
			name: "Single cell with sheet",
			r:    NewRange("Sheet1", []xloper.XLREF{{RowFirst: 0, RowLast: 0, ColFirst: 0, ColLast: 0}}),
			want: "Sheet1!A1",
		},
		{
			name: "Single cell without sheet",
			r:    NewRange("", []xloper.XLREF{{RowFirst: 4, RowLast: 4, ColFirst: 2, ColLast: 2}}),
			want: "C5",
		},
		{
			name: "Multi-cell range",
			r:    NewRange("Sheet1", []xloper.XLREF{{RowFirst: 1, RowLast: 9, ColFirst: 1, ColLast: 3}}),
			want: "Sheet1!B2:D10",
		},
		{
			name: "Multi-area range",
			r:    NewRange("Sheet2", []xloper.XLREF{{RowFirst: 0, RowLast: 0, ColFirst: 0, ColLast: 0}, {RowFirst: 2, RowLast: 2, ColFirst: 3, ColLast: 3}}),
			want: "Sheet2!A1,Sheet2!D3",
		},
		{
			name: "Quoted sheet name",
			r:    NewRange("My Sheet", []xloper.XLREF{{RowFirst: 0, RowLast: 0, ColFirst: 0, ColLast: 0}}),
			want: "'My Sheet'!A1",
		},
		{
			name: "Empty range",
			r:    NewRange("Sheet1", []xloper.XLREF{}),
			want: "",
		},
		{
			name: "Invalid column",
			r:    NewRange("Sheet1", []xloper.XLREF{{RowFirst: 0, RowLast: 0, ColFirst: -1, ColLast: 0}}),
			want: xloper.RefErr.String(),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.String(); got != tc.want {
				t.Errorf("Range.String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRange_Getters(t *testing.T) {
	ref1 := xloper.XLREF{RowFirst: 0, RowLast: 1, ColFirst: 2, ColLast: 3}
	ref2 := xloper.XLREF{RowFirst: 4, RowLast: 5, ColFirst: 6, ColLast: 7}

	t.Run("XLREF", func(t *testing.T) {
		r := NewRange("S", []xloper.XLREF{ref1, ref2})
		if got := r.XLREF(); got != ref1 {
			t.Errorf("XLREF() = %v, want %v", got, ref1)
		}
	})

	t.Run("XLREF empty", func(t *testing.T) {
		r := NewRange("S", []xloper.XLREF{})
		if got := r.XLREF(); got != (xloper.XLREF{}) {
			t.Errorf("XLREF() on empty range = %v, want empty struct", got)
		}
	})

	t.Run("XLREFs", func(t *testing.T) {
		refs := []xloper.XLREF{ref1, ref2}
		r := NewRange("S", refs)
		if got := r.XLREFs(); !reflect.DeepEqual(got, refs) {
			t.Errorf("XLREFs() = %v, want %v", got, refs)
		}
	})
}

func TestRange_First(t *testing.T) {
	ref1 := xloper.XLREF{RowFirst: 0, RowLast: 1, ColFirst: 2, ColLast: 3}
	ref2 := xloper.XLREF{RowFirst: 4, RowLast: 5, ColFirst: 6, ColLast: 7}
	r := NewRange("MySheet", []xloper.XLREF{ref1, ref2})

	first := r.First()

	if first.SheetName != "MySheet" {
		t.Errorf("Expected sheet name %q, got %q", "MySheet", first.SheetName)
	}
	if len(first.xlRefs) != 1 {
		t.Fatalf("Expected 1 ref, got %d", len(first.xlRefs))
	}
	if first.xlRefs[0] != ref1 {
		t.Errorf("Expected first ref %v, got %v", ref1, first.xlRefs[0])
	}
}

func Test_quoteSheetName(t *testing.T) {
	testCases := []struct {
		name  string
		input string
		want  string
	}{
		{"Simple", "Sheet1", "Sheet1"},
		{"With space", "My Sheet", "'My Sheet'"},
		{"With single quote", "O'Malley", "'O''Malley'"},
		{"With brackets", "Sheet[1]", "'Sheet[1]'"},
		{"With special chars", "Test-Sheet!", "'Test-Sheet!'"},
		{"Empty", "", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quoteSheetName(tc.input); got != tc.want {
				t.Errorf("quoteSheetName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// CtoaNaive is a less optimized version of Ctoa for benchmarking purposes.
// It uses slice prepending which leads to multiple allocations.
func CtoaNaive(col int32) (string, error) {
	if col < 0 || col >= 16384 {
		return "", xloper.ErrOutOfBounds
	}
	var result []byte
	n := col + 1
	for n > 0 {
		rem := (n - 1) % 26
		result = append([]byte{byte('A' + rem)}, result...)
		n = (n - 1) / 26
	}
	return string(result), nil
}
