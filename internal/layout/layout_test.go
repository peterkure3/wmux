package layout

import "testing"

func TestLayoutSinglePaneFillsBounds(t *testing.T) {
	root := NewLeaf("a")
	rects := root.Layout(Rect{0, 0, 100, 40})
	if got := rects["a"]; got != (Rect{0, 0, 100, 40}) {
		t.Fatalf("rect = %+v, want the full bounds", got)
	}
}

// TestLayoutTwoPaneSideBySide matches wmux grid's former documented 2-pane layout
// ([0 | 1]) — the tree this package builds must reproduce what wt.exe
// used to lay out.
func TestLayoutTwoPaneSideBySide(t *testing.T) {
	root := &Node{Dir: SplitRight, A: NewLeaf("0"), B: NewLeaf("1")}
	rects := root.Layout(Rect{0, 0, 100, 40})

	a, b := rects["0"], rects["1"]
	if a.X != 0 || a.Y != 0 || a.H != 40 {
		t.Fatalf("pane 0 = %+v, want it to start at the origin and span full height", a)
	}
	if b.X != a.X+a.W || b.Y != 0 || b.H != 40 {
		t.Fatalf("pane 1 = %+v, want it immediately right of pane 0, full height", b)
	}
	if a.W+b.W != 100 {
		t.Fatalf("widths %d+%d = %d, want 100 (no gap, no overlap)", a.W, b.W, a.W+b.W)
	}
}

// TestLayoutFourPaneGrid matches wmux grid's former documented 4-pane layout:
//
//	[0 | 1]
//	[3 | 2]
func TestLayoutFourPaneGrid(t *testing.T) {
	root := &Node{
		Dir: SplitRight,
		A:   &Node{Dir: SplitDown, A: NewLeaf("0"), B: NewLeaf("3")},
		B:   &Node{Dir: SplitDown, A: NewLeaf("1"), B: NewLeaf("2")},
	}
	rects := root.Layout(Rect{0, 0, 100, 40})

	// Left column (0 over 3) directly above/below each other, same X/W.
	if rects["0"].X != rects["3"].X || rects["0"].W != rects["3"].W {
		t.Fatalf("0 and 3 should share X/W (same column): %+v vs %+v", rects["0"], rects["3"])
	}
	if rects["3"].Y != rects["0"].Y+rects["0"].H {
		t.Fatalf("3 should sit directly below 0: %+v then %+v", rects["0"], rects["3"])
	}
	// Right column (1 over 2), and it must start where the left column ends.
	if rects["1"].X != rects["0"].X+rects["0"].W {
		t.Fatalf("right column should start where the left column ends")
	}
	if rects["1"].X != rects["2"].X || rects["1"].W != rects["2"].W {
		t.Fatalf("1 and 2 should share X/W (same column): %+v vs %+v", rects["1"], rects["2"])
	}
	// Every leaf accounted for, no overlap: total area == bounds area.
	total := 0
	for _, r := range rects {
		total += r.W * r.H
	}
	if total != 100*40 {
		t.Fatalf("summed pane area = %d, want %d (100x40, no gaps/overlaps)", total, 100*40)
	}
}

func TestLayoutRatioSkewsSplit(t *testing.T) {
	root := &Node{Dir: SplitRight, Ratio: 0.8, A: NewLeaf("a"), B: NewLeaf("b")}
	rects := root.Layout(Rect{0, 0, 100, 40})
	if rects["a"].W != 80 {
		t.Fatalf("A width = %d, want 80 (0.8 of 100)", rects["a"].W)
	}
	if rects["b"].W != 20 {
		t.Fatalf("B width = %d, want 20", rects["b"].W)
	}
}

func TestLayoutInvalidRatioNormalizesToHalf(t *testing.T) {
	for _, ratio := range []float64{0, -1, 1, 2} {
		root := &Node{Dir: SplitRight, Ratio: ratio, A: NewLeaf("a"), B: NewLeaf("b")}
		rects := root.Layout(Rect{0, 0, 100, 40})
		if rects["a"].W != 50 {
			t.Errorf("ratio %v: A width = %d, want 50 (normalized to 0.5)", ratio, rects["a"].W)
		}
	}
}

func TestLeavesReadingOrder(t *testing.T) {
	root := &Node{
		Dir: SplitRight,
		A:   &Node{Dir: SplitDown, A: NewLeaf("0"), B: NewLeaf("3")},
		B:   &Node{Dir: SplitDown, A: NewLeaf("1"), B: NewLeaf("2")},
	}
	got := root.Leaves()
	want := []string{"0", "3", "1", "2"}
	if len(got) != len(want) {
		t.Fatalf("Leaves() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Leaves() = %v, want %v", got, want)
		}
	}
}

func TestSplitLeafOnRootLeaf(t *testing.T) {
	root := NewLeaf("a")
	root, ok := root.SplitLeaf("a", SplitRight, "b", 0.5)
	if !ok {
		t.Fatal("SplitLeaf returned false for the root leaf")
	}
	leaves := root.Leaves()
	if len(leaves) != 2 || leaves[0] != "a" || leaves[1] != "b" {
		t.Fatalf("Leaves() = %v, want [a b]", leaves)
	}
}

