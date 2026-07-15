//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	user32Panes                  = syscall.NewLazyDLL("user32.dll")
	procEnumWindows              = user32Panes.NewProc("EnumWindows")
	procGetWindowThreadProcessId = user32Panes.NewProc("GetWindowThreadProcessId")
	procGetWindowTextW           = user32Panes.NewProc("GetWindowTextW")
	procIsWindowVisible          = user32Panes.NewProc("IsWindowVisible")
)

// findWindowForPID enumerates top-level windows for one owned by pid,
// preferring a visible match — a native agent process normally owns
// exactly one (its console, or the pane hosting it). This is the only
// "does this session actually have a live pane" signal available without
// wt.exe's own (nonexistent) introspection API.
func findWindowForPID(pid int) (hwnd uintptr, title string, ok bool) {
	var found uintptr
	var foundTitle string

	cb := syscall.NewCallback(func(h uintptr, _ uintptr) uintptr {
		var winPID uint32
		procGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&winPID)))
		if winPID != uint32(pid) {
			return 1 // BOOL TRUE: keep enumerating
		}
		visible, _, _ := procIsWindowVisible.Call(h)
		if visible == 0 {
			return 1 // same pid but not visible (e.g. a hidden conhost); keep looking
		}
		buf := make([]uint16, 512)
		n, _, _ := procGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		found = h
		foundTitle = syscall.UTF16ToString(buf[:n])
		return 0 // BOOL FALSE: stop enumerating, found it
	})

	procEnumWindows.Call(cb, 0)
	if found == 0 {
		return 0, "", false
	}
	return found, foundTitle, true
}
