package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/peterkure3/wmux/internal/client"
	"github.com/peterkure3/wmux/internal/layout"
	"github.com/peterkure3/wmux/internal/proto"
)

// newTestTuiModel mirrors newModel without touching the network —
// Update/View are driven directly, the same pattern
// sidebarui_verify_test.go already established for sidebarModel.
func newTestTuiModel() tuiModel {
	return tuiModel{
		layout:  layout.NewLeaf(sidebarLeafID),
		focused: sidebarLeafID,
		panes:   make(map[string]*tuiSurfacePane),
		sidebar: sidebarModel{
			unread:  map[string]unreadNote{},
			ti:      textinput.New(),
			help:    newHelpModel(),
			events:  make(chan proto.Event, 1),
			onFocus: sidebarFocusHook,
			onOpen:  sidebarOpenHook,
			onClose: sidebarCloseHook,
		},
	}
}

func update(m tuiModel, msg tea.Msg) (tuiModel, tea.Cmd) {
	mm, cmd := m.Update(msg)
	return mm.(tuiModel), cmd
}

// enterCommandMode is the Ctrl-B the mode switch requires before any
// command key is meaningful.
func enterCommandMode(t *testing.T, m tuiModel) tuiModel {
	t.Helper()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlB})
	if m.mode != modeCommand {
		t.Fatal("ctrl+b did not enter command mode")
	}
	return m
}

func press(m tuiModel, s string) (tuiModel, tea.Cmd) {
	return update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
}

// mockDaemon stands up a wmuxd that answers /identify plus whatever mux
// handles, and points the package client at it for the test's duration.
func mockDaemon(t *testing.T, mux *http.ServeMux) {
	t.Helper()
	mux.HandleFunc("/identify", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(proto.IdentifyResponse{App: "wmuxd", Protocol: proto.ProtocolVersion})
	})
	srv := httptest.NewServer(mux)
	old := dc()
	SetClient(client.New(srv.URL, "test-tok"))
	t.Cleanup(func() { SetClient(old); srv.Close() })
}

func TestTuiInitialStateIsSidebarOnly(t *testing.T) {
	m := newTestTuiModel()
	leaves := m.layout.Leaves()
	if len(leaves) != 1 || leaves[0] != sidebarLeafID {
		t.Fatalf("Leaves() = %v, want just the sidebar", leaves)
	}
	if m.focused != sidebarLeafID {
		t.Fatalf("focused = %q, want sidebar", m.focused)
	}
	if m.mode != modeInsert {
		t.Fatal("a fresh model must start in insert mode")
	}
}

func TestTuiWindowSizeComputesRectsAndSidebarSize(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})

	r, ok := m.rects[sidebarLeafID]
	if !ok {
		t.Fatal("no rect computed for the sidebar leaf")
	}
	if r != (layout.Rect{X: 0, Y: 0, W: 100, H: 39}) { // height-1 for the footer line
		t.Fatalf("sidebar rect = %+v, want the full 100x39 body", r)
	}
	// The sidebar sub-model must be sized to its *content* area (inside
	// the border), not the raw rect — otherwise its own View() would
	// overflow the box wrapped around it in renderPaneBox.
	wantW, wantH := contentSize(r)
	if m.sidebar.width != wantW || m.sidebar.height != wantH {
		t.Fatalf("sidebar sub-model size = %dx%d, want %dx%d (rect minus border)",
			m.sidebar.width, m.sidebar.height, wantW, wantH)
	}
}

