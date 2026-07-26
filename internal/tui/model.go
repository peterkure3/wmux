// Package tui is wmux's full-screen multi-pane terminal multiplexer.
//
// It is a *client*, not part of the daemon: panes are daemon-owned
// surfaces attached over mode=cells (see internal/daemon's cellsframe
// machinery and tuipane.go), composed by internal/layout's split tree,
// with the session sidebar folded in as one more pane. Nothing here
// touches wt.exe — the layout, the focus model, and the key routing are
// all owned by this package.
//
// Entry point is Run; internal/cli builds Options from the command line
// and calls it. Nothing in here imports internal/cli.
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/peterkure3/wmux/internal/layout"
	"github.com/peterkure3/wmux/internal/proto"
)

// sidebarLeafID is the layout tree's reserved leaf ID for the sidebar
// pane. NUL-prefixed so no real (user-typed) session ID can ever collide
// with it.
const sidebarLeafID = "\x00sidebar"

// sidebarRatio is the share of the width the session list keeps when it
// sits beside panes.
const sidebarRatio = 0.2

// PaneSpec describes one pane to open at startup.
type PaneSpec struct {
	ID      string // "" derives one from Cwd
	Cwd     string
	Command string
	Distro  string
	Native  bool
}

// Options configure a TUI session.
type Options struct {
	// Open lists panes to create at startup, in order.
	Open []PaneSpec

	// Grid arranges panes in a balanced grid (`wmux grid N`) instead of
	// splitting each new pane off the focused one. It stays on for the
	// rest of the session, so panes opened later join the grid too.
	Grid bool
}

// inputMode decides where a keypress goes. This is the mode switch: in
// modeInsert every key is forwarded verbatim to the focused pane's pty,
// so agents see an ordinary terminal; in modeCommand keys are wmux
// commands (split, focus, close, quit) and never reach the pane.
type inputMode int

const (
	modeInsert inputMode = iota
	modeCommand
)

