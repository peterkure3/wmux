//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var procGetConsoleTitleW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleTitleW")

// consoleTitle returns this process's console window title. Inside a pane
// opened by `wmux pane`, wt.exe's --title flag sets it to the session ID
// (verified empirically) — which is how `wmux pane-exec` learns which
// pending pane spec is its own without wt.exe being able to pass it any
// arguments (see cmdPaneExec).
func consoleTitle() (string, error) {
	buf := make([]uint16, 512)
	n, _, err := procGetConsoleTitleW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return "", fmt.Errorf("GetConsoleTitleW: %v", err)
	}
	return syscall.UTF16ToString(buf[:n]), nil
}
