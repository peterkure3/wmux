//go:build !windows && !linux

package cli

import "errors"

// The self-update flow renames/replaces binaries and restarts wmuxd
// detached. Linux has its own real implementation in spawn_linux.go; this
// stub remains only for other non-Windows, non-Linux dev builds.
func startDaemonDetached(wmuxdPath string) error {
	return errors.New("wmux update is Windows-only for now")
}
