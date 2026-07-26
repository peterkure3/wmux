// Layout tree for `wmux tui` (Phase 3 of wmux-tui-refactor-plan.md): a
// binary split tree that computes each pane's rect on resize. This is
// the thing wt.exe was silently doing for `wmux pane`/`wmux grid` today
// — deciding where each pane goes and how big it is — reimplemented here
// as ~150 lines this package fully owns, instead of a chain of wt.exe
// split-pane invocations.
//
// Pure geometry and tree editing only: no bubbletea, no daemon calls, no
// terminal I/O. That's deliberate — it makes the whole thing trivially
// unit-testable, and it's the same boundary cmux-tui draws between
// layout and rendering.
package layout

import "math"

// SplitDir is a split's orientation, named after the resulting layout —
// "right" (children side-by-side) or "down" (children stacked) — the
// same vocabulary `wmux pane --split` already uses, not wt.exe's -V/-H
// (which names the *divider's* orientation and reads backwards to most
// people; see gridSplitArgs's comment for the specific confusion this
// avoids repeating).
type SplitDir int

const (
	SplitRight SplitDir = iota // A left, B right
	SplitDown                  // A top, B bottom
)

// Node is one layout tree node: a Leaf (SessionID set, A/B nil) holding
// one session's pane, or a Split (SessionID empty, A/B both set) dividing
// its rect between two children. The tree shape directly mirrors the
// screen layout — an in-order-ish traversal of leaves is left-to-right,
// top-to-bottom reading order.
type Node struct {
	SessionID string // "" for a Split node

	Dir   SplitDir
	Ratio float64 // A's share of the split, (0,1); <=0 or >=1 normalizes to 0.5
	A, B  *Node
}

// Rect is a pane's position and size in terminal cells.
type Rect struct {
	X, Y, W, H int
}

// NewLeaf builds a single-pane tree.
func NewLeaf(sessionID string) *Node {
	return &Node{SessionID: sessionID}
}

// Layout computes every leaf's rect within bounds.
func (n *Node) Layout(bounds Rect) map[string]Rect {
	out := make(map[string]Rect)
	n.layout(bounds, out)
	return out
}

func (n *Node) layout(r Rect, out map[string]Rect) {
	if n == nil {
		return
	}
	if n.SessionID != "" {
		out[n.SessionID] = r
		return
	}
	ratio := n.Ratio
	if ratio <= 0 || ratio >= 1 {
		ratio = 0.5
	}
	switch n.Dir {
	case SplitDown:
		ah := int(math.Round(float64(r.H) * ratio))
		n.A.layout(Rect{r.X, r.Y, r.W, ah}, out)
		n.B.layout(Rect{r.X, r.Y + ah, r.W, r.H - ah}, out)
	default: // SplitRight
		aw := int(math.Round(float64(r.W) * ratio))
		n.A.layout(Rect{r.X, r.Y, aw, r.H}, out)
		n.B.layout(Rect{r.X + aw, r.Y, r.W - aw, r.H}, out)
	}
}

// Leaves returns every session ID in the tree, in reading order
// (left-to-right, top-to-bottom) — the order Tab/Shift-Tab focus cycling
// uses.
func (n *Node) Leaves() []string {
	if n == nil {
		return nil
	}
	if n.SessionID != "" {
		return []string{n.SessionID}
	}
	return append(n.A.Leaves(), n.B.Leaves()...)
}

// Has reports whether id is a leaf anywhere in the tree — the cheap
// pre-check callers need before aiming a SplitLeaf at a leaf that may
// have been closed since they picked it.
func (n *Node) Has(id string) bool {
	if n == nil {
		return false
	}
	if n.SessionID != "" {
		return n.SessionID == id
	}
	return n.A.Has(id) || n.B.Has(id)
}

// findParent locates the leaf with the given ID and returns its parent
// plus which side (true = A, false = B) it's on. Returns (nil, false,
// false) if id is the root leaf or isn't found.
func (n *Node) findParent(id string) (parent *Node, isA, found bool) {
	if n == nil || n.SessionID != "" {
		return nil, false, false
	}
	if n.A.SessionID == id {
		return n, true, true
	}
	if n.B.SessionID == id {
		return n, false, true
	}
	if p, a, ok := n.A.findParent(id); ok {
		return p, a, true
	}
	return n.B.findParent(id)
}

