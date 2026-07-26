package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/peterkure3/wmux/internal/proto"
)

func TestSurfacePaneApplyReplayThenUpdate(t *testing.T) {
	p := &tuiSurfacePane{frames: make(chan proto.CellsFrame, 1)}
	p.applyFrame(proto.CellsFrame{
		Type: proto.CellsReplay, Cols: 10, RowCnt: 2,
		Rows: []proto.RowUpdate{
			{Y: 0, Runs: []proto.Run{{X: 0, Text: "hello     "}}},
			{Y: 1, Runs: []proto.Run{{X: 0, Text: "world     "}}},
		},
		Cursor: proto.Pos{X: 2, Y: 0}, Visible: true,
	})
	if p.cols != 10 || p.rows != 2 {
		t.Fatalf("size = %dx%d, want 10x2", p.cols, p.rows)
	}
	lines := p.render(10, 2, false)
	if !strings.Contains(lines[0], "hello") {
		t.Fatalf("row 0 = %q, want it to contain hello", lines[0])
	}
	if !strings.Contains(lines[1], "world") {
		t.Fatalf("row 1 = %q, want it to contain world", lines[1])
	}

	// An update touching only row 1 must leave row 0 alone.
	p.applyFrame(proto.CellsFrame{
		Type:   proto.CellsUpdate,
		Rows:   []proto.RowUpdate{{Y: 1, Runs: []proto.Run{{X: 0, Text: "changed!  "}}}},
		Cursor: proto.Pos{X: 0, Y: 1}, Visible: true,
	})
	lines = p.render(10, 2, false)
	if !strings.Contains(lines[0], "hello") {
		t.Fatalf("row 0 after unrelated update = %q, want it still to contain hello", lines[0])
	}
	if !strings.Contains(lines[1], "changed!") {
		t.Fatalf("row 1 after update = %q, want it to contain changed!", lines[1])
	}
}

func TestSurfacePaneExitFlag(t *testing.T) {
	p := &tuiSurfacePane{frames: make(chan proto.CellsFrame, 1)}
	if p.exited {
		t.Fatal("exited should start false")
	}
	p.applyFrame(proto.CellsFrame{Type: proto.CellsExit})
	if !p.exited {
		t.Fatal("exited should be true after a CellsExit frame")
	}
}

func TestSurfacePaneRenderPadsShortGrid(t *testing.T) {
	// A pane whose grid hasn't caught up to a resize yet (fewer rows
	// than requested) must not panic or return short lines.
	p := &tuiSurfacePane{grid: []proto.RowUpdate{{Y: 0, Runs: []proto.Run{{X: 0, Text: "hi"}}}}}
	lines := p.render(5, 3, false)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	for i, l := range lines {
		if len([]rune(l)) < 5 && !strings.Contains(l, "\x1b") {
			t.Errorf("line %d = %q, shorter than requested width with no styling to explain it", i, l)
		}
	}
}

// TestTuiViewRendersWithoutPanicAtVariousSizes exercises the full
// composite View() — sidebar alone, then with a surface pane split off
// it — at a few sizes, including ones small enough to hit contentSize's
// clamping.
func TestTuiViewRendersWithoutPanicAtVariousSizes(t *testing.T) {
	sizes := []struct{ w, h int }{{100, 40}, {40, 15}, {3, 3}, {0, 0}}
	for _, sz := range sizes {
		m := newTestTuiModel()
		m, _ = update(m, tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
		if out := m.View(); out == "" {
			t.Errorf("sidebar-only, size %dx%d: View() returned empty string", sz.w, sz.h)
		}

		m, _ = update(m, paneOpenedMsg{id: "agent1"})
		m.panes["agent1"].grid = []proto.RowUpdate{{Y: 0, Runs: []proto.Run{{X: 0, Text: "hi", FG: "#ff0000"}}}}
		if out := m.View(); out == "" {
			t.Errorf("sidebar+pane, size %dx%d: View() returned empty string", sz.w, sz.h)
		}
	}
}
