//go:build windows

package daemon

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobHandle wraps a Windows Job Object holding one session's process tree.
//
// Why a job object rather than enumerating and killing PIDs: `wmux close`
// needs to end the agent, but the daemon's own child is frequently a
// launcher (`cmd.exe /c claude`, `wsl.exe ...`) rather than the agent
// itself. TerminateProcess on the root leaves the real agent running as an
// orphan, still holding the repo and still listening on its ports. Walking
// processTree() and killing each PID is racy in the other direction — a
// child created between the enumeration and the kill escapes.
//
// A job object closes both holes: descendants are added to the job
// automatically as they are created, and TerminateJobObject stops every
// member atomically.
//
// The zero value is invalid and safe: valid() reports false and every
// method is a no-op, which is what a restored session (whose job died with
// the previous daemon process) and a failed CreateJobObject both get.
type jobHandle struct {
	h windows.Handle
}

func (j jobHandle) valid() bool { return j.h != 0 }

// newJob creates a job object and assigns proc to it.
//
// killOnClose sets JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, which kills every
// member the moment the last handle to the job closes — including when
// wmuxd exits or crashes. That is right for surfaces, which already cannot
// survive a daemon restart (their ConPTY dies with the daemon) and whose
// children would otherwise leak silently. It is wrong for Spawn-mode
// sessions, which load() is explicitly designed to re-adopt across a
// restart by re-checking their PID — turning it on there would kill every
// tracked agent on `wmux update`.
//
// Residual race: the process is assigned after CreateProcess has returned,
// so a child spawned in that window (microseconds) escapes the job. Closing
// it properly means CREATE_SUSPENDED plus a ResumeThread on the primary
// thread, which os/exec does not expose a handle for; for the launchers
// wmux actually spawns — which take milliseconds to reach their own
// CreateProcess — the window is not reachable in practice.
func newJob(proc *os.Process, killOnClose bool) (jobHandle, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return jobHandle{}, fmt.Errorf("CreateJobObject: %w", err)
	}

	if killOnClose {
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(
			h,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		); err != nil {
			windows.CloseHandle(h)
			return jobHandle{}, fmt.Errorf("SetInformationJobObject: %w", err)
		}
	}

	// PROCESS_SET_QUOTA is what AssignProcessToJobObject actually requires;
	// PROCESS_TERMINATE is needed for the job to be able to kill it later.
	ph, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(proc.Pid),
	)
	if err != nil {
		windows.CloseHandle(h)
		return jobHandle{}, fmt.Errorf("OpenProcess(%d): %w", proc.Pid, err)
	}
	defer windows.CloseHandle(ph)

	if err := windows.AssignProcessToJobObject(h, ph); err != nil {
		windows.CloseHandle(h)
		return jobHandle{}, fmt.Errorf("AssignProcessToJobObject(%d): %w", proc.Pid, err)
	}

	return jobHandle{h: h}, nil
}

// terminate kills every process in the job, then releases the handle.
// Safe to call on an invalid handle and safe to call twice.
func (j jobHandle) terminate() error {
	if !j.valid() {
		return nil
	}
	// Exit code 1: these processes were killed, not asked to leave.
	err := windows.TerminateJobObject(j.h, 1)
	windows.CloseHandle(j.h)
	if err != nil {
		return fmt.Errorf("TerminateJobObject: %w", err)
	}
	return nil
}

// release drops the daemon's handle without killing anything — used on the
// normal exit path, where the process is already gone and only the handle
// needs reclaiming. On a killOnClose job this *is* the kill.
func (j jobHandle) release() {
	if j.valid() {
		windows.CloseHandle(j.h)
	}
}
