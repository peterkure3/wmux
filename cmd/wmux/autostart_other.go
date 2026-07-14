//go:build !windows

package main

import "errors"

// Task Scheduler registration is Windows-specific mechanics — the linux
// wmux binary resident inside a WSL distro is a dev/testing build, not a
// deployment target that needs its own autostart (see MANUAL.md).
func installAutostart(wmuxdPath string) error {
	return errors.New("wmux autostart is Windows-only for now")
}

func uninstallAutostart() error {
	return errors.New("wmux autostart is Windows-only for now")
}

func printAutostartStatus() error {
	return errors.New("wmux autostart is Windows-only for now")
}
