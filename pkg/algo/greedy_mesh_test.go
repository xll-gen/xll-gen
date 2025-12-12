package algo

import (
	"reflect"
	"sort"
	"testing"
)

func TestGreedyMesh(t *testing.T) {
	tests := []struct {
		name     string
		cells    []Cell
		expected []Rect
	}{
		{
			name:     "Empty",
			cells:    []Cell{},
			expected: nil,
		},
		{
			name: "Single Cell",
			cells: []Cell{
				{Row: 1, Col: 1},
			},
			expected: []Rect{
				{RowFirst: 1, RowLast: 1, ColFirst: 1, ColLast: 1},
			},
		},
		{
			name: "Horizontal Line",
			cells: []Cell{
				{Row: 1, Col: 1},
				{Row: 1, Col: 2},
				{Row: 1, Col: 3},
			},
			expected: []Rect{
				{RowFirst: 1, RowLast: 1, ColFirst: 1, ColLast: 3},
			},
		},
		{
			name: "Vertical Line",
			cells: []Cell{
				{Row: 1, Col: 1},
				{Row: 2, Col: 1},
				{Row: 3, Col: 1},
			},
			expected: []Rect{
				{RowFirst: 1, RowLast: 3, ColFirst: 1, ColLast: 1},
			},
		},
		{
			name: "Square Block 2x2",
			cells: []Cell{
				{Row: 1, Col: 1}, {Row: 1, Col: 2},
				{Row: 2, Col: 1}, {Row: 2, Col: 2},
			},
			expected: []Rect{
				{RowFirst: 1, RowLast: 2, ColFirst: 1, ColLast: 2},
			},
		},
		{
			name: "Rect 3x2", // 3 Rows, 2 Cols
			cells: []Cell{
				{Row: 1, Col: 1}, {Row: 1, Col: 2},
				{Row: 2, Col: 1}, {Row: 2, Col: 2},
				{Row: 3, Col: 1}, {Row: 3, Col: 2},
			},
			expected: []Rect{
				{RowFirst: 1, RowLast: 3, ColFirst: 1, ColLast: 2},
			},
		},
		{
			name: "Disjoint Cells",
			cells: []Cell{
				{Row: 1, Col: 1},
				{Row: 5, Col: 5},
			},
			expected: []Rect{
				{RowFirst: 1, RowLast: 1, ColFirst: 1, ColLast: 1},
				{RowFirst: 5, RowLast: 5, ColFirst: 5, ColLast: 5},
			},
		},
		{
			name: "L-Shape (Greedy prefers width first)",
			// 1 1
			// 1 .
			cells: []Cell{
				{Row: 1, Col: 1}, {Row: 1, Col: 2},
				{Row: 2, Col: 1},
			},
			expected: []Rect{
				// Row 1 is merged first because it comes first in sort order and width expansion happens first
				{RowFirst: 1, RowLast: 1, ColFirst: 1, ColLast: 2},
				{RowFirst: 2, RowLast: 2, ColFirst: 1, ColLast: 1},
			},
		},
		{
			name: "Complex Shape",
			// 1 1 1
			// 1 1 .
			// 1 . .
			cells: []Cell{
				{Row: 1, Col: 1}, {Row: 1, Col: 2}, {Row: 1, Col: 3},
				{Row: 2, Col: 1}, {Row: 2, Col: 2},
				{Row: 3, Col: 1},
			},
			expected: []Rect{
				// Row 1 (1-3)
				{RowFirst: 1, RowLast: 1, ColFirst: 1, ColLast: 3},
				// Row 2 (1-2)
				{RowFirst: 2, RowLast: 2, ColFirst: 1, ColLast: 2},
				// Row 3 (1-1)
				{RowFirst: 3, RowLast: 3, ColFirst: 1, ColLast: 1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GreedyMesh(tt.cells)

			// Sort expected and got for deterministic comparison, although algorithm should be deterministic
			sortRects(got)
			sortRects(tt.expected)

			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("GreedyMesh() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func sortRects(rects []Rect) {
	sort.Slice(rects, func(i, j int) bool {
		if rects[i].RowFirst != rects[j].RowFirst {
			return rects[i].RowFirst < rects[j].RowFirst
		}
		return rects[i].ColFirst < rects[j].ColFirst
	})
}
