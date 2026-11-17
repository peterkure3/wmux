//go:build windows

package cli

import "syscall"

const detachedProcess = 0x00000008

// A direct detached process has no console and never opens a command window.
// Only the invoking wmux process prints the startup notice in its terminal.
func startDaemonDetached(path string) error {
	cmd, log, err := detachedDaemonCommand(path)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess,
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
