//go:build !windows

package cli

// enableVTOutput is a no-op outside Windows — Unix terminals process
// escape sequences unconditionally.
func enableVTOutput() error { return nil }
