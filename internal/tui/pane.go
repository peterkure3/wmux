// Per-surface pane state for `wmux tui`: attaching over mode=cells,
// applying replay/update frames, and rendering the resulting grid with
// lipgloss — the rich-client half of Part 3 of the phased refactor plan
// (internal/daemon/surface.go's cellsframe machinery is the other half).
package tui

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/peterkure3/wmux/internal/proto"
)

// tuiSurfacePane holds one attached surface's known screen state, kept
// current by applyFrame as CellsFrames arrive on frames.
type tuiSurfacePane struct {
	id string

	cols, rows int
	grid       []proto.RowUpdate // grid[y].Runs is row y's current content
	cursor     proto.Pos
	cursorVis  bool

	exited bool

	frames chan proto.CellsFrame
}

// attachSurfaceCellsPane opens GET /surfaces/attach?mode=cells for id and
// starts a goroutine pumping decoded frames into the returned pane's
// channel until the stream ends (process exit, or the request itself
// failing to reach wmuxd, which is reported as an immediate CellsExit —
// there is nothing a pane can usefully show for "never connected" beyond
// "gone").
func attachSurfaceCellsPane(id string) *tuiSurfacePane {
	p := &tuiSurfacePane{id: id, frames: make(chan proto.CellsFrame, 64)}
	go func() {
		resp, err := daemonStream("/surfaces/attach?id=" + url.QueryEscape(id) + "&mode=cells")
		if err != nil {
			p.frames <- proto.CellsFrame{Type: proto.CellsExit}
			return
		}
		defer resp.Body.Close()
		dec := json.NewDecoder(resp.Body)
		for {
			var frame proto.CellsFrame
			if err := dec.Decode(&frame); err != nil {
				p.frames <- proto.CellsFrame{Type: proto.CellsExit}
				return
			}
			p.frames <- frame
			if frame.Type == proto.CellsExit {
				return
			}
		}
	}()
	return p
}

// applyFrame folds a newly received CellsFrame into the pane's state.
func (p *tuiSurfacePane) applyFrame(f proto.CellsFrame) {
	switch f.Type {
	case proto.CellsReplay:
		p.cols, p.rows = f.Cols, f.RowCnt
		p.grid = f.Rows
		p.cursor, p.cursorVis = f.Cursor, f.Visible
	case proto.CellsUpdate:
		for _, row := range f.Rows {
			if row.Y >= 0 && row.Y < len(p.grid) {
				p.grid[row.Y] = row
			}
		}
		p.cursor, p.cursorVis = f.Cursor, f.Visible
	case proto.CellsExit:
		p.exited = true
	}
}

// render draws exactly rows lines of exactly cols columns from the
// pane's current grid, styling each cell per its Run and (when focused)
// marking the cursor cell with reverse video — there is no way to place
// N independent terminal cursors from N composed sub-terminals, so a
// styled cell is the substitute every terminal multiplexer that draws
// its panes rather than piping raw bytes ends up using.
func (p *tuiSurfacePane) render(cols, rows int, focused bool) []string {
	lines := make([]string, rows)
	showCursor := focused && p.cursorVis
	for y := 0; y < rows; y++ {
		var b strings.Builder
		x := 0
		if y < len(p.grid) {
			for _, run := range p.grid[y].Runs {
				if x >= cols {
					break
				}
				style := runStyle(run)
				for _, r := range run.Text {
					if x >= cols {
						break
					}
					cellStyle := style
					if showCursor && p.cursor.X == x && p.cursor.Y == y {
						cellStyle = cellStyle.Reverse(true)
					}
					b.WriteString(cellStyle.Render(string(r)))
					x++
				}
			}
		}
		for x < cols {
			style := lipgloss.NewStyle()
			if showCursor && p.cursor.X == x && p.cursor.Y == y {
				style = style.Reverse(true)
			}
			b.WriteString(style.Render(" "))
			x++
		}
		lines[y] = b.String()
	}
	return lines
}

// runStyle translates a proto.Run's wire-level style into lipgloss.
func runStyle(run proto.Run) lipgloss.Style {
	s := lipgloss.NewStyle()
	if run.FG != "" {
		s = s.Foreground(lipgloss.Color(run.FG))
	}
	if run.BG != "" {
		s = s.Background(lipgloss.Color(run.BG))
	}
	if run.Attrs&proto.AttrBold != 0 {
		s = s.Bold(true)
	}
	if run.Attrs&proto.AttrFaint != 0 {
		s = s.Faint(true)
	}
	if run.Attrs&proto.AttrItalic != 0 {
		s = s.Italic(true)
	}
	if run.Attrs&proto.AttrUnderline != 0 {
		s = s.Underline(true)
	}
	if run.Attrs&proto.AttrBlink != 0 {
		s = s.Blink(true)
	}
	if run.Attrs&proto.AttrReverse != 0 {
		s = s.Reverse(true)
	}
	if run.Attrs&proto.AttrStrikethrough != 0 {
		s = s.Strikethrough(true)
	}
	return s
}
