package generator

import (
	"fmt"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// TestCppWideLiteral pins the wide-string-literal escaping used for custom
// loading_placeholder text: quotes/backslashes/controls get C escapes, and
// non-ASCII runes become universal character names so the literal compiles
// independently of source encoding or wide execution charset.
func TestCppWideLiteral(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ascii", "Loading...", `L"Loading..."`},
		{"quote_and_backslash", `a"b\c`, `L"a\"b\\c"`},
		{"tab", "tab\there", `L"tab\there"`},
		{"newline", "a\nb", `L"a\nb"`},
		// Korean 로딩 (U+B85C, U+B529) -> 4-digit universal character names.
		{"bmp_unicode", string([]rune{0xB85C, 0xB529}), fmt.Sprintf(`L"\u%04X\u%04X"`, 0xB85C, 0xB529)},
		// Supplementary-plane rune (emoji) -> 8-digit \U form.
		{"astral_unicode", string(rune(0x1F600)), fmt.Sprintf(`L"\U%08X"`, 0x1F600)},
	}
	for _, tc := range cases {
		if got := cppWideLiteral(tc.in); got != tc.want {
			t.Errorf("%s: cppWideLiteral(%q) = %s, want %s", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestEscapeCppString pins the escaping used for config free-text (Description,
// Category, HelpTopic, Arg.Description) emitted inside an L"..." literal. The
// regression of record: an embedded control char (notably NUL) must NOT pass
// through raw, or it would truncate the generated C string.
func TestEscapeCppString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ascii", "Computes the sum", "Computes the sum"},
		{"quote_and_backslash", `a"b\c`, `a\"b\\c`},
		{"newline_cr_tab", "a\n\r\tb", `a\n\r\tb`},
		{"embedded_nul", "a\x00b", fmt.Sprintf(`a\u%04Xb`, 0)},
		{"other_control", "a\x01\x1fb", fmt.Sprintf(`a\u%04X\u%04Xb`, 1, 0x1f)},
		// Legitimate non-ASCII text passes through unchanged (cppWideLiteral, by
		// contrast, escapes it — escapeCppString preserves established behavior).
		{"unicode_passthrough", "데이터", "데이터"},
	}
	for _, tc := range cases {
		if got := escapeCppString(tc.in); got != tc.want {
			t.Errorf("%s: escapeCppString(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestAnyRtdOnceGrid covers the gating helper used by later template tasks to
// decide whether to emit the rtd-once grid-spill C++/Go codegen. It is true
// only when a function is BOTH mode:"rtd-once" AND returns grid/numgrid.
func TestAnyRtdOnceGrid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		fns  []config.Function
		want bool
	}{
		{
			name: "empty",
			fns:  nil,
			want: false,
		},
		{
			name: "rtd-once grid",
			fns:  []config.Function{{Name: "G", Mode: "rtd-once", Return: "grid"}},
			want: true,
		},
		{
			name: "rtd-once numgrid",
			fns:  []config.Function{{Name: "NG", Mode: "rtd-once", Return: "numgrid"}},
			want: true,
		},
		{
			name: "rtd-once scalar return",
			fns:  []config.Function{{Name: "S", Mode: "rtd-once", Return: "float"}},
			want: false,
		},
		{
			name: "sync grid return",
			fns:  []config.Function{{Name: "SG", Mode: "sync", Return: "grid"}},
			want: false,
		},
		{
			name: "async numgrid return",
			fns:  []config.Function{{Name: "AG", Mode: "async", Return: "numgrid", Async: true}},
			want: false,
		},
		{
			name: "mixed: one rtd-once grid among others",
			fns: []config.Function{
				{Name: "S", Mode: "rtd-once", Return: "float"},
				{Name: "SG", Mode: "sync", Return: "grid"},
				{Name: "G", Mode: "rtd-once", Return: "numgrid"},
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := anyRtdOnceGrid(tc.fns); got != tc.want {
				t.Errorf("anyRtdOnceGrid(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
