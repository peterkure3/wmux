// wmux tui: a full-screen multi-pane terminal multiplexer over surfaces
// (Phase 3 of wmux-tui-refactor-plan.md — "the payoff"). Unlike `wmux
// pane`/`wmux grid`, it never touches wt.exe: panes are daemon-owned
// surfaces attached over mode=cells (see internal/daemon's cellsframe
// machinery and tuipane.go), composed and laid out by layout.go's tree,
// with the sidebar folded in as one more pane instead of a separate
// wt.exe split.
//
// This is a client like any other — same daemonGet/daemonPost/dc plumbing
// `wmux connect`/the sidebar already use, just rendering N surfaces
// instead of one.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/peterkure3/wmux/internal/proto"
)

// tuiSidebarLeafID is the layout tree's reserved leaf ID for the sidebar
// pane. NUL-prefixed so no real (user-typed) session ID can ever collide
// with it.
const tuiSidebarLeafID = "\x00sidebar"

// tuiPrefixHelp is the footer hint shown once the prefix key has been
// pressed and the next key is being interpreted as a command.
const tuiPrefixHelp = "PREFIX  arrows/hjkl focus · tab cycle · n new pane · x close · ctrl+b send prefix · q quit"

type tuiModel struct {
	layout *Node
	rects  map[string]Rect

	focused string // tuiSidebarLeafID or a session ID in panes

	sidebar sidebarModel
	panes   map[string]*tuiSurfacePane

	width, height int // height already excludes the footer line

	prefix bool // true right after ctrl+b, awaiting the next key as a command

	status string

	initOpen *openRequestMsg // set by cmdTui when --with was given; consumed by Init
}

// Messages this model handles beyond what sidebarModel already defines.
type paneFrameMsg struct {
	id    string
	frame proto.CellsFrame
}
type paneOpenedMsg struct{ id string }
type focusRequestMsg struct{ id string }
type openRequestMsg struct {
	id, cwd, command, distro string
	native                   bool
}
type closeRequestMsg struct{ id string }

func waitPaneFrame(id string, ch chan proto.CellsFrame) tea.Cmd {
	return func() tea.Msg { return paneFrameMsg{id: id, frame: <-ch} }
}

// tuiSidebarFocusHook/openHook/closeHook are the sidebarModel.onFocus/
// onOpen/onClose implementations `wmux tui` installs. They're static
// (capture nothing) because bubbletea's model is a value copied on every
// Update — there is no "current tuiModel" to close over at hook-install
// time, only the message they emit, which tuiModel.Update resolves
// against whatever the latest model actually is.
func tuiSidebarFocusHook(id string) tea.Cmd {
	return func() tea.Msg { return focusRequestMsg{id: id} }
}
func tuiSidebarOpenHook(cwd, command string) tea.Cmd {
	return func() tea.Msg { return openRequestMsg{cwd: cwd, command: command, native: true} }
}
func tuiSidebarCloseHook(id string) tea.Cmd {
	return func() tea.Msg { return closeRequestMsg{id: id} }
}

func cmdTui(args []string) {
	fs := newFlagSet("tui")
	with := fs.String("with", "", "open a first agent pane running this command, next to the sidebar")
	cwd := fs.String("cwd", "", "working directory for --with")
	id := fs.String("id", "", "session ID for --with (defaults to the cwd's base name)")
	distro := fs.String("distro", "", "WSL distro for --with (ignored with --native)")
	native := fs.Bool("native", false, "run --with directly on the daemon's OS, no WSL")
	fs.Parse(args)
	*with = resolveCmd(*with)

	home, _ := os.UserHomeDir()
	m := tuiModel{
		layout:  NewLeaf(tuiSidebarLeafID),
		focused: tuiSidebarLeafID,
		panes:   make(map[string]*tuiSurfacePane),
		sidebar: sidebarModel{
			unread:   map[string]unreadNote{},
			daemonOK: true,
			newCwd:   home,
			ti:       textinput.New(),
			help:     newHelpModel(),
			events:   make(chan proto.Event, 32),
			onFocus:  tuiSidebarFocusHook,
			onOpen:   tuiSidebarOpenHook,
			onClose:  tuiSidebarCloseHook,
		},
	}
	if *with != "" {
		m.initOpen = &openRequestMsg{id: *id, cwd: *cwd, command: *with, distro: *distro, native: *native}
	}
	go sseListen(m.sidebar.events)

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "wmux tui: %v\n", err)
		os.Exit(1)
	}
}

