// wmux is the CLI used to control wmuxd: create sessions, list state, and
// (most importantly) let agent hooks push notifications.
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf16"

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
	case "attach":
		cmdAttach(os.Args[2:])
	case "pane":
		cmdPane(os.Args[2:])
	case "close":
		cmdClose(os.Args[2:])
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
  wmux new --id ID --cwd PATH --cmd CMD  spawn a new HEADLESS agent session (no TTY; daemon owns the pipe)
  wmux attach --id ID --cwd PATH -- CMD  run CMD interactively (real TTY), tracked by the daemon
  wmux pane --id ID --cwd PATH --distro D --cmd CMD [--split right|down|tab]
                                          open a new wt.exe pane running 'wmux attach' inside WSL
  wmux pane --native --id ID --cwd PATH --cmd CMD [--split right|down|tab]
                                          same, but runs CMD directly on Windows, no WSL
  wmux close --id ID                     kill a session's tracked process (does not close the wt.exe pane/tab itself)
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

// cmdAttach runs a command interactively with full TTY passthrough — real
// stdin/stdout/stderr, so colors, readline, and prompts all work — while
// registering with the daemon purely for tracking (branch/ports, and as a
// target for hook-claude/hook-codex notifications). This is what a wt.exe
// pane should run directly (see cmdPane), unlike `wmux new`, which pipes
// output through the daemon and has no TTY at all.
func cmdAttach(args []string) {
	fs := newFlagSet("attach")
	id := fs.String("id", "", "session ID")
	cwd := fs.String("cwd", ".", "working directory")
	distro := fs.String("distro", "", "WSL distro name, recorded for daemon metadata only")
	fs.Parse(args)

	if *id == "" {
		fmt.Fprintln(os.Stderr, "wmux attach: --id is required")
		os.Exit(1)
	}
	cmdArgs := fs.Args()
	if len(cmdArgs) == 0 {
		fmt.Fprintln(os.Stderr, "wmux attach: missing command, e.g. 'wmux attach --id x -- claude'")
		os.Exit(1)
	}

	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Dir = *cwd
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "wmux attach: could not start %q: %v\n", cmdArgs[0], err)
		os.Exit(1)
	}

	// Register only after Start() so the real PID can be included — this is
	// what `wmux close` later uses to kill this exact process.
	regReq := proto.RegisterSessionRequest{
		ID: *id, Cwd: *cwd, Distro: *distro, PID: cmd.Process.Pid,
		Native: runtime.GOOS == "windows",
	}
	b, _ := json.Marshal(regReq)
	resp, err := http.Post(daemonAddr+"/sessions/register", "application/json", bytes.NewReader(b))
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux attach: could not reach wmuxd (is it running?): %v\n", err)
		os.Exit(1)
	}
	regBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "wmux attach: daemon returned %s: %s\n", resp.Status, string(regBody))
		os.Exit(1)
	}

	runErr := cmd.Wait()

	// os.Exit skips deferred functions, so deregister explicitly on every
	// exit path rather than relying on defer — a non-zero exit code below
	// would otherwise silently leave the session marked "running" forever.
	deregReq := proto.DeregisterSessionRequest{ID: *id}
	b, _ = json.Marshal(deregReq)
	if resp, err := http.Post(daemonAddr+"/sessions/deregister", "application/json", bytes.NewReader(b)); err == nil {
		resp.Body.Close()
	} else {
		fmt.Fprintf(os.Stderr, "wmux attach: warning: could not deregister session with wmuxd: %v\n", err)
	}

	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "wmux attach: %v\n", runErr)
		os.Exit(1)
	}
}

