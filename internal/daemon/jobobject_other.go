//go:build !windows

package daemon

import "os"

// jobHandle is the non-Windows stand-in for a Windows Job Object (see
// jobobject_windows.go for what the real one is for).
//
// Nothing is needed here. The Linux build of wmuxd is the WSL-resident
// one, and every session it can close is reached through killInWSL, which
// already signals the process group. The zero value is the only value:
// valid() is always false, so Close falls through to its single-PID path
// on the rare non-Windows Spawn-mode session (local dev/testing of the
// daemon itself).
type jobHandle struct{}

func (jobHandle) valid() bool      { return false }
func (jobHandle) terminate() error { return nil }
func (jobHandle) release()         {}

func newJob(proc *os.Process, killOnClose bool) (jobHandle, error) {
	return jobHandle{}, nil
}
