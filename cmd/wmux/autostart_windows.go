//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// autostartTaskName is the Task Scheduler task wmuxd runs under. Named
// distinctly (not just "wmuxd") so it's unambiguous in Task Scheduler's UI
// among unrelated tasks.
const autostartTaskName = "wmux-wmuxd"

// installAutostart registers a Task Scheduler task that starts wmuxd at
// logon, in the current user's own context (/RL LIMITED — no elevation,
// same privilege level `wmux update`'s startDaemonDetached already runs
// wmuxd with). /F overwrites a stale registration from a previous install
// instead of erroring.
//
// schtasks needs the /TR value's path double-quoted as literal characters
// within the argument (not just OS-level argv quoting) when the path
// contains spaces — this isn't shelled through cmd.exe, so the quotes
// below are what schtasks itself expects to find and store.
func installAutostart(wmuxdPath string) error {
	out, err := exec.Command("schtasks", "/Create",
		"/TN", autostartTaskName,
		"/TR", fmt.Sprintf(`"%s"`, wmuxdPath),
		"/SC", "ONLOGON",
		"/RL", "LIMITED",
		"/F",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks /Create failed: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func uninstallAutostart() error {
	out, err := exec.Command("schtasks", "/Delete", "/TN", autostartTaskName, "/F").CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "cannot find") || strings.Contains(msg, "does not exist") {
			return nil // nothing to remove
		}
		return fmt.Errorf("schtasks /Delete failed: %v\n%s", err, msg)
	}
	return nil
}

func printAutostartStatus() error {
	out, err := exec.Command("schtasks", "/Query", "/TN", autostartTaskName, "/FO", "LIST", "/V").CombinedOutput()
	if err != nil {
		fmt.Println("wmux autostart: not installed")
		return nil
	}
	fmt.Print(string(out))
	return nil
}