func TestTuiPaneOpenedSplitsLayoutAndFocuses(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})

	m, cmd := update(m, paneOpenedMsg{id: "agent1"})
	if m.focused != "agent1" {
		t.Fatalf("focused = %q, want agent1 (the just-opened pane)", m.focused)
	}
	if _, ok := m.panes["agent1"]; !ok {
		t.Fatal("agent1 not registered in m.panes")
	}
	leaves := m.layout.Leaves()
	if len(leaves) != 2 {
		t.Fatalf("Leaves() = %v, want sidebar + agent1", leaves)
	}
	if _, ok := m.rects["agent1"]; !ok {
		t.Fatal("no rect computed for agent1 after paneOpenedMsg")
	}
	// A pane split off the sidebar should leave the sidebar only a narrow
	// column, not half the screen.
	if sidebarRect := m.rects[sidebarLeafID]; sidebarRect.W >= 50 {
		t.Fatalf("sidebar rect width = %d, want roughly 20%% of 100 after splitting off agent1", sidebarRect.W)
	}
	if cmd == nil {
		t.Fatal("paneOpenedMsg should return a Cmd (at least waitPaneFrame)")
	}
}

// TestTuiCommandModeIsSticky is the behavioural core of the mode switch:
// one ctrl+b buys a *run* of commands, unlike tmux's one-shot prefix, and
// esc is what ends it.
func TestTuiCommandModeIsSticky(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = update(m, paneOpenedMsg{id: "agent1"})
	m, _ = update(m, paneOpenedMsg{id: "agent2"})

	m = enterCommandMode(t, m)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.mode != modeCommand {
		t.Fatal("command mode ended after one key — it must be sticky")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.mode != modeCommand {
		t.Fatal("command mode ended after two keys")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeInsert {
		t.Fatal("esc did not return to insert mode")
	}
}

func TestTuiCommandModeQuits(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m = enterCommandMode(t, m)
	if _, cmd := press(m, "q"); cmd == nil {
		t.Fatal("command mode q should return tea.Quit")
	}
}

func TestTuiCtrlOCyclesFocusWithoutModeSwitch(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = update(m, paneOpenedMsg{id: "agent1"})
	m, _ = update(m, paneOpenedMsg{id: "agent2"})

	seen := map[string]bool{m.focused: true}
	for i := 0; i < 3; i++ {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlO})
		if m.mode != modeInsert {
			t.Fatal("ctrl+o must not leave the TUI in command mode")
		}
		seen[m.focused] = true
	}
	for _, id := range []string{sidebarLeafID, "agent1", "agent2"} {
		if !seen[id] {
			t.Errorf("ctrl+o cycling never visited %q", id)
		}
	}
}

func TestTuiCommandModeMoveFocusUsesNeighbor(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = update(m, paneOpenedMsg{id: "agent1"}) // splits right of the sidebar

	m = enterCommandMode(t, m)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.focused != sidebarLeafID {
		t.Fatalf("focused = %q after command-mode left, want the sidebar", m.focused)
	}
}

func TestTuiClosePaneRemovesFromLayoutAndRefocuses(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = update(m, paneOpenedMsg{id: "agent1"})

	m = enterCommandMode(t, m)
	m, cmd := press(m, "x")
	if _, ok := m.panes["agent1"]; ok {
		t.Fatal("agent1 still in m.panes after command-mode x")
	}
	leaves := m.layout.Leaves()
	if len(leaves) != 1 || leaves[0] != sidebarLeafID {
		t.Fatalf("Leaves() = %v, want just the sidebar after closing the only other pane", leaves)
	}
	if m.focused != sidebarLeafID {
		t.Fatalf("focused = %q, want the sidebar after its only sibling closed", m.focused)
	}
	if cmd == nil {
		t.Fatal("closing a real pane should return Cmds (recompute + close)")
	}
}

