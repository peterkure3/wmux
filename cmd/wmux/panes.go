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

	// Two-pass window resolution. PID enumeration only answers for a
	// process that owns its own console window; a WT-hosted pane's window
	// belongs to WindowsTerminal.exe, so every `wmux pane` session used to
	// show WINDOW=none. Those (and WSL sessions, whose panes are addressable
	// the same way) fall back to one batched UIA title lookup — the pane's
	// fixed title is its session ID, same addressing as `wmux focus --id`.
	type resolved struct{ window, title string }
	rows := make(map[string]resolved, len(sessions))
	var uiaIDs []string
	for _, s := range sessions {
		r := resolved{window: "n/a"}
		if s.Running {
			if s.Native && s.PID != 0 {
				if hwnd, wtitle, ok := findWindowForPID(s.PID); ok {
					rows[s.ID] = resolved{fmt.Sprintf("0x%x", hwnd), wtitle}
					continue
				}
			}
			r.window = "none"
			uiaIDs = append(uiaIDs, s.ID)
		}
		rows[s.ID] = r
	}
	wtWins := wtPanesByTitle(uiaIDs)
	for id, hwnd := range wtWins {
		// wt:<hwnd> = a WT window contains a pane/tab with this session's
		// title; the handle is the WT window's, not one the session owns.
		rows[id] = resolved{"wt:" + hwnd, id}
	}

	fmt.Printf("%-20s %-8s %-8s %-12s %s\n", "ID", "RUNNING", "PID", "WINDOW", "TITLE")
	for _, s := range sessions {
		r := rows[s.ID]
		fmt.Printf("%-20s %-8v %-8d %-12s %s\n", s.ID, s.Running, s.PID, r.window, r.title)
	}
}
