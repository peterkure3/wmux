//go:build !windows

package main

import "errors"

// sendKeysToPID is Windows-only — see panes_other.go.
func sendKeysToPID(pid int, keys []string) error {
	return errors.New("wmux send-keys is Windows-only for now")
}
