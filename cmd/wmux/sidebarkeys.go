package main

import (
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
)

type sidebarKeyMap struct {
	Up      key.Binding
	Down    key.Binding
	Focus   key.Binding
	Close   key.Binding
	New     key.Binding
	Refresh key.Binding
	Quit    key.Binding
}

var sidebarKeys = sidebarKeyMap{
	Up:      key.NewBinding(key.WithKeys("up", "k")),
	Down:    key.NewBinding(key.WithKeys("down", "j")),
	Focus:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "focus")),
	Close:   key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "close")),
	New:     key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
	Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
}

// ShortHelp is the sidebar's one-line footer — same visible keys as before
// the bubbles/help swap.
func (k sidebarKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Focus, k.Close, k.New, k.Quit}
}

func (k sidebarKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Up, k.Down, k.Focus}, {k.Close, k.New, k.Refresh, k.Quit}}
}

// newHelpModel zeroes every help.Styles field: the sidebar wraps its whole
// footer line in styleDim itself, and help's default per-token coloring
// would inject ANSI codes that padTrunc's rune count can't see through,
// breaking the fixed-width layout the mouse hit-test relies on elsewhere.
func newHelpModel() help.Model {
	h := help.New()
	h.Styles = help.Styles{
		Ellipsis:       lipgloss.NewStyle(),
		ShortKey:       lipgloss.NewStyle(),
		ShortDesc:      lipgloss.NewStyle(),
		ShortSeparator: lipgloss.NewStyle(),
		FullKey:        lipgloss.NewStyle(),
		FullDesc:       lipgloss.NewStyle(),
		FullSeparator:  lipgloss.NewStyle(),
	}
	return h
}
