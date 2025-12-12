package algo

import (
	"sort"
)

// Cell represents a single cell coordinate in a grid.
// It uses int32 to align with standard generic grid representations.
type Cell struct {
	Row int32
	Col int32
}

// Rect represents a rectangular region defined by its boundaries.
type Rect struct {
	RowFirst int32
	RowLast  int32
	ColFirst int32
	ColLast  int32
}

// GreedyMesh optimizes a set of cells into the minimum number of non-overlapping rectangular regions.
// The algorithm uses a greedy approach:
// 1. Sorts cells by Row, then Col.
// 2. Iterates through unvisited cells to form the largest possible rectangle starting from that cell.
// 3. Marks cells as visited and continues until all cells are processed.
func GreedyMesh(cells []Cell) []Rect {
	if len(cells) == 0 {
		return nil
	}

	// Sort cells to process them in top-left to bottom-right order
	sort.Slice(cells, func(i, j int) bool {
		if cells[i].Row != cells[j].Row {
			return cells[i].Row < cells[j].Row
		}
		return cells[i].Col < cells[j].Col
	})

	// Create a lookup map for faster existence checks
	grid := make(map[Cell]bool, len(cells))
	for _, c := range cells {
		grid[c] = true
	}

	var rects []Rect
	visited := make(map[Cell]bool, len(cells))

	for _, c := range cells {
		if visited[c] {
			continue
		}

		rFirst, cFirst := c.Row, c.Col
		rLast, cLast := rFirst, cFirst

		// Expand Width (Cols)
		for {
			nextCol := cLast + 1
			nextCell := Cell{Row: rFirst, Col: nextCol}
			if grid[nextCell] && !visited[nextCell] {
				cLast = nextCol
			} else {
				break
			}
		}

		// Expand Height (Rows)
		for {
			nextRow := rLast + 1
			canExpand := true
			for col := cFirst; col <= cLast; col++ {
				checkCell := Cell{Row: nextRow, Col: col}
				if !grid[checkCell] || visited[checkCell] {
					canExpand = false
					break
				}
			}
			if canExpand {
				rLast = nextRow
			} else {
				break
			}
		}

		// Mark processed cells as visited
		for r := rFirst; r <= rLast; r++ {
			for c := cFirst; c <= cLast; c++ {
				visited[Cell{Row: r, Col: c}] = true
			}
		}

		rects = append(rects, Rect{
			RowFirst: rFirst,
			RowLast:  rLast,
			ColFirst: cFirst,
			ColLast:  cLast,
		})
	}
	return rects
}
