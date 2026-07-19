package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// updateProgress is the step-counted progress bar for `wmux update`. Steps
// are coarse (pull, builds, stop, install, restart) because the slow parts
// are external commands whose own progress is opaque — the bar shows which
// phase is running, not a percentage of bytes. On a terminal it repaints a
// single line in place (plain \r + space padding, no ANSI, so it renders
// even in a classic conhost without VT processing); redirected output gets
// one plain line per step instead. The total can grow mid-run (addSteps)
// because whether the stop/restart phases apply is only known after the
// daemon probe. All methods are nil-safe so fatalUpdate and helpers can
// call them unconditionally.
type updateProgress struct {
	total   int
	started int // steps entered so far; started-1 are complete
	tty     bool
	lastLen int // width of the last painted line, for \r overwrite padding
}

func newUpdateProgress(total int) *updateProgress {
	return &updateProgress{
		total: total,
		tty:   term.IsTerminal(int(os.Stdout.Fd())),
	}
}

func (p *updateProgress) addSteps(n int) {
	if p != nil {
		p.total += n
	}
}

// step begins the next phase: the bar fills to the completed count and the
// label names what is running now.
func (p *updateProgress) step(label string) {
	if p == nil {
		return
	}
	p.started++
	p.paint(label, true)
}

// note repaints the current step's label without advancing the count —
// for in-step progress like download percentages. TTY only: on redirected
// output the one line per step is the log, and a note per chunk would
// spam it.
func (p *updateProgress) note(label string) {
	if p == nil || !p.tty {
		return
	}
	p.paint(label, false)
}

func (p *updateProgress) paint(label string, logLine bool) {
	const width = 20
	filled := 0
	if p.total > 0 {
		filled = width * (p.started - 1) / p.total
	}
	line := fmt.Sprintf("[%s%s] %d/%d %s",
		strings.Repeat("#", filled), strings.Repeat("-", width-filled),
		p.started, p.total, label)
	if !p.tty {
		if logLine {
			fmt.Println(line)
		}
		return
	}
	pad := ""
	if n := p.lastLen - len(line); n > 0 {
		pad = strings.Repeat(" ", n)
	}
	fmt.Printf("\r%s%s", line, pad)
	p.lastLen = len(line)
}

// clear wipes the in-place bar line so normal output (final message,
// warnings, errors) starts on a clean line. No-op when not a terminal.
func (p *updateProgress) clear() {
	if p == nil || !p.tty || p.lastLen == 0 {
		return
	}
	fmt.Printf("\r%s\r", strings.Repeat(" ", p.lastLen))
	p.lastLen = 0
}
