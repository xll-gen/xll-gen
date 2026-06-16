#pragma once

// xll_greedy_mesh.h — header-only greedy-voxel (greedy-mesh) coalescing of a
// set of grid cells into a minimal-ish set of non-overlapping rectangles.
//
// This is a direct C++ port of pkg/algo/greedy_mesh.go (GreedyMesh). The
// algorithm semantics are IDENTICAL and are exercised by
// pkg/algo/greedy_mesh_test.go (TestGreedyMesh); this header keeps the same
// shape (sort by (row,col); expand width along cols first, then height along
// rows; visited set) so that Go test is the authoritative spec for the
// algorithm and this port only has to match it.
//
// WHY HERE / WHY HEADER-ONLY: the ONLY consumer is src/xll_date_format.cpp
// (the date auto-format calc-end drain), which greedy-meshes contiguous
// same-format date cells into rectangular blocks so each block is formatted
// with ONE COM Range.NumberFormat assignment instead of one per cell. The
// common case — a YDH-style single contiguous date COLUMN of ~21 cells with one
// identical format — collapses to a SINGLE rectangle / single format op. Fewer
// COM round-trips at calc-end also shrinks the reentrancy surface of the
// deferred-runner path.
//
// DEPENDENCY-FREE BY DESIGN: this header touches NO Excel/XLOPER types — just
// ints — so the logic is unit-testable in isolation (and the Go test covers it
// 1:1). The date-format .cpp maps (row,col) <-> XLREF12 at the call site.

#include <vector>
#include <algorithm>
#include <set>

namespace xll {
namespace mesh {

// MeshCell is a single (row,col) grid coordinate. Mirrors algo.Cell.
struct MeshCell {
    int row;
    int col;
};

// MeshRect is an inclusive rectangular region [rowFirst..rowLast] x
// [colFirst..colLast]. Mirrors algo.Rect.
struct MeshRect {
    int rowFirst;
    int rowLast;
    int colFirst;
    int colLast;
};

// GreedyMesh coalesces `cells` into non-overlapping rectangles.
//   1. Sort cells by (row, col).
//   2. For each unvisited cell, expand WIDTH along columns first, then expand
//      HEIGHT along rows while the full candidate row is present & unvisited.
//   3. Mark the rectangle's cells visited; continue.
// Cells not in the input naturally form holes, so a rectangle never spans a
// gap (e.g. an already-formatted cell excluded by the caller).
//
// Port note: matches GreedyMesh in pkg/algo/greedy_mesh.go exactly, including
// the width-first greedy bias (see the "L-Shape" / "Complex Shape" cases in
// the Go test). Returns an empty vector for empty input.
inline std::vector<MeshRect> GreedyMesh(std::vector<MeshCell> cells) {
    std::vector<MeshRect> rects;
    if (cells.empty()) {
        return rects;
    }

    // Sort top-left to bottom-right: row major, then col.
    std::sort(cells.begin(), cells.end(),
              [](const MeshCell& a, const MeshCell& b) {
                  if (a.row != b.row) return a.row < b.row;
                  return a.col < b.col;
              });

    // (row,col) presence lookup. std::set<pair> keeps the port dependency-free
    // (no hashing) and small — the date-format input is tens of cells, not
    // thousands.
    using Key = std::pair<int, int>;
    std::set<Key> grid;
    for (const auto& c : cells) {
        grid.insert(Key(c.row, c.col));
    }

    std::set<Key> visited;

    for (const auto& c : cells) {
        const Key ck(c.row, c.col);
        if (visited.count(ck)) {
            continue;
        }

        const int rFirst = c.row;
        const int cFirst = c.col;
        int rLast = rFirst;
        int cLast = cFirst;

        // Expand width (cols) along the first row.
        for (;;) {
            const int nextCol = cLast + 1;
            const Key nc(rFirst, nextCol);
            if (grid.count(nc) && !visited.count(nc)) {
                cLast = nextCol;
            } else {
                break;
            }
        }

        // Expand height (rows): a row joins only if EVERY column in
        // [cFirst..cLast] is present and unvisited.
        for (;;) {
            const int nextRow = rLast + 1;
            bool canExpand = true;
            for (int col = cFirst; col <= cLast; ++col) {
                const Key check(nextRow, col);
                if (!grid.count(check) || visited.count(check)) {
                    canExpand = false;
                    break;
                }
            }
            if (canExpand) {
                rLast = nextRow;
            } else {
                break;
            }
        }

        // Mark the rectangle's cells visited.
        for (int r = rFirst; r <= rLast; ++r) {
            for (int col = cFirst; col <= cLast; ++col) {
                visited.insert(Key(r, col));
            }
        }

        MeshRect rect;
        rect.rowFirst = rFirst;
        rect.rowLast = rLast;
        rect.colFirst = cFirst;
        rect.colLast = cLast;
        rects.push_back(rect);
    }

    return rects;
}

} // namespace mesh
} // namespace xll