// TestTuiHorizontalSplitStacksPanes checks the `-` command actually
// changes the split axis: the new pane must end up *below* the one it
// divided, not beside it.
func TestTuiHorizontalSplitStacksPanes(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = update(m, paneOpenedMsg{id: "agent1"})

	m = enterCommandMode(t, m)
	m, _ = press(m, "-")
	if m.pendingSplit != layout.SplitDown {
		t.Fatalf("pendingSplit = %v after '-', want SplitDown", m.pendingSplit)
	}
	if m.pendingTarget != "agent1" {
		t.Fatalf("pendingTarget = %q, want agent1 (the pane being divided)", m.pendingTarget)
	}
	// The split command hands off to the sidebar's new-pane prompt, so
	// typing works immediately.
	if m.mode != modeInsert || m.focused != sidebarLeafID {
		t.Fatalf("after '-': mode=%v focused=%q, want insert mode on the sidebar prompt", m.mode, m.focused)
	}

	m, _ = update(m, paneOpenedMsg{id: "agent2"})
	r1, r2 := m.rects["agent1"], m.rects["agent2"]
	if r2.Y <= r1.Y {
		t.Fatalf("agent2 rect %+v is not below agent1 %+v — '-' did not stack them", r2, r1)
	}
	if r1.X != r2.X {
		t.Fatalf("stacked panes should share a column: agent1 X=%d, agent2 X=%d", r1.X, r2.X)
	}
	if m.pendingSplit != layout.SplitRight {
		t.Fatal("pendingSplit should reset to the default after the pane it applied to opened")
	}
}

func TestTuiVerticalSplitPlacesPanesSideBySide(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = update(m, paneOpenedMsg{id: "agent1"})

	m = enterCommandMode(t, m)
	m, _ = press(m, "|")
	m, _ = update(m, paneOpenedMsg{id: "agent2"})

	r1, r2 := m.rects["agent1"], m.rects["agent2"]
	if r2.X <= r1.X {
		t.Fatalf("agent2 rect %+v is not right of agent1 %+v", r2, r1)
	}
	if r1.Y != r2.Y {
		t.Fatalf("side-by-side panes should share a row: agent1 Y=%d, agent2 Y=%d", r1.Y, r2.Y)
	}
}

// TestTuiGridModeArrangesPanesInAGrid covers `wmux grid 4`: four panes
// land in a 2x2, not a staircase of nested splits.
func TestTuiGridModeArrangesPanesInAGrid(t *testing.T) {
	m := newTestTuiModel()
	m.grid = true
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 41})
	for _, id := range []string{"a", "b", "c", "d"} {
		m, _ = update(m, paneOpenedMsg{id: id})
	}

	rows := map[int]bool{}
	cols := map[int]bool{}
	for _, id := range []string{"a", "b", "c", "d"} {
		r, ok := m.rects[id]
		if !ok {
			t.Fatalf("no rect for pane %q in grid mode", id)
		}
		rows[r.Y] = true
		cols[r.X] = true
	}
	if len(rows) != 2 || len(cols) != 2 {
		t.Fatalf("4 panes occupy %d distinct rows and %d columns, want 2x2", len(rows), len(cols))
	}
	// The sidebar keeps its column beside the grid.
	if _, ok := m.rects[sidebarLeafID]; !ok {
		t.Fatal("grid mode dropped the sidebar")
	}
}

func TestTuiGridRebalancesOnClose(t *testing.T) {
	m := newTestTuiModel()
	m.grid = true
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 41})
	for _, id := range []string{"a", "b", "c", "d"} {
		m, _ = update(m, paneOpenedMsg{id: id})
	}
	m.closePane("d")

	rows := map[int]bool{}
	for _, id := range []string{"a", "b", "c"} {
		r, ok := m.rects[id]
		if !ok {
			t.Fatalf("pane %q lost its rect after a rebalance", id)
		}
		rows[r.Y] = true
	}
	// 3 panes is ceil(sqrt(3)) = 2 columns, so two rows.
	if len(rows) != 2 {
		t.Fatalf("3 panes occupy %d rows, want 2 after rebalancing", len(rows))
	}
}

func TestTuiFocusRequestOnUnknownPaneShowsStatusNotPanic(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, focusRequestMsg{id: "never-opened"})
	if m.focused == "never-opened" {
		t.Fatal("focused an ID that was never opened as a pane in this tui")
	}
	if m.status == "" {
		t.Fatal("expected a status message explaining why focus didn't move")
	}
}