type tuiModel struct {
	layout *layout.Node
	rects  map[string]layout.Rect

	focused string // sidebarLeafID or a session ID in panes

	sidebar       sidebarModel
	sidebarHidden bool
	panes         map[string]*tuiSurfacePane

	width, height int // height already excludes the footer line

	mode inputMode

	// grid keeps the whole pane set in a balanced grid, rebuilt whenever
	// panes are added or removed.
	grid bool

	// pendingSplit/pendingTarget describe how the *next* opened pane
	// joins the layout: which leaf it divides, and along which axis.
	// Command mode's `|`/`-`/`n` set them just before handing off to the
	// sidebar's new-pane prompt, which is why they have to survive the
	// focus move to the sidebar that handoff performs.
	pendingSplit  layout.SplitDir
	pendingTarget string

	status string

	initOpen []PaneSpec // consumed by Init
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

// sidebarFocusHook/sidebarOpenHook/sidebarCloseHook are the
// sidebarModel.onFocus/onOpen/onClose implementations the TUI installs.
// They're static (capture nothing) because bubbletea's model is a value
// copied on every Update — there is no "current tuiModel" to close over
// at hook-install time, only the message they emit, which
// tuiModel.Update resolves against whatever the latest model actually is.
func sidebarFocusHook(id string) tea.Cmd {
	return func() tea.Msg { return focusRequestMsg{id: id} }
}
func sidebarOpenHook(cwd, command string) tea.Cmd {
	return func() tea.Msg { return openRequestMsg{cwd: cwd, command: command, native: true} }
}
func sidebarCloseHook(id string) tea.Cmd {
	return func() tea.Msg { return closeRequestMsg{id: id} }
}

// Run starts the multiplexer and blocks until the user quits.
func Run(opts Options) error {
	m := newModel(opts)
	go sseListen(m.sidebar.events)

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

func newModel(opts Options) tuiModel {
	home, _ := os.UserHomeDir()
	return tuiModel{
		layout:  layout.NewLeaf(sidebarLeafID),
		focused: sidebarLeafID,
		panes:   make(map[string]*tuiSurfacePane),
		grid:    opts.Grid,
		sidebar: sidebarModel{
			unread:   map[string]unreadNote{},
			daemonOK: true,
			newCwd:   home,
			ti:       textinput.New(),
			help:     newHelpModel(),
			events:   make(chan proto.Event, 32),
			onFocus:  sidebarFocusHook,
			onOpen:   sidebarOpenHook,
			onClose:  sidebarCloseHook,
		},
		initOpen: opts.Open,
	}
}

func (m tuiModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.sidebar.Init()}
	for _, spec := range m.initOpen {
		req := openRequestMsg{
			id: spec.ID, cwd: spec.Cwd, command: spec.Command,
			distro: spec.Distro, native: spec.Native,
		}
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
		return m, tea.Batch(m.placePane(msg.id)...)

	case focusRequestMsg:
		if _, ok := m.panes[msg.id]; ok {
			m.focused = msg.id
			m.mode = modeInsert
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
		return m.handleMouse(msg)

	default:
		// Everything else (sessionsMsg, evtMsg, tickMsg, daemonDownMsg,
		// statusMsg) is the sidebar's own vocabulary — always routed
		// there regardless of focus, so its session list and event feed
		// keep updating in the background even while a surface pane has
		// keyboard focus.
		return m, m.updateSidebarMsg(msg)
	}
}

// placePane inserts a freshly created surface into the layout, attaches
// to its cell stream, and focuses it.
func (m *tuiModel) placePane(id string) []tea.Cmd {
	pane := attachSurfaceCellsPane(id)
	m.panes[id] = pane

	switch {
	case m.grid:
		m.rebuildGrid()
	case m.layout == nil:
		m.layout = layout.NewLeaf(id)
	default:
		// pendingTarget is the leaf command mode aimed the split at; it
		// outranks m.focused, which the new-pane prompt has since moved
		// to the sidebar.
		target := m.pendingTarget
		if target == "" {
			target = m.focused
		}
		if !m.layout.Has(target) {
			target = m.layout.Leaves()[0]
		}
		ratio := 0.5
		if target == sidebarLeafID {
			ratio = sidebarRatio // the sidebar keeps a narrow column, the pane takes the rest
		}
		m.layout, _ = m.layout.SplitLeaf(target, m.pendingSplit, id, ratio)
	}
	m.pendingSplit, m.pendingTarget = layout.SplitRight, ""
	m.focused = id
	m.mode = modeInsert

	cmds := m.recomputeLayout()
	return append(cmds, waitPaneFrame(id, pane.frames))
}

// rebuildGrid re-lays every pane out as a balanced grid, with the sidebar
// (when visible) as a narrow column down the left. Called on every open
// and close in grid mode so the arrangement stays balanced as the pane
// count changes.
func (m *tuiModel) rebuildGrid() {
	gridRoot := layout.Grid(m.paneIDsInOrder())
	switch {
	case gridRoot == nil:
		m.layout = layout.NewLeaf(sidebarLeafID)
		m.sidebarHidden = false
	case m.sidebarHidden:
		m.layout = gridRoot
	default:
		m.layout = &layout.Node{
			Dir: layout.SplitRight, Ratio: sidebarRatio,
			A: layout.NewLeaf(sidebarLeafID), B: gridRoot,
		}
	}
}

// paneIDsInOrder returns the open surface panes in their current layout
// order, so a rebuild doesn't shuffle panes the user is looking at.
// Panes not yet in the tree (the one just opened) go on the end.
func (m *tuiModel) paneIDsInOrder() []string {
	var ids []string
	seen := map[string]bool{}
	for _, id := range m.layout.Leaves() {
		if _, ok := m.panes[id]; ok {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	for id := range m.panes {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	return ids
}

// updateSidebarMsg runs msg through the embedded sidebar model and keeps
// the (possibly mutated) result.
func (m *tuiModel) updateSidebarMsg(msg tea.Msg) tea.Cmd {
	newModel, cmd := m.sidebar.Update(msg)
	m.sidebar = newModel.(sidebarModel)
	return cmd
}

func (m tuiModel) moveFocus(dir string) (tea.Model, tea.Cmd) {
	if next := layout.NeighborInDirection(m.rects, m.focused, dir); next != "" {
		m.focused = next
	}
	return m, nil
}

// cycleFocus moves focus to the next leaf in reading order — Ctrl-O, and
// command mode's tab.
func (m *tuiModel) cycleFocus() {
	leaves := m.layout.Leaves()
	if len(leaves) < 2 {
		return
	}
	m.focused = nextLeaf(leaves, m.focused)
}

// toggleSidebar shows or hides the session list column. Hiding it gives
// the panes the full width without needing a launch-time flag.
func (m *tuiModel) toggleSidebar() {
	if !m.sidebarHidden {
		if len(m.panes) == 0 {
			m.status = "nothing else to show — open a pane first"
			return // refuse to blank the screen
		}
		m.sidebarHidden = true
		if m.grid {
			m.rebuildGrid()
		} else {
			m.layout, _ = m.layout.RemoveLeaf(sidebarLeafID)
		}
		if m.focused == sidebarLeafID {
			m.focused = m.layout.Leaves()[0]
		}
		return
	}
	m.sidebarHidden = false
	if m.grid {
		m.rebuildGrid()
		return
	}
	m.layout = &layout.Node{
		Dir: layout.SplitRight, Ratio: sidebarRatio,
		A: layout.NewLeaf(sidebarLeafID), B: m.layout,
	}
}

// closePane removes id from the layout and daemon, refocusing to
// whatever leaf ends up first if the closed pane had focus. Returns nil
// (no-op) if id isn't one of this tui's own panes — the sidebar leaf and
// unknown IDs both fall through this, letting callers fall back to their
// own behavior.
func (m *tuiModel) closePane(id string) []tea.Cmd {
	if id == sidebarLeafID {
		return nil
	}
	if _, ok := m.panes[id]; !ok {
		return nil
	}
	delete(m.panes, id)
	if m.grid {
		m.rebuildGrid()
	} else {
		m.layout, _ = m.layout.RemoveLeaf(id)
	}
	if m.layout == nil {
		// The last pane went away with the sidebar hidden — bring the
		// session list back rather than leaving an empty screen.
		m.layout = layout.NewLeaf(sidebarLeafID)
		m.sidebarHidden = false
	}
	if m.focused == id {
		if leaves := m.layout.Leaves(); len(leaves) > 0 {
			m.focused = leaves[0]
		} else {
			m.focused = sidebarLeafID
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
	m.rects = m.layout.Layout(layout.Rect{X: 0, Y: 0, W: m.width, H: m.height})

	var cmds []tea.Cmd
	if r, ok := m.rects[sidebarLeafID]; ok {
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
func contentSize(r layout.Rect) (int, int) {
	w, h := r.W-2, r.H-2
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return w, h
}

// DefaultPaneID is defaultPaneID exported for internal/cli, which names
// `wmux grid`'s panes the same way an interactively-opened pane is named.
func DefaultPaneID(cwd string) string { return defaultPaneID(cwd) }

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
		return "wmux: starting…"
	}
	return m.renderNode(m.layout) + "\n" + m.statusLine()
}

// renderNode walks the layout tree, joining each Split's two rendered
// children exactly as the tree shapes them. It deliberately doesn't
// recompute any geometry itself — every leaf's rect comes from m.rects
// (Layout()'s output, the same values resize already used), so this and
// the daemon-side pty size can never drift apart.
func (m tuiModel) renderNode(n *layout.Node) string {
	if n == nil {
		return ""
	}
	if n.SessionID != "" {
		return m.renderPaneBox(n.SessionID)
	}
	a := m.renderNode(n.A)
	b := m.renderNode(n.B)
	if n.Dir == layout.SplitDown {
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

	var content string
	switch {
	case id == sidebarLeafID:
		content = m.sidebar.View()
	default:
		if p, ok := m.panes[id]; ok {
			// The pane's cursor is only drawn while it actually has the
			// keystrokes: in command mode they're wmux's, not the pty's.
			content = strings.Join(p.render(innerW, innerH, focused && m.mode == modeInsert), "\n")
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
	return box.Render(content)
}

func (m tuiModel) statusLine() string {
	focusLabel := m.focused
	if focusLabel == sidebarLeafID {
		focusLabel = "sidebar"
	}
	label, hint := "INSERT", insertModeHelp
	if m.mode == modeCommand {
		label, hint = "COMMAND", commandModeHelp
	}
	line := fmt.Sprintf(" %s  %s  %s", label, focusLabel, hint)
	if m.status != "" {
		line += "  " + m.status
	}
	return styleDim.Render(padTrunc(line, m.width))
}