func TestSplitLeafOnNestedLeaf(t *testing.T) {
	root := &Node{Dir: SplitRight, A: NewLeaf("a"), B: NewLeaf("b")}
	root, ok := root.SplitLeaf("b", SplitDown, "c", 0.5)
	if !ok {
		t.Fatal("SplitLeaf returned false")
	}
	leaves := root.Leaves()
	want := []string{"a", "b", "c"}
	if len(leaves) != len(want) {
		t.Fatalf("Leaves() = %v, want %v", leaves, want)
	}
	for i := range want {
		if leaves[i] != want[i] {
			t.Fatalf("Leaves() = %v, want %v", leaves, want)
		}
	}
	// b and c must now be side by side within the space b used to own
	// alone — not overlapping a.
	rects := root.Layout(Rect{0, 0, 100, 40})
	if rects["a"].W != 50 {
		t.Fatalf("a's rect changed after splitting b: %+v", rects["a"])
	}
	if rects["b"].Y != rects["c"].Y-rects["b"].H {
		t.Fatalf("b and c should be stacked (SplitDown): b=%+v c=%+v", rects["b"], rects["c"])
	}
}

func TestSplitLeafUnknownTargetFails(t *testing.T) {
	root := NewLeaf("a")
	got, ok := root.SplitLeaf("nonexistent", SplitRight, "b", 0.5)
	if ok {
		t.Fatal("SplitLeaf succeeded against a session ID not in the tree")
	}
	if got != root {
		t.Fatal("SplitLeaf should return the tree unchanged on failure")
	}
}

func TestRemoveLeafOnlyPaneEmptiesLayout(t *testing.T) {
	root := NewLeaf("a")
	root, ok := root.RemoveLeaf("a")
	if !ok {
		t.Fatal("RemoveLeaf returned false")
	}
	if root != nil {
		t.Fatalf("root = %v, want nil (last pane removed)", root)
	}
}

func TestRemoveLeafPromotesSibling(t *testing.T) {
	root := &Node{Dir: SplitRight, A: NewLeaf("a"), B: NewLeaf("b")}
	root, ok := root.RemoveLeaf("a")
	if !ok {
		t.Fatal("RemoveLeaf returned false")
	}
	if root.SessionID != "b" {
		t.Fatalf("root = %+v, want the sole remaining leaf b", root)
	}
	rects := root.Layout(Rect{0, 0, 100, 40})
	if rects["b"] != (Rect{0, 0, 100, 40}) {
		t.Fatalf("b's rect = %+v, want the full bounds now that it's alone", rects["b"])
	}
}

// TestRemoveLeafDeepInTreeCollapsesCorrectly guards RemoveLeaf's use of
// findParentNode: removing a leaf whose parent is itself several levels
// below the root must reattach the sibling at the right point, not
// silently drop the rest of the tree.
func TestRemoveLeafDeepInTreeCollapsesCorrectly(t *testing.T) {
	// root -> [ a | (b | (c | d)) ]
	root := &Node{
		Dir: SplitRight,
		A:   NewLeaf("a"),
		B: &Node{
			Dir: SplitRight,
			A:   NewLeaf("b"),
			B:   &Node{Dir: SplitRight, A: NewLeaf("c"), B: NewLeaf("d")},
		},
	}
	root, ok := root.RemoveLeaf("c")
	if !ok {
		t.Fatal("RemoveLeaf returned false")
	}
	leaves := root.Leaves()
	want := map[string]bool{"a": true, "b": true, "d": true}
	if len(leaves) != 3 {
		t.Fatalf("Leaves() = %v, want exactly a, b, d", leaves)
	}
	for _, l := range leaves {
		if !want[l] {
			t.Fatalf("Leaves() = %v, unexpected member %q", leaves, l)
		}
	}
}

func TestRemoveLeafUnknownTargetFails(t *testing.T) {
	root := &Node{Dir: SplitRight, A: NewLeaf("a"), B: NewLeaf("b")}
	got, ok := root.RemoveLeaf("nonexistent")
	if ok {
		t.Fatal("RemoveLeaf succeeded against a session ID not in the tree")
	}
	if got != root {
		t.Fatal("RemoveLeaf should return the tree unchanged on failure")
	}
}

func TestNeighborInDirection(t *testing.T) {
	// [0 | 1]
	// [3 | 2]
	root := &Node{
		Dir: SplitRight,
		A:   &Node{Dir: SplitDown, A: NewLeaf("0"), B: NewLeaf("3")},
		B:   &Node{Dir: SplitDown, A: NewLeaf("1"), B: NewLeaf("2")},
	}
	rects := root.Layout(Rect{0, 0, 100, 40})

	cases := []struct {
		from, dir, want string
	}{
		{"0", "right", "1"},
		{"0", "down", "3"},
		{"1", "left", "0"},
		{"1", "down", "2"},
		{"2", "up", "1"},
		{"3", "up", "0"},
		{"0", "up", ""},   // nothing above the top row
		{"0", "left", ""}, // nothing left of the left column
	}
	for _, c := range cases {
		if got := NeighborInDirection(rects, c.from, c.dir); got != c.want {
			t.Errorf("NeighborInDirection(%s, %q) = %q, want %q", c.from, c.dir, got, c.want)
		}
	}
}

func TestNeighborInDirectionUnknownSession(t *testing.T) {
	rects := map[string]Rect{"a": {0, 0, 10, 10}}
	if got := NeighborInDirection(rects, "nonexistent", "right"); got != "" {
		t.Fatalf("NeighborInDirection with an unknown session = %q, want \"\"", got)
	}
}
