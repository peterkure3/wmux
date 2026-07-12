//go:build !windows

package main

import "errors"

// The self-update flow renames locked .exe files and restarts wmuxd
// detached — all Windows-specific mechanics. The linux wmux binary that
// lives inside WSL is still updated manually (see MANUAL.md).
func startDaemonDetached(wmuxdPath string) error {
	return errors.New("wmux update is Windows-only for now")
}