func (m tuiModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.sidebar.Init()}
	if m.initOpen != nil {
		req := *m.initOpen
		cmds = append(cmds, func() tea.Msg { return req })
	}
	return tea.Batch(cmds...)
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height - 1 // reserve the footer line
		if m.height < 1 {
			m.height = 1
		}
		return m, tea.Batch(m.recomputeLayout()...)

	case paneFrameMsg:
		p, ok := m.panes[msg.id]
		if !ok {
			return m, nil
		}
		p.applyFrame(msg.frame)
		if p.exited {
			return m, nil // the attach goroutine has already stopped; nothing left to wait for
		}
		return m, waitPaneFrame(msg.id, p.frames)

	case openRequestMsg:
		return m, m.openSurfaceCmd(msg.id, msg.cwd, msg.command, msg.distro, msg.native)

	case paneOpenedMsg:
		target := m.focused
		ratio := 0.5
		if target == tuiSidebarLeafID {
			ratio = 0.2 // new pane gets 80%, matching `wmux sidebar --with`'s own split
		}
		if m.layout == nil {
			m.layout = NewLeaf(msg.id)
		} else {
			m.layout, _ = m.layout.SplitLeaf(target, SplitRight, msg.id, ratio)
		}
		pane := attachSurfaceCellsPane(msg.id)
		m.panes[msg.id] = pane
		m.focused = msg.id
		cmds := m.recomputeLayout()
		cmds = append(cmds, waitPaneFrame(msg.id, pane.frames))
		return m, tea.Batch(cmds...)

	case focusRequestMsg:
		if _, ok := m.panes[msg.id]; ok {
			m.focused = msg.id
		} else {
			m.status = msg.id + " isn't an open pane in this tui — press n to open it"
		}
		return m, nil

	case closeRequestMsg:
		if cmds := m.closePane(msg.id); cmds != nil {
			return m, tea.Batch(cmds...)
		}
		// Not one of this tui's own panes — still ask the daemon to close
		// it, matching the sidebar's default x behavior for any session.
		return m, closeSurfaceCmd(msg.id)

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m, nil // not forwarded to panes in this pass; sidebar-only mouse isn't wired up either

	default:
		// Everything else (sessionsMsg, evtMsg, tickMsg, daemonDownMsg,
		// statusMsg) is the sidebar's own vocabulary — always routed
		// there regardless of focus, so its session list and event feed
		// keep updating in the background even while a surface pane has
		// keyboard focus.
		return m, m.updateSidebarMsg(msg)
	}
}

// updateSidebarMsg runs msg through the embedded sidebar model and keeps
// the (possibly mutated) result.
func (m *tuiModel) updateSidebarMsg(msg tea.Msg) tea.Cmd {
	newModel, cmd := m.sidebar.Update(msg)
	m.sidebar = newModel.(sidebarModel)
	return cmd
}

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.prefix {
		m.prefix = false
		return m.handlePrefixedKey(msg)
	}
	if msg.Type == tea.KeyCtrlB {
		m.prefix = true
		return m, nil
	}
	if m.focused == tuiSidebarLeafID {
		return m, m.updateSidebarMsg(msg)
	}
	p, ok := m.panes[m.focused]
	if !ok || p.exited {
		return m, nil
	}
	b := keyMsgToBytes(msg)
	if b == nil {
		return m, nil
	}
	id := m.focused
	return m, func() tea.Msg { dc().InputSurface(id, b); return nil } //nolint:errcheck // best-effort; a dead surface just stops responding
}

