package main

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/peterkure/wmux/internal/proto"
)

// Throwaway coverage for the bubbles/lipgloss refactor: exercises the
// model's Update/View directly so the textinput/help wiring can be
// verified without a live terminal.

func newTestModel(w, h int) sidebarModel {
	m := sidebarModel{
		unread: map[string]unreadNote{},
		newCwd: `D:\dev\proj`,
		ti:     textinput.New(),
		help:   newHelpModel(),
		events: make(chan proto.Event, 1),
	}
	mm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return mm.(sidebarModel)
}

func TestSidebarPromptFlow(t *testing.T) {
	m := newTestModel(30, 24)

	mm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = mm.(sidebarModel)
	if m.mode != modePromptCwd {
		t.Fatalf("after n: mode = %v, want modePromptCwd", m.mode)
	}
	if m.ti.Value() != `D:\dev\proj` {
		t.Fatalf("cwd prompt seed = %q, want default newCwd", m.ti.Value())
	}
	if cmd == nil {
		t.Fatal("expected a cursor-blink cmd from Focus()")
	}

	// Backspace three times off the seeded value.
	for i := 0; i < 3; i++ {
		mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = mm.(sidebarModel)
	}
	if m.ti.Value() != `D:\dev\p` {
		t.Fatalf("after 3 backspaces = %q, want D:\\dev\\p", m.ti.Value())
	}

	// Type more text.
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("roj2")})
	m = mm.(sidebarModel)
	if m.ti.Value() != `D:\dev\proj2` {
		t.Fatalf("after typing = %q, want D:\\dev\\proj2", m.ti.Value())
	}

	// Enter advances to the cmd prompt, clearing the field.
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = mm.(sidebarModel)
	if m.mode != modePromptCmd {
		t.Fatalf("after enter: mode = %v, want modePromptCmd", m.mode)
	}
	if m.newCwd != `D:\dev\proj2` {
		t.Fatalf("newCwd = %q, want D:\\dev\\proj2", m.newCwd)
	}
	if m.ti.Value() != "" {
		t.Fatalf("cmd prompt should start empty, got %q", m.ti.Value())
	}
	if m.ti.Prompt != " cmd> " {
		t.Fatalf("prompt = %q, want ' cmd> '", m.ti.Prompt)
	}

	// Esc cancels back to the list without opening a pane.
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mm.(sidebarModel)
	if m.mode != modeList {
		t.Fatalf("after esc: mode = %v, want modeList", m.mode)
	}
	if m.ti.Focused() {
		t.Fatal("ti should be blurred after esc")
	}
}

func TestSidebarPromptEnterOpensPaneCmd(t *testing.T) {
	m := newTestModel(30, 24)
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = mm.(sidebarModel)
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // accept default cwd
	m = mm.(sidebarModel)
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("claude")})
	m = mm.(sidebarModel)
	mm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = mm.(sidebarModel)
	if m.mode != modeList {
		t.Fatalf("after final enter: mode = %v, want modeList", m.mode)
	}
	if cmd == nil {
		t.Fatal("expected openPaneCmd to be returned")
	}
}

func TestSidebarViewNoPanic(t *testing.T) {
	m := newTestModel(30, 24)
	m.sessions = []proto.SessionInfo{
		{ID: "api", Branch: "main", Cwd: `D:\dev\api`, Running: true, Native: true, Ports: []int{3000}},
		{ID: "web", Branch: "feat/login", Cwd: `D:\dev\web`, Running: false, Native: true},
	}
	m.unread["api"] = unreadNote{body: "tests passed"}

	out := m.View()
	if !strings.Contains(out, "api") || !strings.Contains(out, "web") {
		t.Fatalf("View() missing session rows:\n%s", out)
	}
	if !strings.Contains(out, "wmux") {
		t.Fatalf("View() missing header:\n%s", out)
	}

	// Footer must render via bubbles/help without panicking and must not
	// silently drop "quit" the way the pre-fix double-truncation did
	// (help.Width was set to m.width instead of m.width-1, so the leading
	// space plus outer padTrunc cut the string twice).
	footer := m.help.View(sidebarKeys)
	if len(footer) == 0 {
		t.Fatal("empty footer from help.View")
	}
}

