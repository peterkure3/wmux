package layout

import "testing"

// gridShape reports how many distinct rows and columns a grid's leaves
// occupy once laid out — the only thing "is this a grid?" actually means
// geometrically.
func gridShape(t *testing.T, ids []string, bounds Rect) (rows, cols int, rects map[string]Rect) {
	t.Helper()
	root := Grid(ids)
	if root == nil {
		t.Fatalf("Grid(%v) = nil", ids)
	}
	rects = root.Layout(bounds)
	if len(rects) != len(ids) {
		t.Fatalf("Grid(%v) laid out %d rects, want %d", ids, len(rects), len(ids))
	}
	rowSet, colSet := map[int]bool{}, map[int]bool{}
	for _, r := range rects {
		rowSet[r.Y] = true
		colSet[r.X] = true
	}
	return len(rowSet), len(colSet), rects
}

func TestGridShapes(t *testing.T) {
	bounds := Rect{X: 0, Y: 0, W: 120, H: 60}
	for _, tc := range []struct {
		name       string
		ids        []string
		rows, cols int
	}{
		{"one pane is one pane", []string{"a"}, 1, 1},
		{"two side by side", []string{"a", "b"}, 1, 2},
		{"three is two over one", []string{"a", "b", "c"}, 2, 2},
		{"four is 2x2", []string{"a", "b", "c", "d"}, 2, 2},
		{"six is 3x2", []string{"a", "b", "c", "d", "e", "f"}, 2, 3},
		{"nine is 3x3", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, 3, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, cols, _ := gridShape(t, tc.ids, bounds)
			if rows != tc.rows || cols != tc.cols {
				t.Fatalf("Grid(%v) is %d rows x %d cols, want %dx%d", tc.ids, rows, cols, tc.rows, tc.cols)
			}
		})
	}
}

func TestGridEmpty(t *testing.T) {
	if got := Grid(nil); got != nil {
		t.Fatalf("Grid(nil) = %+v, want nil", got)
	}
}

// TestGridTilesTheWholeArea is the property that matters for rendering:
// no gaps and no overlaps, or lipgloss's join would produce a ragged
// frame and the daemon-side pty sizes would not add up to the terminal.
func TestGridTilesTheWholeArea(t *testing.T) {
	bounds := Rect{X: 0, Y: 0, W: 100, H: 40}
	ids := []string{"a", "b", "c", "d", "e", "f"}
	_, _, rects := gridShape(t, ids, bounds)

	covered := map[[2]int]string{}
	for id, r := range rects {
		for y := r.Y; y < r.Y+r.H; y++ {
			for x := r.X; x < r.X+r.W; x++ {
				if other, dup := covered[[2]int{x, y}]; dup {
					t.Fatalf("cell (%d,%d) claimed by both %q and %q", x, y, other, id)
				}
				covered[[2]int{x, y}] = id
			}
		}
	}
	if len(covered) != bounds.W*bounds.H {
		t.Fatalf("grid covers %d cells, want the full %d", len(covered), bounds.W*bounds.H)
	}
}

// TestGridPanesAreEvenlySized: a "grid" whose panes differ wildly in
// size is a staircase of nested splits, which is exactly what chain2's
// 1/k ratio exists to avoid.
func TestGridPanesAreEvenlySized(t *testing.T) {
	_, _, rects := gridShape(t, []string{"a", "b", "c", "d"}, Rect{X: 0, Y: 0, W: 100, H: 40})
	for id, r := range rects {
		if r.W != 50 || r.H != 20 {
			t.Fatalf("pane %q is %dx%d, want an even 50x20 quarter", id, r.W, r.H)
		}
	}
}

func TestGridLeavesAreInReadingOrder(t *testing.T) {
	ids := []string{"a", "b", "c", "d"}
	got := Grid(ids).Leaves()
	for i, id := range ids {
		if got[i] != id {
			t.Fatalf("Grid(%v).Leaves() = %v, want the ids in order", ids, got)
		}
	}
}
