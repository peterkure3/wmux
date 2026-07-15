//go:build !windows

package main

// findWindowForPID is Windows-only — non-Windows daemons/CLIs are dev
// builds only (see NOTES.md); there's no wt.exe pane to find here.
func findWindowForPID(pid int) (hwnd uintptr, title string, ok bool) {
	return 0, "", false
}
