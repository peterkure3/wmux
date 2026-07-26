//go:build windows

package cli

import (
	"fmt"
	"os"
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
//
// /RL LIMITED is meant to avoid needing elevation at all, but some
// machines' local security policy restricts *creating* any scheduled task
// to admins regardless of the task's own run level — schtasks then fails
// with "Access is denied" even though nothing here asked for admin rights.
// runSchtasks retries through a real UAC prompt in exactly that case,
// rather than making the user go find an elevated terminal themselves.
func installAutostart(wmuxdPath string) error {
	return runSchtasks("schtasks /Create",
		"/Create",
		"/TN", autostartTaskName,
		"/TR", fmt.Sprintf(`"%s"`, wmuxdPath),
		"/SC", "ONLOGON",
		"/RL", "LIMITED",
		"/F",
	)
}

func uninstallAutostart() error {
	err := runSchtasks("schtasks /Delete", "/Delete", "/TN", autostartTaskName, "/F")
	if err != nil && (strings.Contains(err.Error(), "cannot find") || strings.Contains(err.Error(), "does not exist")) {
		return nil // nothing to remove
	}
	return err
}

// runSchtasks runs schtasks normally, and — only if that fails with
// "Access is denied" — retries once through a UAC-elevated relaunch of
// wmux itself (see runElevated). label is what the error message calls
// the operation ("schtasks /Create", "schtasks /Delete").
func runSchtasks(label string, args ...string) error {
	out, err := exec.Command("schtasks", args...).CombinedOutput()
	if err == nil {
		return nil
	}
	if !isAccessDenied(err, string(out)) {
		return fmt.Errorf("%s failed: %v\n%s", label, err, strings.TrimSpace(string(out)))
	}

	fmt.Fprintln(os.Stderr, "wmux autostart: access denied; requesting administrator privileges (a UAC prompt should appear)...")
	elevOut, code, elevErr := runElevated("__elevated-schtasks", args...)
	if elevErr != nil {
		return fmt.Errorf("%s failed: %v\n%s\nelevation also failed: %v", label, err, strings.TrimSpace(string(out)), elevErr)
	}
	if code != 0 {
		return fmt.Errorf("%s failed (elevated): exit %d\n%s", label, code, strings.TrimSpace(elevOut))
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
