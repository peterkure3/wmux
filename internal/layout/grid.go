package layout

import "math"

// Grid builds a balanced grid over ids: a stack of equal-height rows,
// each row an equal-width chain of panes, in reading order.
//
// The column count is ceil(sqrt(n)), which gives the shapes people
// actually mean by "a grid of N": 2 side by side, 3 as two-over-one, 4 as
// a 2x2, 6 as 3x2. The last row is short when n isn't a perfect
// rectangle, and its panes share that row's full width rather than
// leaving a hole.
//
// Returns nil for an empty id list.
func Grid(ids []string) *Node {
	if len(ids) == 0 {
		return nil
	}
	if len(ids) == 1 {
		return NewLeaf(ids[0])
	}
	cols := int(math.Ceil(math.Sqrt(float64(len(ids)))))

	var rows []*Node
	for start := 0; start < len(ids); start += cols {
		end := min(start+cols, len(ids))
		rows = append(rows, chain(ids[start:end], SplitRight))
	}
	return chain2(rows, SplitDown)
}

// chain splits ids evenly along dir, left-to-right. Each split's ratio is
// 1/k for the k panes remaining, so every pane in the chain ends up the
// same size regardless of nesting depth.
func chain(ids []string, dir SplitDir) *Node {
	nodes := make([]*Node, len(ids))
	for i, id := range ids {
		nodes[i] = NewLeaf(id)
	}
	return chain2(nodes, dir)
}

func chain2(nodes []*Node, dir SplitDir) *Node {
	if len(nodes) == 0 {
		return nil
	}
	if len(nodes) == 1 {
		return nodes[0]
	}
	return &Node{
		Dir:   dir,
		Ratio: 1 / float64(len(nodes)),
		A:     nodes[0],
		B:     chain2(nodes[1:], dir),
	}
}
