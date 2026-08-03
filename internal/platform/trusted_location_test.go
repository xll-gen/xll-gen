//go:build windows

package platform

import "testing"

// TestPathCoveredByTrustedEntry pins the decision that `doctor`'s
// "Excel trusted location" check rests on.
//
// WHY THIS MATTERS MORE THAN IT LOOKS. This check REPLACED a path-length warning
// that named the wrong cause: a 2026-08-03 2x2 measured a 31-character path
// outside a trusted location failing to load and a 168-character path inside one
// loading. The old warning's failure mode was a FALSE GREEN — it printed
// "✔ Project path 25 characters" from a directory where the XLL provably would
// not load. So the direction that must never break here is the same one: saying
// "covered" when Excel does not agree. The naive prefix compare
// (strings.HasPrefix without a separator) does exactly that, which is why
// foo/foobar is a case below rather than a comment.
func TestPathCoveredByTrustedEntry(t *testing.T) {
	cases := []struct {
		name       string
		entry      string
		allowSub   bool
		dir        string
		wantCovers bool
	}{
		{"exact match", `C:\dev\proj`, false, `C:\dev\proj`, true},
		{"exact match is case-insensitive", `C:\Dev\Proj`, false, `c:\dev\proj`, true},
		{"trailing separator on the entry still matches exactly", `C:\dev\proj\`, false, `C:\dev\proj`, true},

		{"subfolder needs AllowSubfolders", `C:\dev`, false, `C:\dev\proj`, false},
		{"subfolder with AllowSubfolders", `C:\dev`, true, `C:\dev\proj`, true},
		{"deep subfolder with AllowSubfolders", `C:\dev`, true, `C:\dev\a\b\c\d`, true},
		{"subfolder match is case-insensitive", `C:\Dev`, true, `c:\dev\proj`, true},
		{"entry with trailing separator covers subfolders", `C:\dev\`, true, `C:\dev\proj`, true},

		// THE TRAP: a sibling whose name merely starts with the entry's name.
		// A separator-blind prefix compare answers true here and hands back a
		// false green — the exact defect class this check exists to remove.
		{"sibling sharing a name prefix is NOT covered", `C:\foo`, true, `C:\foobar`, false},
		{"sibling sharing a name prefix, deeper", `C:\foo`, true, `C:\foobar\baz`, false},
		{"unrelated path", `C:\dev`, true, `D:\other`, false},
		{"parent of the entry is not covered", `C:\dev\proj`, true, `C:\dev`, false},

		{"empty entry covers nothing", "", true, `C:\dev`, false},
		{"empty dir is covered by nothing", `C:\dev`, true, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathCoveredByTrustedEntry(tc.entry, tc.allowSub, tc.dir); got != tc.wantCovers {
				t.Errorf("pathCoveredByTrustedEntry(%q, allowSub=%v, %q) = %v, want %v",
					tc.entry, tc.allowSub, tc.dir, got, tc.wantCovers)
			}
		})
	}
}

// TestExcelTrustedLocationForRejectsGarbage checks the exported wrapper survives
// input it cannot resolve. It deliberately asserts nothing about whether a real
// entry is found: that depends on the developer's own Trust Center, so asserting
// a hit would make the test pass or fail on machine configuration rather than on
// this code.
func TestExcelTrustedLocationForRejectsGarbage(t *testing.T) {
	if _, ok := ExcelTrustedLocationFor(""); ok {
		t.Error(`ExcelTrustedLocationFor("") reported a trusted location`)
	}
	if _, ok := ExcelTrustedLocationFor(`\\?\nonexistent-volume\nope`); ok {
		t.Error("ExcelTrustedLocationFor reported a trusted location for a nonexistent volume")
	}
}
