// Bridge to a WSL-resident wmuxd from the Windows-side CLI.
//
// On the two-daemon topology (Windows-native wmuxd for native agents, a
// WSL-resident one for WSL agents — see the skill/README's Step 0), plain
// `wmux pane` sessions register with the daemon *inside* the distro:
// WSL→Windows loopback doesn't cross the namespace boundary, so they can
// never appear in the Windows daemon's list. The Windows CLI used to be
// completely blind to them — `wmux list` empty, `wmux close` a 404 —
// while the sessions ran on. The only route that works from this side is
// Windows→WSL interop: shell into the distro and ask the wmux binary
// there. Best-effort by design: no wsl.exe, no wmux in the distro, or no
// daemon there all just mean no bridged results, never an error the local
// command trips over.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"os/exec"
	"strings"
	"time"

	"github.com/peterkure/wmux/internal/proto"
)

// wslBridgeTimeout caps every bridge shell-out: wsl.exe can hang well past
// any useful wait when the distro is booting or wedged, and the bridge is
// a bonus, not the command's main job.
const wslBridgeTimeout = 5 * time.Second

// wslDaemonSessions returns the WSL-resident daemon's session list, or nil
// if there is no reachable one (any failure at any layer). Default distro
// only — that's where pane-exec's inner `wmux attach` lands too.
func wslDaemonSessions() []proto.SessionInfo {
	if runtime.GOOS != "windows" {
		return nil // a WSL-resident daemon IS the local one here
	}
	ctx, cancel := context.WithTimeout(context.Background(), wslBridgeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "wsl.exe", "--exec", "wmux", "list", "--json").Output()
	if err != nil {
		return nil
	}
	var sessions []proto.SessionInfo
	if json.Unmarshal(bytes.TrimSpace(out), &sessions) != nil {
		return nil
	}
	return sessions
}

// wslDaemonClose closes a session on the WSL-resident daemon — the
// fallback when the local daemon answered 404, since a WSL-path pane
// session only ever registered on the other side.
func wslDaemonClose(id string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("no WSL daemon bridge on this platform")
	}
	ctx, cancel := context.WithTimeout(context.Background(), wslBridgeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "wsl.exe", "--exec", "wmux", "close", "--id", id).CombinedOutput()
	if err != nil {
		return fmt.Errorf("WSL daemon: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
