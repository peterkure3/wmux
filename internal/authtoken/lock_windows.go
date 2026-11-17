//go:build windows

package authtoken

import (
	"os"

	"golang.org/x/sys/windows"
)

// An OS lock is released even if a provisioning process crashes. Keep the
// sidecar file: unlinking it could split concurrent callers across two locks.
func lockToken(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	o := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, o); err != nil {
		f.Close()
		return nil, err
	}
	return func() { windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, o); f.Close() }, nil
}
