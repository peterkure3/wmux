// wmux sidebar-ui is the TUI that runs inside the pane `wmux sidebar`
// opens: a live, interactive list of every session the daemon knows about
// (the cmux sidebar experience, inside Windows Terminal). It renders from
// the daemon's typed /events push feed with a slow poll as fallback, and
// drives the same focus/close/pane machinery the plain CLI commands use.
// See docs/sidebar-design.md for the design of record.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/peterkure/wmux/internal/proto"
)

const sidebarPoll = 2 * time.Second

// ANSI fragments — raw escapes instead of a styling library; the sidebar's
// look is a handful of colors, not a layout engine.
const (
	aReset   = "\x1b[0m"
	aDim     = "\x1b[2m"
	aInverse = "\x1b[7m"
	aGreen   = "\x1b[32m"
	aYellow  = "\x1b[33m"
	aMagenta = "\x1b[35m"
)

type sidebarMode int

const (
	modeList sidebarMode = iota
	modeConfirmClose
	modePromptCwd
	modePromptCmd
)

type unreadNote struct {
	body string
	at   time.Time
}

type sidebarModel struct {
	sessions []proto.SessionInfo
	unread   map[string]unreadNote

	selected int
	scroll   int // first visible session index
	width    int
	height   int

	daemonOK bool
	status   string // transient footer message (last action result)

	mode      sidebarMode
	pendingID string // session awaiting close confirmation
	input     string // prompt buffer for modePromptCwd/Cmd
	newCwd    string // cwd collected by the first prompt step

	events chan proto.Event
}

// messages
type sessionsMsg []proto.SessionInfo
type evtMsg proto.Event
type tickMsg time.Time
type daemonDownMsg struct{}
type statusMsg string

func cmdSidebarUI(args []string) {
	home, _ := os.UserHomeDir()
	m := sidebarModel{
		unread:   map[string]unreadNote{},
		daemonOK: true,
		newCwd:   home,
		events:   make(chan proto.Event, 32),
	}
	go sseListen(m.events)

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "wmux sidebar-ui: %v\n", err)
		os.Exit(1)
	}
}

// sseListen tails GET /events forever, reconnecting on any failure — the
// model just consumes proto.Events off the channel and never worries about
// connection state (the 2s poll covers gaps, and fetch failures drive the
// "wmuxd offline" indicator).
func sseListen(ch chan<- proto.Event) {
	for {
		resp, err := http.Get(daemonAddr + "/events")
		if err == nil {
			scanner := bufio.NewScanner(resp.Body)
			scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "data: ") {
					var evt proto.Event
					if json.Unmarshal([]byte(line[6:]), &evt) == nil {
						ch <- evt
					}
				}
			}
			resp.Body.Close()
		}
		time.Sleep(2 * time.Second)
	}
}

func waitEvent(ch chan proto.Event) tea.Cmd {
	return func() tea.Msg { return evtMsg(<-ch) }
}

