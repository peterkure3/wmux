// wmux panes and wmux send-keys are wt.exe-native replacements for the
// introspection/input-injection Zellij's CLI actions (list-panes,
// send-keys) offer — built directly on Win32 console APIs instead of
// depending on Zellij, since a Zellij background session proved
// unreliable in testing (its detached server did not consistently survive
// between separate invocations). wt.exe itself exposes none of this: no
// API to enumerate its panes or their state, and no way to inject input
// into one from outside. Native sessions only — see cmdPanes/cmdSendKeys.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/peterkure/wmux/internal/proto"
)

// fetchSessions is the same GET /sessions call cmdList makes, factored out
// since both cmdPanes and cmdSendKeys need to resolve a session by ID.
func fetchSessions(cmdName string) []proto.SessionInfo {
	resp, err := http.Get(daemonAddr + "/sessions")
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux %s: could not reach wmuxd: %v\n", cmdName, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var sessions []proto.SessionInfo
	json.NewDecoder(resp.Body).Decode(&sessions)
	return sessions
}

// cmdPanes lists every session the daemon knows about alongside whether a
// live console window currently exists for its tracked PID — the closest
// wt.exe-native equivalent to `zellij action list-panes`: proof a
// session's process is not just alive (processAlive already covers that
// server-side) but actually has a console attached and interactable.
// WSL-targeted (non-native) sessions have no PID meaningful to the
// Windows window table, so they're reported as such rather than guessed.
func cmdPanes(args []string) {
	sessions := fetchSessions("panes")
	if len(sessions) == 0 {
		fmt.Println("no sessions")
		return
	}

	fmt.Printf("%-20s %-8s %-8s %-8s %s\n", "ID", "RUNNING", "PID", "WINDOW", "TITLE")
	for _, s := range sessions {
		windowCol := "n/a"
		title := ""
		if !s.Native {
			windowCol = "wsl"
		} else if s.Running && s.PID != 0 {
			if hwnd, wtitle, ok := findWindowForPID(s.PID); ok {
				windowCol = fmt.Sprintf("0x%x", hwnd)
				title = wtitle
			} else {
				windowCol = "none"
			}
		}
		fmt.Printf("%-20s %-8v %-8d %-8s %s\n", s.ID, s.Running, s.PID, windowCol, title)
	}
}
