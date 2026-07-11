//go:build !windows

package main

import "errors"

// consoleTitle only means something under a Windows console session — the
// commands that need it (`wmux pane-exec`) are Windows-only by nature.
func consoleTitle() (string, error) {
	return "", errors.New("console title lookup is only available on Windows")
}
