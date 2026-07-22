package main

import (
	"fmt"
	"strings"
	"time"
)

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
	sep := styleDim.Render(strings.Repeat("─", w))

	var b strings.Builder
	b.WriteString(styleTitle.Render(padTrunc(" wmux", w)) + "\n" + sep + "\n")

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
		// styleOffline.Render is nested inside styleDim's own Render below: its
		// embedded reset code means the theme background doesn't carry across
		// the few trailing pad columns after this substring. Cosmetic-only,
		// and only visible with a --background theme while wmuxd is down.
		summary += " · " + styleOffline.Render("wmuxd offline")
	}
	b.WriteString(styleDim.Render(padTrunc(summary, w)) + "\n")

	// Context line: active prompt/confirmation, else last action result.
	switch m.mode {
	case modeConfirmClose:
		b.WriteString(styleInverse.Render(padTrunc(fmt.Sprintf(" close %s? y/n", m.pendingID), w)) + "\n")
	case modePromptCwd, modePromptCmd:
		b.WriteString(styleInverse.Render(padTrunc(m.ti.View(), w)) + "\n")
	default:
		b.WriteString(styleDim.Render(padTrunc(" "+m.status, w)) + "\n")
	}

	b.WriteString(styleDim.Render(padTrunc(" "+m.help.View(sidebarKeys), w)))
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
		dot, dotStyle := "●", styleRunning
		if !s.Running {
			dot, dotStyle = "○", styleExited
		}
		tag := ""
		if !s.Native {
			tag = " wsl"
		}

		padded := []rune(padTrunc(fmt.Sprintf("%s%s %s  %s%s", marker, dot, s.ID, s.Branch, tag), w))
		var line1 string
		switch {
		case len(padded) < 3:
			// Pathologically narrow pane — not enough room to slice out the
			// marker/dot cleanly, just render the plain text.
			line1 = string(padded)
		case active.cursorReverse && i == m.selected:
			// gradient: a solid rowColors bar instead of the classic reverse
			// swap, matching the tui-demo reference's selected-row look.
			line1 = selectedBarStyle(i).Render(string(padded))
		case i == m.selected:
			line1 = styleInverse.Render(string(padded))
		default:
			// Three independently-rendered segments (marker, dot, rest) so
			// the dot keeps its state color while the rest of the row still
			// carries the theme's background/item color — nesting one
			// Render() inside another would lose the outer background after
			// the inner segment's own trailing reset code.
			base := rowStyle(i)
			prefix, dotRune, suffix := string(padded[:2]), string(padded[2:3]), string(padded[3:])
			line1 = base.Render(prefix) + dotStyle.Render(dotRune) + base.Render(suffix)
		}
		lines = append(lines, line1)
		owner = append(owner, i)

		ports := ""
		for _, p := range s.Ports {
			ports += fmt.Sprintf(" :%d", p)
		}
		lines = append(lines, styleDim.Render(padTrunc("    "+leftTrunc(s.Cwd, w-8-len(ports))+ports, w)))
		owner = append(owner, i)

		if hasNote && len(lines) < avail {
			age := shortAge(time.Since(note.at))
			body := strings.ReplaceAll(note.body, "\n", " ")
			lines = append(lines, styleUnread.Render(padTrunc(fmt.Sprintf("    ✉ %s  %s", body, age), w)))
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

// promptWidth is the textinput field width for the sidebar's cwd/cmd
// prompts — the pane width minus room for the "▌ cwd> " prompt prefix.
func promptWidth(w int) int {
	if w == 0 {
		w = 30
	}
	if w -= 8; w < 4 {
		w = 4
	}
	return w
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
