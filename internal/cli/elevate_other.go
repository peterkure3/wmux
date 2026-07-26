//go:build !windows

package cli

import "os"

// cmdElevatedSchtasks's real implementation is Windows-only (see
// elevate_windows.go); this stub exists only so main.go's dispatch table
// compiles on the non-Windows dev build. It is never reachable there —
// nothing on that build ever invokes the hidden "__elevated-schtasks"
// subcommand, since autostart itself is Windows-only.
func cmdElevatedSchtasks(args []string) {
	os.Exit(2)
}
