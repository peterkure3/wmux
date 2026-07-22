package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// themeConfigPath is where `wmux theme <name>` persists the chosen sidebar
// theme — a plain-text file holding just the name, read fresh by
// currentSidebarTheme on every sidebar launch. Persisting to disk (rather
// than relying solely on WMUX_THEME) matters because a new WT pane
// inherits Windows Terminal's own process environment, not the shell the
// wmux CLI was invoked from — an env var set in one PowerShell prompt
// often never reaches the pane.
func themeConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".wmux", "theme")
}

func sortedThemeNames() []string {
	names := make([]string, 0, len(sidebarThemes))
	for n := range sidebarThemes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// cmdTheme prints or persists the sidebar's theme. With no argument it
// prints the theme that's currently active (env override or persisted
// file, same resolution as currentSidebarTheme) and the full list; with a
// name it validates against sidebarThemes and writes it to
// themeConfigPath, taking effect on the next 'wmux sidebar' launch (the
// running sidebar's colors are resolved once at its own startup, so an
// already-open pane keeps its look until reopened).
func cmdTheme(args []string) {
	names := sortedThemeNames()

	if len(args) == 0 {
		fmt.Printf("current theme: %s\n", currentSidebarTheme().name)
		fmt.Printf("available: %s\n", strings.Join(names, ", "))
		return
	}

	name := args[0]
	if _, ok := sidebarThemes[name]; !ok {
		fmt.Fprintf(os.Stderr, "wmux theme: unknown theme %q — available: %s\n", name, strings.Join(names, ", "))
		os.Exit(1)
	}

	path := themeConfigPath()
	if path == "" {
		fmt.Fprintln(os.Stderr, "wmux theme: could not resolve home directory")
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "wmux theme: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(path, []byte(name+"\n"), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "wmux theme: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("theme set to %s — takes effect next 'wmux sidebar'\n", name)
}
