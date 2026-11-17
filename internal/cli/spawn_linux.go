//go:build linux

package cli

import "syscall"

// The daemon is launched on demand in a new session, without a controlling TTY.
func startDaemonDetached(path string) error {
	cmd, log, err := detachedDaemonCommand(path)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