// TestTuiMouseClickFocusesPaneUnderCursor is Plan_B's mouse requirement:
// clicking a pane moves focus to it, and clicking back into the sidebar
// moves focus there.
func TestTuiMouseClickFocusesPaneUnderCursor(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = update(m, paneOpenedMsg{id: "agent1"})
	m.focused = sidebarLeafID

	paneRect := m.rects["agent1"]
	m, _ = update(m, tea.MouseMsg{
		X: paneRect.X + 2, Y: paneRect.Y + 2,
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft,
	})
	if m.focused != "agent1" {
		t.Fatalf("focused = %q after clicking inside agent1's rect, want agent1", m.focused)
	}

	sideRect := m.rects[sidebarLeafID]
	m, _ = update(m, tea.MouseMsg{
		X: sideRect.X + 1, Y: sideRect.Y + 1,
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft,
	})
	if m.focused != sidebarLeafID {
		t.Fatalf("focused = %q after clicking the sidebar, want the sidebar", m.focused)
	}
}

// TestTuiMouseClickLeavesCommandMode: a click is an unambiguous "I mean
// this pane", so it should not leave the user typing commands at it.
func TestTuiMouseClickLeavesCommandMode(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = update(m, paneOpenedMsg{id: "agent1"})
	m = enterCommandMode(t, m)

	r := m.rects["agent1"]
	m, _ = update(m, tea.MouseMsg{
		X: r.X + 2, Y: r.Y + 2,
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft,
	})
	if m.mode != modeInsert {
		t.Fatal("clicking a pane should return to insert mode")
	}
}

// TestTuiMouseWheelReachesSidebarWithLocalCoordinates proves the
// translation from screen cells to the sidebar's own frame: the sidebar
// hit-tests against its content area, so an untranslated event would
// select the wrong row (or none).
func TestTuiMouseWheelReachesSidebarWithLocalCoordinates(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = update(m, paneOpenedMsg{id: "agent1"})
	m.sidebar.sessions = []proto.SessionInfo{{ID: "a"}, {ID: "b"}}

	r := m.rects[sidebarLeafID]
	m, _ = update(m, tea.MouseMsg{
		X: r.X + 1, Y: r.Y + 3,
		Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown,
	})
	if m.sidebar.selected != 1 {
		t.Fatalf("sidebar selected = %d after a wheel-down over it, want 1", m.sidebar.selected)
	}
}

// TestTuiKeyForwardedToFocusedSurfaceReachesDaemon proves keys typed while
// a surface pane has focus actually reach POST /surfaces/input — the
// point of the whole key-routing path.
func TestTuiKeyForwardedToFocusedSurfaceReachesDaemon(t *testing.T) {
	var gotBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/surfaces/input", func(w http.ResponseWriter, r *http.Request) {
		var req proto.SurfaceInputRequest
		json.NewDecoder(r.Body).Decode(&req)
		gotBody = req.Data
	})
	mockDaemon(t, mux)

	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.panes["agent1"] = &tuiSurfacePane{id: "agent1", frames: make(chan proto.CellsFrame, 1)}
	m.focused = "agent1"

	_, cmd := press(m, "a")
	if cmd == nil {
		t.Fatal("expected a Cmd forwarding the keypress")
	}
	cmd() // run it synchronously — it's just an HTTP POST

	if string(gotBody) != "a" {
		t.Fatalf("daemon received input %q, want %q", gotBody, "a")
	}
}

// TestTuiCommandModeKeysDoNotReachThePane is the other half of the mode
// contract: in command mode, keys are wmux's, never the pty's.
func TestTuiCommandModeKeysDoNotReachThePane(t *testing.T) {
	var reached bool
	mux := http.NewServeMux()
	mux.HandleFunc("/surfaces/input", func(w http.ResponseWriter, r *http.Request) { reached = true })
	mockDaemon(t, mux)

	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = update(m, paneOpenedMsg{id: "agent1"})
	m = enterCommandMode(t, m)

	if _, cmd := press(m, "j"); cmd != nil {
		cmd()
	}
	if reached {
		t.Fatal("a command-mode key was forwarded to the pane's pty")
	}
}

