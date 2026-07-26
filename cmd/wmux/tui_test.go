package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/peterkure3/wmux/internal/client"
	"github.com/peterkure3/wmux/internal/proto"
)

// newTestTuiModel mirrors cmdTui's construction without touching the
// network — Update/View are driven directly, the same pattern
// sidebarui_verify_test.go already established for sidebarModel.
func newTestTuiModel() tuiModel {
	return tuiModel{
		layout:  NewLeaf(tuiSidebarLeafID),
		focused: tuiSidebarLeafID,
		panes:   make(map[string]*tuiSurfacePane),
		sidebar: sidebarModel{
			unread:  map[string]unreadNote{},
			ti:      textinput.New(),
			help:    newHelpModel(),
			events:  make(chan proto.Event, 1),
			onFocus: tuiSidebarFocusHook,
			onOpen:  tuiSidebarOpenHook,
			onClose: tuiSidebarCloseHook,
		},
	}
}

func update(m tuiModel, msg tea.Msg) (tuiModel, tea.Cmd) {
	mm, cmd := m.Update(msg)
	return mm.(tuiModel), cmd
}

func TestTuiInitialStateIsSidebarOnly(t *testing.T) {
	m := newTestTuiModel()
	leaves := m.layout.Leaves()
	if len(leaves) != 1 || leaves[0] != tuiSidebarLeafID {
		t.Fatalf("Leaves() = %v, want just the sidebar", leaves)
	}
	if m.focused != tuiSidebarLeafID {
		t.Fatalf("focused = %q, want sidebar", m.focused)
	}
}

func TestTuiWindowSizeComputesRectsAndSidebarSize(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})

	r, ok := m.rects[tuiSidebarLeafID]
	if !ok {
		t.Fatal("no rect computed for the sidebar leaf")
	}
	if r != (Rect{0, 0, 100, 39}) { // height-1 for the footer line
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
	// A pane split off the sidebar should give the sidebar only ~20% of
	// its former width (matching `wmux sidebar --with`'s convention).
	sidebarRect := m.rects[tuiSidebarLeafID]
	if sidebarRect.W >= 50 {
		t.Fatalf("sidebar rect width = %d, want roughly 20%% of 100 after splitting off agent1", sidebarRect.W)
	}
	if cmd == nil {
		t.Fatal("paneOpenedMsg should return a Cmd (at least waitPaneFrame)")
	}
}

func TestTuiPrefixKeyEntersCommandModeAndConsumesOneKey(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})

	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyCtrlB})
	if !m.prefix {
		t.Fatal("ctrl+b did not enter prefix mode")
	}
	if cmd != nil {
		t.Fatal("ctrl+b itself should not produce a Cmd")
	}

	// The next key is consumed as a command (q -> quit), not forwarded
	// anywhere, and prefix mode clears.
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if m.prefix {
		t.Fatal("prefix mode should clear after the next keypress")
	}
	if cmd == nil {
		t.Fatal("prefix+q should return tea.Quit")
	}
}

func TestTuiPrefixTabCyclesFocus(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = update(m, paneOpenedMsg{id: "agent1"})
	m, _ = update(m, paneOpenedMsg{id: "agent2"})
	// After two opens focus already sits on agent2, per paneOpenedMsg's
	// own "focus the pane you just opened" rule.
	if m.focused != "agent2" {
		t.Fatalf("focused = %q, want agent2", m.focused)
	}

	seen := map[string]bool{m.focused: true}
	for i := 0; i < 3; i++ {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlB})
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
		seen[m.focused] = true
	}
	for _, id := range []string{tuiSidebarLeafID, "agent1", "agent2"} {
		if !seen[id] {
			t.Errorf("tab cycling never visited %q", id)
		}
	}
}

func TestTuiPrefixMoveFocusUsesNeighbor(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = update(m, paneOpenedMsg{id: "agent1"}) // splits right of the sidebar

	// Focus is on agent1 (just opened); prefix+left should land back on
	// the sidebar, which is geometrically to its left.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlB})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.focused != tuiSidebarLeafID {
		t.Fatalf("focused = %q after prefix+left, want the sidebar", m.focused)
	}
}

func TestTuiClosePaneRemovesFromLayoutAndRefocuses(t *testing.T) {
	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = update(m, paneOpenedMsg{id: "agent1"})

	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyCtrlB})
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if _, ok := m.panes["agent1"]; ok {
		t.Fatal("agent1 still in m.panes after prefix+x")
	}
	leaves := m.layout.Leaves()
	if len(leaves) != 1 || leaves[0] != tuiSidebarLeafID {
		t.Fatalf("Leaves() = %v, want just the sidebar after closing the only other pane", leaves)
	}
	if m.focused != tuiSidebarLeafID {
		t.Fatalf("focused = %q, want the sidebar after its only sibling closed", m.focused)
	}
	if cmd == nil {
		t.Fatal("closing a real pane should return Cmds (recompute + close)")
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

// TestTuiKeyForwardedToFocusedSurfaceReachesDaemon proves keys typed while
// a surface pane has focus actually reach POST /surfaces/input — the
// point of the whole key-routing path — by pointing dc at a mock server
// and running the Cmd handleKey returns.
func TestTuiKeyForwardedToFocusedSurfaceReachesDaemon(t *testing.T) {
	var gotBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/identify", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(proto.IdentifyResponse{App: "wmuxd", Protocol: proto.ProtocolVersion})
	})
	mux.HandleFunc("/surfaces/input", func(w http.ResponseWriter, r *http.Request) {
		var req proto.SurfaceInputRequest
		json.NewDecoder(r.Body).Decode(&req)
		gotBody = req.Data
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	old := dc
	dc = client.New(srv.URL, "test-tok")
	defer func() { dc = old }()

	m := newTestTuiModel()
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.panes["agent1"] = &tuiSurfacePane{id: "agent1", frames: make(chan proto.CellsFrame, 1)}
	m.focused = "agent1"

	_, cmd := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if cmd == nil {
		t.Fatal("expected a Cmd forwarding the keypress")
	}
	cmd() // run it synchronously — it's just an HTTP POST

	if string(gotBody) != "a" {
		t.Fatalf("daemon received input %q, want %q", gotBody, "a")
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
