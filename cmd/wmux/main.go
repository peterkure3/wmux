// wmux is the CLI used to control wmuxd: create sessions, list state, and
// (most importantly) let agent hooks push notifications.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/peterkure/wmux/internal/proto"
)

const daemonAddr = "http://127.0.0.1:47823"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "notify":
		cmdNotify(os.Args[2:])
	case "hook-claude":
		cmdHookClaude(os.Args[2:])
	case "hook-codex":
		cmdHookCodex(os.Args[2:])
	case "new":
		cmdNew(os.Args[2:])
	case "list":
		cmdList(os.Args[2:])
	case "watch":
		cmdWatch(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  wmux notify <message> --session ID     manually push a notification (testing)
  wmux hook-claude                       Claude Code Notification hook (reads stdin JSON)
  wmux hook-codex --session ID <json>    Codex 'notify' config target (JSON as final arg)
  wmux new --id ID --cwd PATH --cmd CMD  spawn a new agent session
  wmux list                              list sessions and their state
  wmux watch                             stream notifications as they arrive`)
}

// cmdNotify is a manual/testing entry point — for real agent integrations,
// point Claude Code / Codex at hook-claude / hook-codex instead (they speak
// each agent's actual wire format; see main() and their doc comments).
func cmdNotify(args []string) {
	fs := newFlagSet("notify")
	session := fs.String("session", "", "session ID this notification belongs to")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "wmux notify: missing <message>")
		os.Exit(1)
	}
	pushNotify(*session, fs.Arg(0), "notify")
}

// pushNotify sends a notification to the daemon over HTTP. `cmdName` is
// only used to prefix error messages so callers get useful diagnostics.
func pushNotify(session, body, cmdName string) {
	evt := proto.NotifyEvent{SessionID: session, Body: body}
	b, _ := json.Marshal(evt)

	resp, err := http.Post(daemonAddr+"/notify", "application/json", bytes.NewReader(b))
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux %s: could not reach wmuxd: %v\n", cmdName, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		bodyBytes, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "wmux %s: daemon returned %s: %s\n", cmdName, resp.Status, string(bodyBytes))
		os.Exit(1)
	}
}

// cmdHookClaude is the command to point Claude Code's Notification hook at.
// Claude Code invokes command hooks with the event payload on **stdin** as
// JSON, e.g.:
//
//	{"session_id":"abc123","cwd":"/home/you/proj","hook_event_name":"Notification","message":"Claude is waiting for your input"}
//
// Wire it up in ~/.claude/settings.json (or the project's .claude/settings.json):
//
//	{
//	  "hooks": {
//	    "Notification": [
//	      { "matcher": "", "hooks": [{ "type": "command", "command": "wmux hook-claude" }] }
//	    ]
//	  }
//	}
//
// Claude Code's own session_id becomes the wmux session ID directly — the
// daemon doesn't require a session to have been pre-registered via `wmux new`
// for a notify to be accepted, so this works standalone.
func cmdHookClaude(args []string) {
	var payload struct {
		SessionID     string `json:"session_id"`
		Cwd           string `json:"cwd"`
		Message       string `json:"message"`
		HookEventName string `json:"hook_event_name"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&payload); err != nil {
		fmt.Fprintf(os.Stderr, "wmux hook-claude: could not parse stdin payload: %v\n", err)
		os.Exit(1)
	}

	sessionID := payload.SessionID
	if sessionID == "" {
		sessionID = payload.Cwd // best-effort fallback if Claude Code omits session_id
	}
	if payload.Message == "" {
		return // nothing to notify about
	}

	pushNotify(sessionID, payload.Message, "hook-claude")
}

