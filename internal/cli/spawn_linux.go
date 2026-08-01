//go:build linux

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// startDaemonDetached launches wmuxd fully detached from this process and
// its controlling terminal, so the daemon survives `wmux update` exiting
// and the terminal closing. Mirrors spawn_windows.go's convention: logs
// go to ~/.wmux/wmuxd.log (next to state.json).
//
// Setsid puts the child in a new session with no controlling terminal at
// all (the Unix equivalent of Windows's CREATE_NEW_PROCESS_GROUP +
// DETACHED_PROCESS) — a plain fork/exec without it would still hang up
// the child on SIGHUP when this process's terminal session ends.
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
