//go:build !windows

package daemon

import (
	"os/exec"
	"syscall"
)

// hideConsole is a no-op off Windows — Unix children don't allocate
// console windows. See exec_windows.go for why this exists.
func hideConsole(cmd *exec.Cmd) {}

// processAliveNative probes PID existence with signal 0 — no process is
// touched, the kernel just checks the target exists. EPERM means it
// exists but belongs to someone else, which still counts as alive.
func processAliveNative(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
