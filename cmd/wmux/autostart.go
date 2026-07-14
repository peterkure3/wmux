// wmux autostart registers wmuxd to launch at Windows logon, so it
// survives reboots without a manual `wmuxd.exe` start — see NOTES.md's
// "Deployment topology" section: wmuxd has no autostart mechanism today
// and the daemon dying silently is a real, already-hit failure mode
// ("wmux pane: could not reach wmuxd").
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func cmdAutostart(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "wmux autostart: missing subcommand (install|uninstall|status)")
		os.Exit(1)
	}

	wmuxd, err := siblingWmuxd()
	if err != nil {
		fatalAutostart("%v", err)
	}

	switch args[0] {
	case "install":
		if err := installAutostart(wmuxd); err != nil {
			fatalAutostart("%v", err)
		}
		fmt.Printf("wmux autostart: installed — wmuxd starts at every logon (%s)\n", wmuxd)

		if !daemonRunning() {
			if err := startDaemonDetached(wmuxd); err != nil {
				fmt.Fprintf(os.Stderr, "wmux autostart: installed, but could not start wmuxd now: %v\n", err)
				return
			}
			fmt.Println("wmux autostart: wmuxd started")
		}
	case "uninstall":
		if err := uninstallAutostart(); err != nil {
			fatalAutostart("%v", err)
		}
		fmt.Println("wmux autostart: removed — wmuxd will no longer start at logon (currently running wmuxd, if any, is untouched)")
	case "status":
		if err := printAutostartStatus(); err != nil {
			fatalAutostart("%v", err)
		}
	default:
		fmt.Fprintf(os.Stderr, "wmux autostart: unknown subcommand %q (install|uninstall|status)\n", args[0])
		os.Exit(1)
	}
}

func fatalAutostart(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "wmux autostart: "+format+"\n", a...)
	os.Exit(1)
}

// siblingWmuxd resolves wmuxd.exe next to this wmux.exe — the same
// deployDir convention `wmux update` uses (see update.go).
func siblingWmuxd() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("could not resolve wmux's own path: %w", err)
	}
	wmuxd := filepath.Join(filepath.Dir(exe), "wmuxd.exe")
	if _, err := os.Stat(wmuxd); err != nil {
		return "", fmt.Errorf("wmuxd.exe not found at %s: %w", wmuxd, err)
	}
	return wmuxd, nil
}