func sidebarTick() tea.Cmd {
	return tea.Tick(sidebarPoll, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func fetchSessionsCmd() tea.Msg {
	resp, err := http.Get(daemonAddr + "/sessions")
	if err != nil {
		return daemonDownMsg{}
	}
	defer resp.Body.Close()
	var ss []proto.SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&ss); err != nil {
		return daemonDownMsg{}
	}
	return sessionsMsg(ss)
}

func focusCmd(id string) tea.Cmd {
	return func() tea.Msg {
		if err := focusSessionByID(id); err != nil {
			return statusMsg(fmt.Sprintf("focus %s: %v", id, err))
		}
		return statusMsg("focused " + id)
	}
}

func closeCmd(id string) tea.Cmd {
	return func() tea.Msg {
		if err := closeSession(id); err != nil {
			return statusMsg(fmt.Sprintf("close %s: %v", id, err))
		}
		return statusMsg("closed " + id)
	}
}

// openPaneCmd opens a new native agent pane next to the sidebar: nudge
// focus right so the split lands in the agent area (a no-op when the
// sidebar is the tab's only pane), then split with the shared wmux
// profile flow. WSL-targeted panes stay a `wmux pane` CLI affair — the
// prompt would need distro plumbing the two-line footer can't carry.
func openPaneCmd(cwd, command string, sessions []proto.SessionInfo) tea.Cmd {
	return func() tea.Msg {
		id := uniquePaneID(filepath.Base(strings.TrimRight(cwd, `\/`)), sessions)
		spec := proto.PaneSpec{ID: id, Cwd: cwd, Command: command, Native: true}
		if err := filePaneSpec(spec); err != nil {
			return statusMsg(fmt.Sprintf("new pane: %v", err))
		}
		exec.Command("wt.exe", "-w", "0", "move-focus", "right").Run()
		wtArgs := []string{"-w", "0", "split-pane", "-V", "-s", "0.67",
			"--title", id, "--suppressApplicationTitle", "--profile", "wmux"}
		if err := exec.Command("wt.exe", wtArgs...).Start(); err != nil {
			return statusMsg(fmt.Sprintf("new pane: wt.exe: %v", err))
		}
		return statusMsg("opened " + id)
	}
}

// uniquePaneID appends -2, -3, ... while base collides with a session that
// is still running (exited entries are reusable, same rule as the daemon's).
func uniquePaneID(base string, sessions []proto.SessionInfo) string {
	if base == "" || base == "." {
		base = "pane"
	}
	running := map[string]bool{}
	for _, s := range sessions {
		if s.Running {
			running[s.ID] = true
		}
	}
	id := base
	for n := 2; running[id]; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

func (m sidebarModel) Init() tea.Cmd {
	return tea.Batch(fetchSessionsCmd, waitEvent(m.events), sidebarTick())
}

func (m sidebarModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureVisible()
		return m, nil

	case sessionsMsg:
		m.daemonOK = true
		m.setSessions([]proto.SessionInfo(msg))
		return m, nil

	case evtMsg:
		evt := proto.Event(msg)
		switch evt.Type {
		case proto.EventNotify:
			if evt.Notify != nil {
				m.unread[evt.Notify.SessionID] = unreadNote{body: evt.Notify.Display(), at: evt.Notify.Time}
				m.ensureVisible() // blocks grow a line when a note appears
			}
		case proto.EventSessions:
			m.daemonOK = true
			m.setSessions(evt.Sessions)
		}
		return m, waitEvent(m.events)

	case tickMsg:
		return m, tea.Batch(fetchSessionsCmd, sidebarTick())

	case daemonDownMsg:
		m.daemonOK = false
		return m, nil

	case statusMsg:
		m.status = string(msg)
		return m, nil

	case tea.MouseMsg:
		return m.updateMouse(msg)

	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m sidebarModel) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Prompt modes consume printable input before anything else.
	if m.mode == modePromptCwd || m.mode == modePromptCmd {
		switch key {
		case "ctrl+c", "esc":
			m.mode = modeList
			m.input = ""
		case "backspace":
			if r := []rune(m.input); len(r) > 0 {
				m.input = string(r[:len(r)-1])
			}
		case "enter":
			val := strings.TrimSpace(m.input)
			if m.mode == modePromptCwd {
				if val == "" {
					val = m.newCwd
				}
				m.newCwd = val
				m.mode = modePromptCmd
				m.input = ""
			} else {
				m.mode = modeList
				m.input = ""
				if val != "" {
					return m, openPaneCmd(m.newCwd, val, m.sessions)
				}
			}
		default:
			if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
				m.input += string(msg.Runes)
				if msg.Type == tea.KeySpace {
					m.input += " "
				}
			}
		}
		return m, nil
	}

	if m.mode == modeConfirmClose {
		switch key {
		case "y", "Y":
			id := m.pendingID
			m.mode = modeList
			m.pendingID = ""
			return m, closeCmd(id)
		default: // anything else cancels
			m.mode = modeList
			m.pendingID = ""
		}
		return m, nil
	}

	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.selected > 0 {
			m.selected--
			m.ensureVisible()
		}
	case "down", "j":
		if m.selected < len(m.sessions)-1 {
			m.selected++
			m.ensureVisible()
		}
	case "enter":
		if s, ok := m.current(); ok {
			delete(m.unread, s.ID)
			return m, focusCmd(s.ID)
		}
	case "x":
		if s, ok := m.current(); ok && s.Running {
			m.mode = modeConfirmClose
			m.pendingID = s.ID
		}
	case "n":
		m.mode = modePromptCwd
		m.input = m.newCwd
	case "r":
		m.status = ""
		return m, fetchSessionsCmd
	}
	return m, nil
}

func (m sidebarModel) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeList {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if m.selected > 0 {
			m.selected--
			m.ensureVisible()
		}
	case tea.MouseButtonWheelDown:
		if m.selected < len(m.sessions)-1 {
			m.selected++
			m.ensureVisible()
		}
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		_, owner := m.bodyLines()
		row := msg.Y - 2 // body starts under header + separator
		if row >= 0 && row < len(owner) && owner[row] >= 0 {
			m.selected = owner[row]
			m.ensureVisible()
			if s, ok := m.current(); ok {
				delete(m.unread, s.ID)
				return m, focusCmd(s.ID)
			}
		}
	}
	return m, nil
}

func (m sidebarModel) current() (proto.SessionInfo, bool) {
	if m.selected < 0 || m.selected >= len(m.sessions) {
		return proto.SessionInfo{}, false
	}
	return m.sessions[m.selected], true
}

// setSessions swaps in a fresh session list, keeping the selection pinned
// to the same session ID across reorderings and removals.
func (m *sidebarModel) setSessions(ss []proto.SessionInfo) {
	var selID string
	if s, ok := m.current(); ok {
		selID = s.ID
	}
	sort.Slice(ss, func(i, j int) bool {
		if ss[i].Running != ss[j].Running {
			return ss[i].Running // running sessions first
		}
		return ss[i].ID < ss[j].ID
	})
	m.sessions = ss
	m.selected = 0
	for i, s := range ss {
		if s.ID == selID {
			m.selected = i
			break
		}
	}
	m.ensureVisible()
}

