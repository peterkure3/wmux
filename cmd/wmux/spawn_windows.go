//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// DETACHED_PROCESS is not in the syscall package; raw constant rather than
// pulling in golang.org/x/sys for one flag (the repo has no external deps).
const detachedProcess = 0x00000008

// startDaemonDetached launches wmuxd fully detached from this process and
// its console, so the daemon survives `wmux update` exiting and the
// terminal tab closing. Logs go to ~/.wmux/wmuxd.log (next to state.json),
// the closest a detached process can get to the manual foreground-start
// convention in MANUAL.md.
func startDaemonDetached(wmuxdPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not resolve home dir for wmuxd.log: %w", err)
	}
	logDir := filepath.Join(home, ".wmux")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(logDir, "wmuxd.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd := exec.Command(wmuxdPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