func (m tuiModel) handlePrefixedKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "ctrl+b":
		if m.focused != tuiSidebarLeafID {
			if p, ok := m.panes[m.focused]; ok && !p.exited {
				id := m.focused
				return m, func() tea.Msg { dc().InputSurface(id, []byte{2}); return nil } //nolint:errcheck
			}
		}
		return m, nil
	case "tab":
		if leaves := m.layout.Leaves(); len(leaves) > 1 {
			m.focused = nextLeaf(leaves, m.focused)
		}
		return m, nil
	case "left", "h":
		return m.moveFocus("left")
	case "right", "l":
		return m.moveFocus("right")
	case "up", "k":
		return m.moveFocus("up")
	case "down", "j":
		return m.moveFocus("down")
	case "x":
		if cmds := m.closePane(m.focused); cmds != nil {
			return m, tea.Batch(cmds...)
		}
		return m, nil
	case "n":
		// One-step shortcut: jump to the sidebar and prime its own "n"
		// prompt, reusing its existing cwd/cmd flow rather than building
		// a second one at this layer.
		m.focused = tuiSidebarLeafID
		cmd := m.updateSidebarMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
		return m, cmd
	}
	return m, nil
}

func (m tuiModel) moveFocus(dir string) (tea.Model, tea.Cmd) {
	if next := NeighborInDirection(m.rects, m.focused, dir); next != "" {
		m.focused = next
	}
	return m, nil
}

// closePane removes id from the layout and daemon, refocusing to
// whatever leaf ends up first if the closed pane had focus. Returns nil
// (no-op) if id isn't one of this tui's own panes — id and "the
// sidebar" both fall through this, letting callers fall back to their
// own behavior.
func (m *tuiModel) closePane(id string) []tea.Cmd {
	if id == tuiSidebarLeafID {
		return nil
	}
	if _, ok := m.panes[id]; !ok {
		return nil
	}
	newRoot, _ := m.layout.RemoveLeaf(id)
	m.layout = newRoot
	delete(m.panes, id)
	if m.focused == id {
		if leaves := m.layout.Leaves(); len(leaves) > 0 {
			m.focused = leaves[0]
		} else {
			m.focused = tuiSidebarLeafID
		}
	}
	cmds := m.recomputeLayout()
	cmds = append(cmds, closeSurfaceCmd(id))
	return cmds
}

// recomputeLayout recomputes every pane's rect from the current tree and
// bounds, resizes the sidebar sub-model and every surface's daemon-side
// pty to match. Resizing is unconditional — ResizeSurface is already a
// no-op on the daemon side when the size hasn't actually changed, so
// there's no need to track "did this rect really change" here too.
func (m *tuiModel) recomputeLayout() []tea.Cmd {
	if m.layout == nil {
		m.rects = nil
		return nil
	}
	m.rects = m.layout.Layout(Rect{0, 0, m.width, m.height})

	var cmds []tea.Cmd
	if r, ok := m.rects[tuiSidebarLeafID]; ok {
		cw, ch := contentSize(r)
		cmds = append(cmds, m.updateSidebarMsg(tea.WindowSizeMsg{Width: cw, Height: ch}))
	}
	for id, p := range m.panes {
		r, ok := m.rects[id]
		if !ok || p.exited {
			continue
		}
		cw, ch := contentSize(r)
		cmds = append(cmds, resizeSurfaceCmd(id, cw, ch))
	}
	return cmds
}

