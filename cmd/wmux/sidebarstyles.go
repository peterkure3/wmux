package main

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Windows 11 logo palette — shared across every sidebar theme below.
const (
	deepBlue   = "#004fe1"
	skyBlue    = "#08a1f7"
	cyanColor  = "#03c1f4"
	brightCyan = "#09e0fe"
	whiteColor = "#ffffff"
	slate      = "#3a4a6b" // muted helper tone derived from the palette for dim/exited text
)

// sidebarTheme mirrors the tui-demo reference (charmbracelet/bubbletea +
// lipgloss task-board demo, themes midnight/frost/gradient): a named color
// set built from the Windows 11 logo palette. Sidebar has no rounded-border
// card like the demo (the WT pane edge already frames it) — background, when
// set, repaints every row's full width instead.
type sidebarTheme struct {
	name         string
	background   *string // nil = terminal default (dark); set = full-bleed repaint
	titleColor   string
	runningColor string // "session has a live process" dot color
	exitedColor  string // "session exited" dot + dim text color
	itemColor    string // default session ID/branch/cwd text
	subtleColor  string // separators, cwd line, summary, footer help
	unreadColor  string // unread-notification line
	offlineColor string // "wmuxd offline" warning — no warm color in this
	// palette, so it reuses an accent tone instead of
	// introducing an off-brand red/yellow.
	rowColors     []string // gradient only: cycles per session index instead of itemColor
	cursorReverse bool     // gradient only: selected row gets a solid rowColors bar (barFg on
	// rowColors background) instead of the classic Reverse(true) swap
	barFg string // gradient only: foreground against the rowColors bar
}

func strp(s string) *string { return &s }

var sidebarThemes = map[string]sidebarTheme{
	// Midnight: dark terminal background, palette used purely as accents —
	// the sidebar's original look, just themed.
	"midnight": {
		name:         "midnight",
		titleColor:   brightCyan,
		runningColor: cyanColor,
		exitedColor:  slate,
		itemColor:    whiteColor,
		subtleColor:  skyBlue,
		unreadColor:  brightCyan,
		offlineColor: brightCyan,
	},
	// Frost: light theme, white full-bleed background with deep blue/navy text.
	"frost": {
		name:         "frost",
		background:   strp(whiteColor),
		titleColor:   deepBlue,
		runningColor: cyanColor,
		exitedColor:  slate,
		itemColor:    "#12224d", // dark navy for body-text contrast on white
		subtleColor:  skyBlue,
		unreadColor:  deepBlue,
		offlineColor: deepBlue,
	},
	// Gradient: dark background, each session's ID/branch line cycles through
	// the four logo blues, with a solid highlight bar for the selected one.
	"gradient": {
		name:          "gradient",
		titleColor:    whiteColor,
		runningColor:  whiteColor,
		exitedColor:   slate,
		itemColor:     whiteColor,
		subtleColor:   skyBlue,
		unreadColor:   brightCyan,
		offlineColor:  whiteColor,
		rowColors:     []string{deepBlue, skyBlue, cyanColor, brightCyan},
		cursorReverse: true,
		barFg:         "#0b1220",
	},
}

// currentSidebarTheme picks the sidebar's theme: WMUX_THEME env var first
// (a scripting/one-off override — but a fresh WT pane inherits Windows
// Terminal's own process environment, not the shell the wmux CLI happened
// to be invoked from, so this often doesn't reach the pane that matters),
// then the name persisted by `wmux theme <name>` (themeConfigPath, read on
// disk every launch so it always applies regardless of environment
// inheritance), then midnight.
func currentSidebarTheme() sidebarTheme {
	if t, ok := sidebarThemes[os.Getenv("WMUX_THEME")]; ok {
		return t
	}
	if path := themeConfigPath(); path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if t, ok := sidebarThemes[strings.TrimSpace(string(b))]; ok {
				return t
			}
		}
	}
	return sidebarThemes["midnight"]
}

// active is resolved once at process start (env vars don't change mid-run),
// and every style below derives from it.
var active = currentSidebarTheme()

// withBG applies the active theme's full-bleed background, if it has one —
// each style wraps a whole padded-to-width line in one Render call, so a
// background repaints that row's entire width, not just its accent text.
func withBG(s lipgloss.Style) lipgloss.Style {
	if active.background != nil {
		return s.Background(lipgloss.Color(*active.background))
	}
	return s
}

var (
	styleTitle   = withBG(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(active.titleColor)))
	styleDim     = withBG(lipgloss.NewStyle().Foreground(lipgloss.Color(active.subtleColor)))
	styleInverse = withBG(lipgloss.NewStyle().Foreground(lipgloss.Color(active.itemColor))).Reverse(true)
	styleRunning = withBG(lipgloss.NewStyle().Foreground(lipgloss.Color(active.runningColor)))
	styleExited  = withBG(lipgloss.NewStyle().Foreground(lipgloss.Color(active.exitedColor)))
	styleOffline = withBG(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(active.offlineColor)))
	styleUnread  = withBG(lipgloss.NewStyle().Foreground(lipgloss.Color(active.unreadColor)))
)

// rowStyle is the base item style for session index i — cycling through
// active.rowColors for the gradient theme, flat itemColor otherwise.
func rowStyle(i int) lipgloss.Style {
	color := active.itemColor
	if active.rowColors != nil {
		color = active.rowColors[i%len(active.rowColors)]
	}
	return withBG(lipgloss.NewStyle().Foreground(lipgloss.Color(color)))
}

// selectedBarStyle is the gradient theme's solid highlight bar for the
// selected session's first line: rowColors[i] as background, barFg as
// foreground — the tui-demo reference's cursorReverse look.
func selectedBarStyle(i int) lipgloss.Style {
	bg := active.itemColor
	if active.rowColors != nil {
		bg = active.rowColors[i%len(active.rowColors)]
	}
	return lipgloss.NewStyle().Bold(true).Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(active.barFg))
}
