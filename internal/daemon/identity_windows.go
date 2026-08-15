//go:build windows

package daemon

import (
	"syscall"
	"unsafe"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procQueryFullImage  = kernel32.NewProc("QueryFullProcessImageNameW")
	procGetProcessTimes = kernel32.NewProc("GetProcessTimes")
)

// implProcessIdentity reads a process's creation time and executable path
// via OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION) — the same access the
// liveness probe uses, so no elevated privilege is required for any
// process owned by this user. dwFlags=0 returns the image name in native
// (device) form; that stable full path is all the identity comparison
// needs, and it does not require the win32 path form.
func implProcessIdentity(pid int) (procIdent, bool) {
	if pid <= 0 {
		return procIdent{}, false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return procIdent{}, false
	}
	defer syscall.CloseHandle(h)

	var creation, exit, kernel, user syscall.Filetime
	// Pass non-nil for all five: this Go toolchain's syscall wrapper crashes
	// (access violation) when GetProcessTimes receives nil pointers.
	if err := syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return procIdent{}, false
	}

	var buf [32768]uint16
	n := uint32(len(buf))
	r, _, _ := procQueryFullImage.Call(
		uintptr(h), uintptr(0),
		uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&buf[0])),
	)
	if r == 0 {
		return procIdent{}, false
	}
	return procIdent{
		startTime: creation.Nanoseconds(),
		exe:       syscall.UTF16ToString(buf[:n]),
	}, true
}
