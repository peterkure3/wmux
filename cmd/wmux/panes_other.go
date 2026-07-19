//go:build !windows

package main

// findWindowForPID is Windows-only — non-Windows daemons/CLIs are dev
// builds only (see NOTES.md); there's no wt.exe pane to find here.
func findWindowForPID(pid int) (hwnd uintptr, title string, ok bool) {
	return 0, "", false
}

// wtPanesByTitle is Windows-only for the same reason.
func wtPanesByTitle(ids []string) map[string]string {
	return nil
}
