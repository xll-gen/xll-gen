package generator

import (
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

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
