package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/peterkure3/wmux/internal/proto"
)

// cmdNotify is a manual/testing entry point — for real agent integrations,
// point Claude Code / Codex at hook-claude / hook-codex instead (they speak
// each agent's actual wire format; see main() and their doc comments).
func cmdNotify(args []string) {
	fs := newFlagSet("notify")
	session := fs.String("session", "", "session ID this notification belongs to")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "wmux notify: missing <message>")
		os.Exit(2)
	}
	pushNotify(*session, fs.Arg(0), "notify")
}

// pushNotify sends a notification to the daemon over HTTP, exiting on any
// failure. `cmdName` only prefixes error messages so callers get useful
// diagnostics. Use pushNotifyErr where failure must not be fatal.
func pushNotify(session, body, cmdName string) {
	if err := pushNotifyErr(session, body); err != nil {
		fmt.Fprintf(os.Stderr, "wmux %s: %v\n", cmdName, err)
		os.Exit(1)
	}
}

func pushNotifyErr(session, body string) error {
	evt := proto.NotifyEvent{SessionID: session, Body: body}
	b, _ := json.Marshal(evt)

	resp, err := daemonPost("/notify", "application/json", bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("could not reach wmuxd: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return errors.New(describeStatus(resp))
	}
	return nil
}

// cmdWatch tails /events and prints notifications as they arrive — a
// terminal-only stand-in for the tray UI, useful while wiring hooks up.
func cmdWatch(args []string) {
	resp, err := daemonStream("/events")
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux watch: could not reach wmuxd: %v\n", err)
		os.Exit(3)
	}
	defer resp.Body.Close()

	fmt.Println("watching for notifications... (Ctrl+C to stop)")
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > 6 && line[:6] == "data: " {
			var evt proto.Event
			if err := json.Unmarshal([]byte(line[6:]), &evt); err == nil &&
				evt.Type == proto.EventNotify && evt.Notify != nil {
				n := evt.Notify
				missed := ""
				if n.Dropped > 0 {
					missed = fmt.Sprintf("  (+%d earlier missed)", n.Dropped)
				}
				fmt.Printf("[%s] %s: %s%s\n", n.Time.Format("15:04:05"), n.SessionID, n.Display(), missed)
			}
			// "sessions" lifecycle events are for UI clients (wmux sidebar);
			// watch stays a notification tail.
		}
	}
}
