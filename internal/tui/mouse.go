// Mouse routing: clicking a pane focuses it, and events inside the
// sidebar reach the sidebar's own hit-testing.
//
// Every leaf's rect is already known (m.rects, the same values the
// daemon-side pty was resized to), so hit-testing is a containment
// check against those — no separate geometry to keep in sync.
package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/peterkure3/wmux/internal/layout"
)

func (m tuiModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	id, ok := m.leafAt(msg.X, msg.Y)
	if !ok {
		return m, nil
	}

	// A press anywhere in a pane moves focus there, and drops command
	// mode: the click already said which pane the user means.
	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
		m.focused = id
		m.mode = modeInsert
	}

	// Wheel and clicks inside the sidebar are the sidebar's — it scrolls
	// its list and focuses the session under the cursor. Coordinates are
	// translated to be relative to the sidebar's own content area
	// (inside its border), which is the frame its View() and bodyLines()
	// hit-test are drawn in.
	if id == sidebarLeafID {
		r := m.rects[sidebarLeafID]
		local := msg
		local.X = msg.X - r.X - 1
		local.Y = msg.Y - r.Y - 1
		return m, m.updateSidebarMsg(local)
	}
	return m, nil
}

// leafAt returns the pane whose rect contains screen cell (x, y).
func (m tuiModel) leafAt(x, y int) (string, bool) {
	for id, r := range m.rects {
		if contains(r, x, y) {
			return id, true
		}
	}
	return "", false
}

func contains(r layout.Rect, x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}
