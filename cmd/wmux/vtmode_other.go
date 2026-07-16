//go:build !windows

package main

// enableVTOutput is a no-op outside Windows — Unix terminals process
// escape sequences unconditionally.
func enableVTOutput() error { return nil }
