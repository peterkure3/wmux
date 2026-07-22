package main

import "github.com/charmbracelet/lipgloss"

// Sidebar styling, lipgloss instead of raw ANSI fragments. Each style wraps
// already-width-padded plain text — pad/truncate first, style last, so
// escape codes never enter the rune-counting in padTrunc/leftTrunc.
var (
	styleDim     = lipgloss.NewStyle().Faint(true)
	styleInverse = lipgloss.NewStyle().Reverse(true)
	styleRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // green
	styleExited  = lipgloss.NewStyle().Faint(true)
	styleOffline = lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // yellow
	styleUnread  = lipgloss.NewStyle().Foreground(lipgloss.Color("5")) // magenta
)