// openSurfaceCmd creates a new daemon surface. The layout placement
// itself happens once paneOpenedMsg comes back — Cmds can't mutate the
// model directly, only report what happened.
func (m tuiModel) openSurfaceCmd(id, cwd, command, distro string, native bool) tea.Cmd {
	return func() tea.Msg {
		if id == "" {
			id = uniquePaneID(defaultPaneID(cwd), m.sidebar.sessions)
		}
		info, err := dc().NewSurface(proto.NewSurfaceRequest{
			ID: id, Cwd: cwd, Command: command, Distro: distro, Native: native,
		})
		if err != nil {
			return statusMsg(fmt.Sprintf("new pane: %v", err))
		}
		return paneOpenedMsg{id: info.ID}
	}
}

func resizeSurfaceCmd(id string, cols, rows int) tea.Cmd {
	return func() tea.Msg {
		if cols >= 2 && rows >= 2 {
			dc().ResizeSurface(id, cols, rows) //nolint:errcheck // best-effort; a dead surface just ignores it
		}
		return nil
	}
}

func closeSurfaceCmd(id string) tea.Cmd {
	return func() tea.Msg { dc().CloseSession(id); return nil } //nolint:errcheck // best-effort
}

// contentSize is a pane's usable interior after its 1-cell border on
// every side — the single place border chrome is accounted for, shared
// by resize (daemon-side pty size), the sidebar's own WindowSizeMsg, and
// View's box rendering, so those three can never disagree.
func contentSize(r Rect) (int, int) {
	w, h := r.W-2, r.H-2
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return w, h
}

// defaultPaneID derives a session ID from a working directory's base name.
func defaultPaneID(cwd string) string {
	base := filepath.Base(strings.TrimRight(cwd, `\/`))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "pane"
	}
	return base
}

func nextLeaf(leaves []string, current string) string {
	for i, id := range leaves {
		if id == current {
			return leaves[(i+1)%len(leaves)]
		}
	}
	return leaves[0]
}

func (m tuiModel) View() string {
	if m.width == 0 || m.height == 0 || m.layout == nil {
		return "wmux tui: starting…"
	}
	return m.renderNode(m.layout) + "\n" + m.statusLine()
}

// renderNode walks the layout tree, joining each Split's two rendered
// children exactly as the tree shapes them. It deliberately doesn't
// recompute any geometry itself — every leaf's rect comes from m.rects
// (Layout()'s output, the same values resize already used), so this and
// the daemon-side pty size can never drift apart.
func (m tuiModel) renderNode(n *Node) string {
	if n == nil {
		return ""
	}
	if n.SessionID != "" {
		return m.renderPaneBox(n.SessionID)
	}
	a := m.renderNode(n.A)
	b := m.renderNode(n.B)
	if n.Dir == SplitDown {
		return lipgloss.JoinVertical(lipgloss.Left, a, b)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, a, b)
}

func (m tuiModel) renderPaneBox(id string) string {
	rect, ok := m.rects[id]
	if !ok {
		return ""
	}
	innerW, innerH := contentSize(rect)
	focused := m.focused == id

	var content, title string
	switch {
	case id == tuiSidebarLeafID:
		content = m.sidebar.View()
		title = "sidebar"
	default:
		if p, ok := m.panes[id]; ok {
			content = strings.Join(p.render(innerW, innerH, focused), "\n")
			title = id
			if p.exited {
				title += " [exited]"
			}
		}
	}

	borderColor := lipgloss.Color("240")
	if focused {
		borderColor = lipgloss.Color("212")
	}
	box := lipgloss.NewStyle().
		Width(innerW).Height(innerH).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor)
	_ = title // border titles aren't a lipgloss primitive; the border color plus the sidebar's own listing is enough to identify panes for v1
	return box.Render(content)
}

func (m tuiModel) statusLine() string {
	hint := "ctrl+b: prefix"
	if m.prefix {
		hint = tuiPrefixHelp
	}
	focusLabel := m.focused
	if focusLabel == tuiSidebarLeafID {
		focusLabel = "sidebar"
	}
	line := fmt.Sprintf(" focus=%s  %s", focusLabel, hint)
	if m.status != "" {
		line += "  " + m.status
	}
	return styleDim.Render(padTrunc(line, m.width))
}