// bodyHeight is the line budget for session blocks given the fixed chrome:
// header + separator above, separator + summary + context + help below.
func (m sidebarModel) bodyHeight() int {
	h := m.height
	if h == 0 {
		h = 24
	}
	if b := h - 6; b > 0 {
		return b
	}
	return 1
}

// ensureVisible adjusts scroll so the selected session's whole block fits
// in the body. Blocks are 2 or 3 lines (3 with an unread note), so this
// walks real heights instead of assuming a fixed row size.
func (m *sidebarModel) ensureVisible() {
	if m.selected < m.scroll {
		m.scroll = m.selected
		return
	}
	avail := m.bodyHeight()
	for m.scroll < m.selected {
		used := 0
		for i := m.scroll; i <= m.selected; i++ {
			used += m.blockHeight(i)
		}
		if used <= avail {
			break
		}
		m.scroll++
	}
}

func (m sidebarModel) blockHeight(i int) int {
	if _, ok := m.unread[m.sessions[i].ID]; ok {
		return 3
	}
	return 2
}

func (m sidebarModel) View() string {
	w := m.width
	if w == 0 {
		w = 30
	}
	sep := aDim + strings.Repeat("─", w) + aReset

	var b strings.Builder
	b.WriteString(padTrunc(" wmux", w) + "\n" + sep + "\n")

	lines, _ := m.bodyLines()
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	for i := len(lines); i < m.bodyHeight(); i++ {
		b.WriteString("\n")
	}

	b.WriteString(sep + "\n")
	summary := fmt.Sprintf(" %d sessions · %d unread", len(m.sessions), len(m.unread))
	if !m.daemonOK {
		summary += " · " + aYellow + "wmuxd offline" + aReset
	}
	b.WriteString(padTrunc(summary, w) + "\n")

	// Context line: active prompt/confirmation, else last action result.
	switch m.mode {
	case modeConfirmClose:
		b.WriteString(aInverse + padTrunc(fmt.Sprintf(" close %s? y/n", m.pendingID), w) + aReset + "\n")
	case modePromptCwd:
		b.WriteString(aInverse + padTrunc(" cwd> "+m.input+"▌", w) + aReset + "\n")
	case modePromptCmd:
		b.WriteString(aInverse + padTrunc(" cmd> "+m.input+"▌", w) + aReset + "\n")
	default:
		b.WriteString(aDim + padTrunc(" "+m.status, w) + aReset + "\n")
	}

	b.WriteString(aDim + padTrunc(" ⏎ focus  x close  n new  q quit", w) + aReset)
	return b.String()
}

// bodyLines renders the visible session blocks and, in parallel, which
// session owns each screen line — the same mapping mouse clicks resolve
// against, so render and hit-testing can never disagree.
func (m sidebarModel) bodyLines() (lines []string, owner []int) {
	w := m.width
	if w == 0 {
		w = 30
	}
	avail := m.bodyHeight()

	for i := m.scroll; i < len(m.sessions) && len(lines) < avail; i++ {
		s := m.sessions[i]
		note, hasNote := m.unread[s.ID]

		marker := "  "
		if i == m.selected {
			marker = "▸ "
		}
		dot, dotColor := "●", aGreen
		if !s.Running {
			dot, dotColor = "○", aDim
		}
		tag := ""
		if !s.Native {
			tag = " wsl"
		}

		line1 := padTrunc(fmt.Sprintf("%s%s %s  %s%s", marker, dot, s.ID, s.Branch, tag), w)
		// Inverse the whole selected row; otherwise color just the dot.
		if i == m.selected {
			line1 = aInverse + line1 + aReset
		} else {
			line1 = strings.Replace(line1, dot, dotColor+dot+aReset, 1)
		}
		lines = append(lines, line1)
		owner = append(owner, i)

		ports := ""
		for _, p := range s.Ports {
			ports += fmt.Sprintf(" :%d", p)
		}
		lines = append(lines, aDim+padTrunc("    "+leftTrunc(s.Cwd, w-8-len(ports))+ports, w)+aReset)
		owner = append(owner, i)

		if hasNote && len(lines) < avail {
			age := shortAge(time.Since(note.at))
			body := strings.ReplaceAll(note.body, "\n", " ")
			lines = append(lines, aMagenta+padTrunc(fmt.Sprintf("    ✉ %s  %s", body, age), w)+aReset)
			owner = append(owner, i)
		}
	}
	return lines, owner
}

// padTrunc right-pads or truncates a plain (ANSI-free) string to exactly
// w columns, counting runes — good enough for the sidebar's ASCII-heavy
// content without pulling in a display-width library.
func padTrunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) > w {
		if w == 1 {
			return "…"
		}
		return string(r[:w-1]) + "…"
	}
	return s + strings.Repeat(" ", w-len(r))
}

// leftTrunc keeps the tail of a path-like string, which is the useful end.
func leftTrunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	return "…" + string(r[len(r)-w+1:])
}

func shortAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
