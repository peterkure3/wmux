package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/peterkure3/wmux/internal/tui"
)

// cmdTheme prints or persists the TUI's theme. With no argument it prints
// the theme that's currently active (env override or persisted file) plus
// the full list; with a name it validates and persists it, taking effect
// on the next `wmux` launch — a running TUI resolves its styles once at
// startup and keeps them.
//
// The theme table itself lives in internal/tui, which is what renders
// with it; this command is a front end over tui.ThemeNames/ThemeSet.
func cmdTheme(args []string) {
	if len(args) == 0 {
		fmt.Printf("current theme: %s\n", tui.ThemeCurrent())
		fmt.Printf("available: %s\n", strings.Join(tui.ThemeNames(), ", "))
		return
	}
	name := args[0]
	if err := tui.ThemeSet(name); err != nil {
		fmt.Fprintf(os.Stderr, "wmux theme: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("theme set to %s — takes effect on the next 'wmux'\n", name)
}
