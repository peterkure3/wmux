// Key routing for the multiplexer: which of the two input modes is
// active, and where each keypress goes.
//
// The whole model is two modes and three global keys:
//
//   - INSERT (default) — every key is reconstructed into terminal bytes
//     (tuikeys.go) and forwarded to the focused pane's pty, or handed to
//     the sidebar when the sidebar has focus. Agents see a normal
//     terminal, Ctrl-C included.
//   - COMMAND — keys are wmux commands: split, focus, close, quit. None
//     of them reach the pane.
//
// Ctrl-B switches into COMMAND; Esc (or i) switches back. Unlike tmux's
// one-shot prefix, COMMAND is sticky, so a run of splits and focus moves
// is one Ctrl-B rather than one per key. Ctrl-O (cycle panes) and Ctrl-B
// itself are the only keys INSERT steals from the pane.
package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/peterkure3/wmux/internal/layout"
)

const (
	insertModeHelp  = "ctrl+b: commands · ctrl+o: cycle panes"
	commandModeHelp = "| ─ split · n new · x close · tab/ctrl+o cycle · arrows focus · b sidebar · g grid · esc insert · q quit"
)

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global, mode-independent: cycling panes is the one navigation key
	// worth having without a mode switch first (Plan_B's Ctrl-O).
	if msg.Type == tea.KeyCtrlO {
		m.cycleFocus()
		m.mode = modeInsert
		return m, nil
	}
	if m.mode == modeCommand {
		return m.handleCommandKey(msg)
	}
	if msg.Type == tea.KeyCtrlB {
		m.mode = modeCommand
		m.status = ""
		return m, nil
	}
	if m.focused == sidebarLeafID {
		return m, m.updateSidebarMsg(msg)
	}
	return m, m.forwardKeyToPane(msg)
}

// forwardKeyToPane reconstructs msg's terminal bytes and posts them to
// the focused surface. Returns nil for keys with no byte form, and for a
// pane that has already exited.
func (m tuiModel) forwardKeyToPane(msg tea.KeyMsg) tea.Cmd {
	p, ok := m.panes[m.focused]
	if !ok || p.exited {
		return nil
	}
	b := keyMsgToBytes(msg)
	if b == nil {
		return nil
	}
	id := m.focused
	return func() tea.Msg { dc().InputSurface(id, b); return nil } //nolint:errcheck // best-effort; a dead surface just stops responding
}

func (m tuiModel) handleCommandKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "i", "enter":
		m.mode = modeInsert
		return m, nil

	case "q":
		return m, tea.Quit

	case "ctrl+b":
		// Send a literal Ctrl-B through to the pane (for the agent's own
		// nested tmux/emacs), then get out of the way.
		m.mode = modeInsert
		if m.focused == sidebarLeafID {
			return m, nil
		}
		if p, ok := m.panes[m.focused]; ok && !p.exited {
			id := m.focused
			return m, func() tea.Msg { dc().InputSurface(id, []byte{2}); return nil } //nolint:errcheck
		}
		return m, nil

	case "tab":
		m.cycleFocus()
		return m, nil

	case "left", "h":
		return m.moveFocus("left")
	case "right", "l":
		return m.moveFocus("right")
	case "up", "k":
		return m.moveFocus("up")
	case "down", "j":
		return m.moveFocus("down")

	case "|", "v":
		// Vertical divider, panes side by side.
		return m.startSplit(layout.SplitRight)
	case "-", "_", "s":
		// Horizontal divider, panes stacked.
		return m.startSplit(layout.SplitDown)
	case "n":
		return m.startSplit(m.pendingSplit)

	case "x":
		if cmds := m.closePane(m.focused); cmds != nil {
			return m, tea.Batch(cmds...)
		}
		return m, nil

	case "b":
		m.toggleSidebar()
		return m, tea.Batch(m.recomputeLayout()...)

	case "g":
		// Snap everything into a balanced grid, and keep it that way as
		// panes come and go — the interactive form of `wmux grid N`.
		m.grid = true
		m.rebuildGrid()
		return m, tea.Batch(m.recomputeLayout()...)
	}
	return m, nil
}

// startSplit records which way the next pane divides the focused one and
// opens the sidebar's own cwd/command prompt, rather than building a
// second new-pane flow at this layer. Insert mode comes back so the
// prompt actually receives what gets typed into it.
func (m tuiModel) startSplit(dir layout.SplitDir) (tea.Model, tea.Cmd) {
	m.pendingSplit = dir
	m.pendingTarget = m.focused // the pane being divided, not the sidebar we're about to focus
	m.mode = modeInsert
	m.focused = sidebarLeafID
	if m.sidebarHidden {
		m.toggleSidebar()
	}
	cmd := m.updateSidebarMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	return m, tea.Batch(append(m.recomputeLayout(), cmd)...)
}