// cmdPane opens a new Windows Terminal tab or split pane that runs `wmux
// attach` for you. This only shells out to wt.exe — it never talks to the
// daemon itself; the daemon interaction happens once `wmux attach` starts
// running inside the new pane. Run this from PowerShell, not from inside
// WSL (wt.exe isn't reachable from within a distro).
//
// Two modes, picked by --native:
//   - default (WSL): the pane runs `wmux attach` inside a WSL distro via
//     `wsl.exe -d <distro> -- bash -lc ...`. Only useful when the agent is
//     actually installed inside that distro.
//   - --native: the pane runs `wmux attach` directly on Windows via
//     `powershell.exe -EncodedCommand ...`, no WSL involved at all. Use
//     this when the agent (e.g. claude.exe) is a native Windows install —
//     check with `where claude`/`where codex` first, since having a WSL
//     distro on the machine doesn't mean the agent runs inside it.
func cmdPane(args []string) {
	fs := newFlagSet("pane")
	id := fs.String("id", "", "session ID")
	cwd := fs.String("cwd", "", "working directory (WSL path unless --native)")
	distro := fs.String("distro", "", "WSL distro name (ignored with --native; defaults to your system's default WSL distro if omitted)")
	command := fs.String("cmd", "", "command to run interactively, e.g. 'claude' (WSL mode) or a full .exe path (--native)")
	split := fs.String("split", "tab", "'tab' (new tab), 'right' (side-by-side), or 'down' (stacked)")
	native := fs.Bool("native", false, "run --cmd directly on Windows, no WSL — use when the agent is a native Windows install")
	fs.Parse(args)

	if *id == "" || *cwd == "" || *command == "" {
		fmt.Fprintln(os.Stderr, "wmux pane: --id, --cwd, and --cmd are required")
		os.Exit(1)
	}

	wtArgs := []string{"-w", "0"}
	switch *split {
	case "right":
		// wt.exe's own "-V"/"--vertical" names this after the orientation of
		// the dividing line (vertical line = panes side-by-side), which is
		// the opposite of what most people mean by "vertical split" (panes
		// stacked vertically) — verified by screenshot during testing that
		// -V actually produces a left/right layout. Use unambiguous flag
		// values here instead of reproducing that confusion in wmux's own
		// CLI.
		wtArgs = append(wtArgs, "split-pane", "-V")
	case "down":
		wtArgs = append(wtArgs, "split-pane", "-H")
	default:
		wtArgs = append(wtArgs, "new-tab")
	}
	wtArgs = append(wtArgs, "--title", *id)

	if *native {
		// wmuxExe resolves to this same binary's absolute path, so the
		// pane can find it regardless of whatever PATH state the new
		// powershell.exe process starts with.
		wmuxExe, err := os.Executable()
		if err != nil {
			wmuxExe = "wmux.exe" // fall back to PATH lookup
		}
		innerCmd := fmt.Sprintf("& %s attach --id %s --cwd %s -- %s",
			psQuote(wmuxExe), psQuote(*id), psQuote(*cwd), *command)
		wtArgs = append(wtArgs, "powershell.exe", "-EncodedCommand", psEncodedCommand(innerCmd))
	} else {
		// Runs inside the distro: exec wmux attach with a real TTY, tracked
		// under --id, which then execs the actual agent command interactively.
		innerCmd := fmt.Sprintf("wmux attach --id %s --cwd %s --distro %s -- %s",
			shellQuote(*id), shellQuote(*cwd), shellQuote(*distro), *command)

		// wt.exe's own command-line parser splits on unescaped ';' to chain
		// multiple wt subcommands ("wt new-tab ; split-pane ; ..."), so any
		// semicolon anywhere in --cmd (e.g. a compound shell command like
		// "foo; bar") silently truncates everything after it before it ever
		// reaches wsl.exe. Base64-encode the whole inner command so wt.exe
		// only ever sees one opaque token. Also avoid embedded double
		// quotes/`eval "$(...)"` here: verified empirically that wt.exe's
		// re-tokenizing of the trailing commandline mangles a token
		// containing literal `"` characters (likely a quoting-convention
		// mismatch between Go's argv escaping and wt.exe's own parser),
		// even though the same payload runs fine via wsl.exe/bash directly.
		// A pure pipe with no quote characters at all
		// (`echo B64|base64 -d|bash`) survives intact.
		encoded := base64.StdEncoding.EncodeToString([]byte(innerCmd))
		execCmd := fmt.Sprintf("echo %s|base64 -d|bash", encoded)

		wtArgs = append(wtArgs, "wsl.exe")
		if *distro != "" {
			wtArgs = append(wtArgs, "-d", *distro)
		}
		wtArgs = append(wtArgs, "--cd", *cwd, "--", "bash", "-lc", execCmd)
	}

	cmd := exec.Command("wt.exe", wtArgs...)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "wmux pane: could not launch wt.exe (is Windows Terminal installed and on PATH?): %v\n", err)
		os.Exit(1)
	}
	label, ok := map[string]string{"right": "side-by-side split", "down": "stacked split", "tab": "new tab"}[*split]
	if !ok {
		label = "new tab"
	}
	fmt.Printf("opened %s for session %s\n", label, *id)
}

// shellQuote wraps a value in single quotes for the bash -lc string built
// above, escaping any embedded single quotes the POSIX way.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// psQuote wraps a value in single quotes for a PowerShell command string,
// escaping any embedded single quotes PowerShell's way (doubled, not
// backslash-escaped).
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// psEncodedCommand base64-encodes a PowerShell script as UTF-16LE for
// `powershell.exe -EncodedCommand`, PowerShell's own documented mechanism
// for passing an arbitrary script as a single opaque token. Used here for
// the same reason the WSL path base64-encodes through a pipe: it avoids
// handing wt.exe's trailing-commandline tokenizer a token containing
// semicolons or quote characters, both of which have been observed to
// break as they cross the wt.exe layer (see the comment above the WSL
// branch in cmdPane).
func psEncodedCommand(script string) string {
	units := utf16.Encode([]rune(script))
	buf := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(buf[i*2:], u)
	}
	return base64.StdEncoding.EncodeToString(buf)
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

// cmdClose kills a session's tracked process (daemon-owned for `wmux new`,
// or the registered PID for `wmux attach`/`wmux pane`). This ends the
// agent and, transitively, whatever wrapping shell wt.exe is hosting for a
// pane-opened session — but wmuxd has no way to make Windows Terminal
// remove the now-inert pane/tab from its own UI, so that part still needs
// closing by hand (Ctrl+Shift+W or the tab's close button).
func cmdClose(args []string) {
	fs := newFlagSet("close")
	id := fs.String("id", "", "session ID")
	fs.Parse(args)

	if *id == "" {
		fmt.Fprintln(os.Stderr, "wmux close: --id is required")
		os.Exit(1)
	}

	req := proto.CloseSessionRequest{ID: *id}
	b, _ := json.Marshal(req)
	resp, err := http.Post(daemonAddr+"/sessions/close", "application/json", bytes.NewReader(b))
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux close: could not reach wmuxd: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "wmux close: daemon returned %s: %s\n", resp.Status, string(body))
		os.Exit(1)
	}
	fmt.Printf("closed session %s (the wt.exe pane/tab, if any, may still need closing by hand)\n", *id)
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
