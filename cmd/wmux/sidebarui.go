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

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/peterkure/wmux/internal/proto"
)

const sidebarPoll = 2 * time.Second

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
	pendingID string          // session awaiting close confirmation
	ti        textinput.Model // input for modePromptCwd/Cmd
	newCwd    string          // cwd collected by the first prompt step
	help      help.Model

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
		ti:       textinput.New(),
		help:     newHelpModel(),
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
		m.ti.Width = promptWidth(m.width)
		m.help.Width = m.width - 1 // leading space in the View() footer line
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
			m.ti.Blur()
			return m, nil
		case "enter":
			val := strings.TrimSpace(m.ti.Value())
			if m.mode == modePromptCwd {
				if val == "" {
					val = m.newCwd
				}
				m.newCwd = val
				m.mode = modePromptCmd
				m.ti.Prompt = " cmd> "
				m.ti.SetValue("")
				return m, nil
			}
			m.mode = modeList
			m.ti.Blur()
			if val != "" {
				return m, openPaneCmd(m.newCwd, val, m.sessions)
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.ti, cmd = m.ti.Update(msg)
		return m, cmd
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
		m.ti.Prompt = " cwd> "
		m.ti.Width = promptWidth(m.width)
		m.ti.SetValue(m.newCwd)
		m.ti.CursorEnd()
		return m, m.ti.Focus()
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