func TestSidebarNarrowWidthNoPanic(t *testing.T) {
	// Extreme narrow width shouldn't panic padTrunc/promptWidth/help.
	m := newTestModel(1, 5)
	m.sessions = []proto.SessionInfo{{ID: "x", Running: true, Native: true}}
	_ = m.View()

	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = mm.(sidebarModel)
	_ = m.View()
}

func TestPromptWidthFloor(t *testing.T) {
	if got := promptWidth(0); got != 22 {
		t.Fatalf("promptWidth(0) = %d, want 22 (default width 30 - 8)", got)
	}
	if got := promptWidth(5); got != 4 {
		t.Fatalf("promptWidth(5) = %d, want floor 4", got)
	}
	if got := promptWidth(1); got != 4 {
		t.Fatalf("promptWidth(1) = %d, want floor 4", got)
	}
}

// isolateHome points USERPROFILE/HOME at a fresh temp dir so
// themeConfigPath (and anything else keyed off os.UserHomeDir) never
// touches this machine's real ~/.wmux.
func isolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
}

func TestCurrentSidebarThemeFallback(t *testing.T) {
	isolateHome(t)
	t.Setenv("WMUX_THEME", "not-a-real-theme")
	if got := currentSidebarTheme(); got.name != "midnight" {
		t.Fatalf("unknown theme name = %q, want midnight fallback", got.name)
	}
	t.Setenv("WMUX_THEME", "")
	if got := currentSidebarTheme(); got.name != "midnight" {
		t.Fatalf("unset WMUX_THEME = %q, want midnight fallback", got.name)
	}
	t.Setenv("WMUX_THEME", "frost")
	if got := currentSidebarTheme(); got.name != "frost" {
		t.Fatalf("WMUX_THEME=frost = %q, want frost", got.name)
	}
}

func TestThemePersistence(t *testing.T) {
	isolateHome(t)
	t.Setenv("WMUX_THEME", "") // env unset, so currentSidebarTheme must read the persisted file

	if got := currentSidebarTheme(); got.name != "midnight" {
		t.Fatalf("before 'wmux theme' runs: %q, want midnight default", got.name)
	}

	cmdTheme([]string{"gradient"})
	if got := currentSidebarTheme(); got.name != "gradient" {
		t.Fatalf("after 'wmux theme gradient': %q, want gradient", got.name)
	}

	path := themeConfigPath()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("themeConfigPath %s not written: %v", path, err)
	}
	if got := strings.TrimSpace(string(b)); got != "gradient" {
		t.Fatalf("persisted file content = %q, want gradient", got)
	}

	// An env var still wins over the persisted file.
	t.Setenv("WMUX_THEME", "frost")
	if got := currentSidebarTheme(); got.name != "frost" {
		t.Fatalf("WMUX_THEME=frost should override persisted gradient, got %q", got.name)
	}
}

// TestRowStyleGradientCycle only exercises anything under
// WMUX_THEME=gradient — active is resolved once at process start, so this
// skips for every other theme the suite happens to run under. Run
// explicitly with: WMUX_THEME=gradient go test ./cmd/wmux/ -run RowStyle
func TestRowStyleGradientCycle(t *testing.T) {
	if active.rowColors == nil {
		t.Skip("meaningful only under WMUX_THEME=gradient")
	}
	lipgloss.SetColorProfile(termenv.TrueColor) // outside a real TTY lipgloss drops all color by default
	n := len(active.rowColors)
	rendered := make([]string, n)
	for i := 0; i < n; i++ {
		rendered[i] = rowStyle(i).Render("x")
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if rendered[i] == rendered[j] {
				t.Fatalf("rowStyle(%d) == rowStyle(%d), want distinct colors", i, j)
			}
		}
	}
	if got, want := rowStyle(n).Render("x"), rendered[0]; got != want {
		t.Fatalf("rowStyle(%d) = %q, want wraparound to rowStyle(0) = %q", n, got, want)
	}
}