// TestTuiFullOpenFlowEndToEnd drives the exact real user sequence — n,
// type a cwd, enter, type a command, enter — running every returned
// tea.Cmd for real (against a mock daemon) instead of just inspecting
// state after one hop.
func TestTuiFullOpenFlowEndToEnd(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/surfaces", func(w http.ResponseWriter, r *http.Request) {
		var req proto.NewSurfaceRequest
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(proto.SessionInfo{ID: req.ID, Cwd: req.Cwd, Surface: true})
	})
	mockDaemon(t, mux)

	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})

	// runToQuiescence feeds msg through Update, then keeps running and
	// re-feeding whatever Cmd comes back until nothing more is pending —
	// exactly what bubbletea's real event loop does, just synchronous.
	runToQuiescence := func(msg tea.Msg) {
		var cmd tea.Cmd
		m, cmd = update(m, msg)
		for cmd != nil {
			result := cmd()
			if result == nil {
				return
			}
			m, cmd = update(m, result)
		}
	}

	runToQuiescence(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if m.sidebar.mode != modePromptCwd {
		t.Fatalf("after n: sidebar mode = %v, want modePromptCwd", m.sidebar.mode)
	}
	runToQuiescence(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(".")})
	runToQuiescence(tea.KeyMsg{Type: tea.KeyEnter})
	if m.sidebar.mode != modePromptCmd {
		t.Fatalf("after cwd+enter: sidebar mode = %v, want modePromptCmd", m.sidebar.mode)
	}
	runToQuiescence(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("echo hi")})
	runToQuiescence(tea.KeyMsg{Type: tea.KeyEnter})

	if len(m.panes) != 1 {
		t.Fatalf("panes = %d, want 1 after completing the full n/cwd/cmd flow", len(m.panes))
	}
	leaves := m.layout.Leaves()
	if len(leaves) != 2 {
		t.Fatalf("layout leaves = %v, want sidebar + the new pane", leaves)
	}
	if m.focused == sidebarLeafID {
		t.Fatal("focus should have moved to the newly opened pane")
	}
}

// TestTuiKeyGoesToSidebarWhenSidebarFocused is the other half of the
// routing contract: with the sidebar focused, a key must reach the
// sidebar's own Update (navigation), not any surface.
func TestTuiKeyGoesToSidebarWhenSidebarFocused(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.sidebar.sessions = []proto.SessionInfo{{ID: "a"}, {ID: "b"}}

	if m.sidebar.selected != 0 {
		t.Fatalf("selected = %d, want 0 before navigating", m.sidebar.selected)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.sidebar.selected != 1 {
		t.Fatalf("selected = %d after down, want 1 (key reached the sidebar)", m.sidebar.selected)
	}
}

// TestTuiToggleSidebarKeepsAtLeastOnePaneVisible: hiding the session list
// with nothing else on screen would leave a blank terminal.
func TestTuiToggleSidebarKeepsAtLeastOnePaneVisible(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m = enterCommandMode(t, m)
	m, _ = press(m, "b")
	if m.sidebarHidden {
		t.Fatal("sidebar hidden with no panes open — nothing would be left on screen")
	}

	m, _ = update(m, paneOpenedMsg{id: "agent1"}) // opening a pane returns to insert mode
	m = enterCommandMode(t, m)
	m, _ = press(m, "b")
	if !m.sidebarHidden {
		t.Fatal("sidebar should hide once a pane can take its place")
	}
	if m.layout.Has(sidebarLeafID) {
		t.Fatal("hidden sidebar is still a leaf in the layout tree")
	}

	// Still in command mode — b is sticky like every other command key.
	m, _ = press(m, "b")
	if m.sidebarHidden || !m.layout.Has(sidebarLeafID) {
		t.Fatal("sidebar did not come back on a second toggle")
	}
}
