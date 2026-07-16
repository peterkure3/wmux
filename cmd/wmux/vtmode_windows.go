//go:build windows

package main

import "golang.org/x/sys/windows"

// enableVTOutput turns on ENABLE_VIRTUAL_TERMINAL_PROCESSING for stdout so
// the escape sequences `wmux connect` writes (VT replays, the session's own
// output) render instead of printing literally. Windows Terminal has it on
// already; a classic conhost window does not.
func enableVTOutput() error {
	h := windows.Handle(windows.Stdout)
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return nil // stdout is not a console (redirected); nothing to enable
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return nil
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
}
