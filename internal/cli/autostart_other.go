//go:build !windows && !linux

package cli

import "errors"

// Task Scheduler registration is Windows-specific mechanics. Linux has its
// own real implementation in autostart_linux.go (systemd --user); this stub
// remains only for other non-Windows, non-Linux dev builds (e.g. darwin),
// which have no autostart mechanism wired up yet.
func installAutostart(wmuxdPath string) error {
	return errors.New("wmux autostart is Windows-only for now")
}

func uninstallAutostart() error {
	return errors.New("wmux autostart is Windows-only for now")
}

func printAutostartStatus() error {
	return errors.New("wmux autostart is Windows-only for now")
}