// SplitLeaf splits the pane holding targetID, replacing that leaf with a
// new Split whose A is the existing pane and whose B is a fresh leaf for
// newID. Returns the new root (unchanged unless targetID was the root
// leaf) and false if targetID isn't in the tree.
func (root *Node) SplitLeaf(targetID string, dir SplitDir, newID string, ratio float64) (*Node, bool) {
	if root == nil {
		return nil, false
	}
	if root.SessionID == targetID {
		return &Node{Dir: dir, Ratio: ratio, A: NewLeaf(targetID), B: NewLeaf(newID)}, true
	}
	parent, isA, found := root.findParent(targetID)
	if !found {
		return root, false
	}
	replacement := &Node{Dir: dir, Ratio: ratio, A: NewLeaf(targetID), B: NewLeaf(newID)}
	if isA {
		parent.A = replacement
	} else {
		parent.B = replacement
	}
	return root, true
}

// RemoveLeaf removes the pane holding id, collapsing its parent split
// into its sibling (the sibling takes over the parent's position in the
// tree, so the freed space merges into whichever pane was next to it).
// Returns the new root (nil if id was the only pane) and false if id
// isn't in the tree.
func (root *Node) RemoveLeaf(id string) (*Node, bool) {
	if root == nil {
		return nil, false
	}
	if root.SessionID == id {
		return nil, true // removing the only pane empties the layout
	}
	parent, isA, found := root.findParent(id)
	if !found {
		return root, false
	}
	sibling := parent.A
	if isA {
		sibling = parent.B
	}
	if parent == root {
		return sibling, true
	}
	grandparent, parentIsA, _ := root.findParentNode(parent)
	if grandparent == nil {
		return sibling, true // shouldn't happen once parent != root, but stay safe
	}
	if parentIsA {
		grandparent.A = sibling
	} else {
		grandparent.B = sibling
	}
	return root, true
}

// findParentNode locates the parent of a specific *Node (by identity,
// not by leaf ID) — RemoveLeaf's own helper, since the node being
// replaced is itself a Split, not a Leaf, so findParent's ID-based
// lookup doesn't apply.
func (n *Node) findParentNode(target *Node) (parent *Node, isA, found bool) {
	if n == nil || n.SessionID != "" {
		return nil, false, false
	}
	if n.A == target {
		return n, true, true
	}
	if n.B == target {
		return n, false, true
	}
	if p, a, ok := n.A.findParentNode(target); ok {
		return p, a, true
	}
	return n.B.findParentNode(target)
}

// NeighborInDirection finds the leaf geometrically nearest fromID in the
// given screen direction ("left", "right", "up", "down"), using each
// leaf's already-computed rect — the layout-tree replacement for `wmux
// focus --dir`'s wt.exe move-focus. Returns "" if fromID has no rect or
// nothing lies in that direction.
//
// "Nearest" means: every leaf whose rect starts strictly beyond fromID's
// edge in the given direction is a candidate; among those, the one
// closest along that axis wins, ties broken by the least perpendicular
// center-to-center distance (so pressing "down" from a wide top pane
// lands on whichever pane below is actually under the cursor, not
// whichever happens to sort first).
func NeighborInDirection(rects map[string]Rect, fromID, dir string) string {
	from, ok := rects[fromID]
	if !ok {
		return ""
	}
	fromCX, fromCY := from.X+from.W/2, from.Y+from.H/2

	best, bestPrimary, bestSecondary := "", math.MaxInt, math.MaxInt
	for id, r := range rects {
		if id == fromID {
			continue
		}
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		var primary, secondary int
		switch dir {
		case "left":
			if r.X+r.W > from.X {
				continue
			}
			primary, secondary = from.X-(r.X+r.W), abs(cy-fromCY)
		case "right":
			if r.X < from.X+from.W {
				continue
			}
			primary, secondary = r.X-(from.X+from.W), abs(cy-fromCY)
		case "up":
			if r.Y+r.H > from.Y {
				continue
			}
			primary, secondary = from.Y-(r.Y+r.H), abs(cx-fromCX)
		case "down":
			if r.Y < from.Y+from.H {
				continue
			}
			primary, secondary = r.Y-(from.Y+from.H), abs(cx-fromCX)
		default:
			return ""
		}
		if primary < bestPrimary || (primary == bestPrimary && secondary < bestSecondary) {
			best, bestPrimary, bestSecondary = id, primary, secondary
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
