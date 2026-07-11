package cmd

import "testing"

// TestParseVersion pins the defensive version parser: it must tolerate the "go"
// prefix Go's `go version` emits, a leading "v", and trailing pre-release junk,
// and report ok=false for anything without a numeric version.
func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want []int
		ok   bool
	}{
		{"go1.24.3", []int{1, 24, 3}, true},
		{"1.24", []int{1, 24}, true},
		{"v3.24", []int{3, 24}, true},
		{"3.24.1", []int{3, 24, 1}, true},
		{"3.24.1-rc2", []int{3, 24, 1}, true},
		{"cmake version 3.30.2", []int{3, 30, 2}, true},
		{"", nil, false},
		{"unknown", nil, false},
	}
	for _, c := range cases {
		got, ok := parseVersion(c.in)
		if ok != c.ok {
			t.Errorf("parseVersion(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("parseVersion(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseVersion(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

// TestVersionAtLeast pins the doctor version-gating decision for Go (>=1.24) and
// CMake (>=3.24), including the "unparseable degrades, does not FAIL" contract.
func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		got, min   string
		wantOK     bool
		wantParsed bool
	}{
		// Go gating (min 1.24).
		{"go1.24.3", minGoVersion, true, true},
		{"go1.24", minGoVersion, true, true},
		{"go1.25.0", minGoVersion, true, true},
		{"go1.23.9", minGoVersion, false, true},
		{"go1.21", minGoVersion, false, true},
		// CMake gating (min 3.24).
		{"3.24.0", minCMakeVersion, true, true},
		{"3.30.2", minCMakeVersion, true, true},
		{"3.23.5", minCMakeVersion, false, true},
		{"2.8.12", minCMakeVersion, false, true},
		// Unparseable version -> parsed=false (caller warns, does not fail).
		{"unknown", minGoVersion, false, false},
		{"", minCMakeVersion, false, false},
	}
	for _, c := range cases {
		ok, parsed := versionAtLeast(c.got, c.min)
		if ok != c.wantOK || parsed != c.wantParsed {
			t.Errorf("versionAtLeast(%q, %q) = (ok=%v, parsed=%v), want (ok=%v, parsed=%v)",
				c.got, c.min, ok, parsed, c.wantOK, c.wantParsed)
		}
	}
}

// TestCompareVersions covers component-wise comparison with unequal lengths.
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b []int
		want int
	}{
		{[]int{1, 24}, []int{1, 24}, 0},
		{[]int{1, 24, 0}, []int{1, 24}, 0}, // trailing zero == missing
		{[]int{1, 24, 1}, []int{1, 24}, 1},
		{[]int{1, 23}, []int{1, 24}, -1},
		{[]int{2}, []int{1, 99}, 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%v, %v) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
