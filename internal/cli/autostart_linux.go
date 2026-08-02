//go:build linux

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// autostartUnitName is the systemd --user unit wmuxd runs under. Named
// distinctly (not just "wmuxd") for the same reason autostart_windows.go
// names its Task Scheduler task "wmux-wmuxd" — unambiguous among unrelated
// units in `systemctl --user list-units`.
const autostartUnitName = "wmux-wmuxd.service"

// unitPath returns the per-user systemd unit directory, honoring
// XDG_CONFIG_HOME the same way systemd itself does.
func unitPath() (string, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not resolve home directory: %w", err)
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "systemd", "user", autostartUnitName), nil
}

// installAutostart writes a systemd --user unit for wmuxd and enables it
// to start at login (and starts it immediately, mirroring `systemctl
// enable --now`). No elevation is needed — user units are entirely
// per-user, unlike the Windows Task Scheduler path this mirrors.
func installAutostart(wmuxdPath string) error {
	path, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("could not create systemd user unit dir: %w", err)
	}

	unit := fmt.Sprintf(`[Unit]
Description=wmux daemon (session/notification tracking for AI agent workflows)
After=default.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`, wmuxdPath)

	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("could not write unit file %s: %w", path, err)
	}

	if err := runSystemctl("daemon-reload"); err != nil {
		return err
	}
	// --now both enables (symlinks into default.target.wants) and starts
	// the unit in one call, so a fresh install doesn't need a separate
	// wmux-side spawn like the Windows path's startDaemonDetached does.
	if err := runSystemctl("enable", "--now", autostartUnitName); err != nil {
		return err
	}
	return nil
}

func uninstallAutostart() error {
	path, err := unitPath()
	if err != nil {
		return err
	}
	// disable --now stops and unlinks the unit; ignore "not loaded" since
	// that just means there's nothing to remove (parity with
	// autostart_windows.go treating a missing task as success).
	if err := runSystemctl("disable", "--now", autostartUnitName); err != nil &&
		!strings.Contains(err.Error(), "not loaded") &&
		!strings.Contains(err.Error(), "does not exist") &&
		!strings.Contains(err.Error(), "No such file") {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("could not remove unit file %s: %w", path, err)
	}
	return runSystemctl("daemon-reload")
}

func printAutostartStatus() error {
	out, err := exec.Command("systemctl", "--user", "status", "--no-pager", autostartUnitName).CombinedOutput()
	// systemctl status exits non-zero for a unit that's loaded-but-stopped
	// too, so only treat this as "not installed" if the unit is truly
	// unknown to systemd.
	if err != nil && strings.Contains(string(out), "could not be found") {
		fmt.Println("wmux autostart: not installed")
		return nil
	}
	fmt.Print(string(out))
	return nil
}

func runSystemctl(args ...string) error {
	full := append([]string{"--user"}, args...)
	out, err := exec.Command("systemctl", full...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user %s failed: %v\n%s",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
