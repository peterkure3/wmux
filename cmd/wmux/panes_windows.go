//go:build windows

package main

import (
	"sync"
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

// enumState carries one findWindowForPID call's input/output through the
// EnumWindows callback. The callback itself is created exactly once (see
// enumWindowsCB): syscall.NewCallback allocations are never released and
// Windows caps them process-wide, so a per-call closure would exhaust the
// limit for any long-lived caller — the sidebar probes windows every couple
// of seconds for its whole lifetime.
type enumState struct {
	pid   uint32
	found uintptr
	title string
}

var (
	enumMu      sync.Mutex // one enumeration at a time; guards enumCur
	enumCur     *enumState
	enumCBOnce  sync.Once
	enumCBAddr  uintptr
)

func enumWindowsCB() uintptr {
	enumCBOnce.Do(func() {
		enumCBAddr = syscall.NewCallback(func(h uintptr, _ uintptr) uintptr {
			var winPID uint32
			procGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&winPID)))
			if winPID != enumCur.pid {
				return 1 // BOOL TRUE: keep enumerating
			}
			visible, _, _ := procIsWindowVisible.Call(h)
			if visible == 0 {
				return 1 // same pid but not visible (e.g. a hidden conhost); keep looking
			}
			buf := make([]uint16, 512)
			n, _, _ := procGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
			enumCur.found = h
			enumCur.title = syscall.UTF16ToString(buf[:n])
			return 0 // BOOL FALSE: stop enumerating, found it
		})
	})
	return enumCBAddr
}

// findWindowForPID enumerates top-level windows for one owned by pid,
// preferring a visible match — a native agent process normally owns
// exactly one (its console, or the pane hosting it). This is the only
// "does this session actually have a live pane" signal available without
// wt.exe's own (nonexistent) introspection API.
func findWindowForPID(pid int) (hwnd uintptr, title string, ok bool) {
	enumMu.Lock()
	defer enumMu.Unlock()

	st := &enumState{pid: uint32(pid)}
	enumCur = st
	procEnumWindows.Call(enumWindowsCB(), 0)
	enumCur = nil

	if st.found == 0 {
		return 0, "", false
	}
	return st.found, st.title, true
}