// cmdHookCodex is the command to point Codex's `notify` config at. Unlike
// Claude Code, Codex appends the JSON payload as the **final CLI argument**,
// not stdin, e.g.:
//
//	wmux hook-codex --session my-project '{"type":"agent-turn-complete","last-assistant-message":"All tests passed"}'
//
// Wire it up in ~/.codex/config.toml (root keys must come before any [tables]):
//
//	notify = ["wmux", "hook-codex", "--session", "my-project"]
//
// Codex currently only supports the agent-turn-complete event through
// `notify`, so anything else is ignored. --session is a fixed label you
// choose per config.toml (Codex doesn't hand you a stable per-project ID
// through this mechanism) — falls back to the current working directory
// if omitted.
func cmdHookCodex(args []string) {
	fs := newFlagSet("hook-codex")
	session := fs.String("session", "", "session ID (falls back to cwd if omitted)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "wmux hook-codex: missing JSON payload argument")
		os.Exit(1)
	}

	var payload struct {
		Type             string `json:"type"`
		LastAssistantMsg string `json:"last-assistant-message"`
	}
	if err := json.Unmarshal([]byte(fs.Arg(0)), &payload); err != nil {
		fmt.Fprintf(os.Stderr, "wmux hook-codex: could not parse payload argument: %v\n", err)
		os.Exit(1)
	}

	if payload.Type != "agent-turn-complete" {
		return // only event type Codex's `notify` config currently emits
	}

	sessionID := *session
	if sessionID == "" {
		if cwd, err := os.Getwd(); err == nil {
			sessionID = cwd
		}
	}

	body := payload.LastAssistantMsg
	if body == "" {
		body = "Codex finished a turn"
	}

	pushNotify(sessionID, body, "hook-codex")
}

func cmdNew(args []string) {
	fs := newFlagSet("new")
	id := fs.String("id", "", "session ID")
	cwd := fs.String("cwd", ".", "working directory")
	command := fs.String("cmd", "", "command to run, e.g. 'claude'")
	distro := fs.String("distro", "", "WSL distro name (Windows only; ignored elsewhere)")
	fs.Parse(args)

	if *id == "" || *command == "" {
		fmt.Fprintln(os.Stderr, "wmux new: --id and --cmd are required")
		os.Exit(1)
	}

	req := proto.NewSessionRequest{ID: *id, Cwd: *cwd, Command: *command, Distro: *distro}
	b, _ := json.Marshal(req)

	resp, err := http.Post(daemonAddr+"/sessions", "application/json", bytes.NewReader(b))
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux new: could not reach wmuxd: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "wmux new: daemon returned %s: %s\n", resp.Status, string(bodyBytes))
		os.Exit(1)
	}

	var info proto.SessionInfo
	if err := json.Unmarshal(bodyBytes, &info); err != nil {
		fmt.Fprintf(os.Stderr, "wmux new: could not parse daemon response: %v\nraw body: %s\n", err, string(bodyBytes))
		os.Exit(1)
	}
	fmt.Printf("spawned session %s (cwd=%s)\n", info.ID, info.Cwd)
}

func cmdList(args []string) {
	resp, err := http.Get(daemonAddr + "/sessions")
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux list: could not reach wmuxd: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var sessions []proto.SessionInfo
	json.NewDecoder(resp.Body).Decode(&sessions)

	if len(sessions) == 0 {
		fmt.Println("no sessions")
		return
	}
	for _, s := range sessions {
		status := "idle"
		if !s.Running {
			status = "exited"
		}
		fmt.Printf("%-20s %-10s %-20s branch=%-15s ports=%v note=%q\n",
			s.ID, status, s.Cwd, s.Branch, s.Ports, s.LastNote)
	}
}

// cmdWatch tails /events and prints notifications as they arrive — a
// terminal-only stand-in for the tray UI, useful while wiring hooks up.
func cmdWatch(args []string) {
	resp, err := http.Get(daemonAddr + "/events")
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux watch: could not reach wmuxd: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	fmt.Println("watching for notifications... (Ctrl+C to stop)")
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > 6 && line[:6] == "data: " {
			var evt proto.NotifyEvent
			if err := json.Unmarshal([]byte(line[6:]), &evt); err == nil {
				fmt.Printf("[%s] %s: %s\n", evt.Time.Format("15:04:05"), evt.SessionID, evt.Body)
			}
		}
	}
}
