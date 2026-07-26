// wmux is the CLI used to control wmuxd: create sessions, list state, and
// (most importantly) let agent hooks push notifications.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"github.com/peterkure3/wmux/internal/proto"
)

// daemonAddr is where the wmuxd HTTP API lives. WMUX_ADDR overrides the
// default — for pointing at a wmuxd started with a non-default -addr
// (parallel test daemon, several isolated daemons on one machine).
var daemonAddr = func() string {
	if addr := os.Getenv("WMUX_ADDR"); addr != "" {
		return addr
	}
	return "http://127.0.0.1:47823"
}()

func main() {
	dispatch()
}

const banner = `
██╗    ██╗███╗   ███╗██╗   ██╗██╗  ██╗
██║    ██║████╗ ████║██║   ██║╚██╗██╔╝
██║ █╗ ██║██╔████╔██║██║   ██║ ╚███╔╝
██║███╗██║██║╚██╔╝██║██║   ██║ ██╔██╗
╚███╔███╔╝██║ ╚═╝ ██║╚██████╔╝██╔╝ ██╗
 ╚══╝╚══╝ ╚═╝     ╚═╝ ╚═════╝ ╚═╝  ╚═╝`

func usage() {
	fmt.Fprintln(os.Stderr, banner)
	fmt.Fprintln(os.Stderr, `usage:
  wmux notify <message> --session ID     manually push a notification (testing)
  wmux hook run <agent> [--session ID]   generic agent hook target, driven by a per-agent profile
                                          (bundled: claude, codex, kimi, kiro; override or add via
                                          ~/.wmux/agents/<agent>.toml);
                                          --forward TOKEN (repeatable) chains a pre-existing notify handler
  wmux hook list                         list known agent profiles
  wmux hook-claude                       alias for 'wmux hook run claude' (reads stdin JSON)
  wmux hook-codex --session ID <json>    alias for 'wmux hook run codex' (JSON as final arg)
  wmux new --id ID --cwd PATH --cmd CMD  spawn a new HEADLESS agent session (no TTY; daemon owns the pipe)
  wmux attach --id ID --cwd PATH -- CMD  run CMD interactively (real TTY), tracked by the daemon
  wmux surface --id ID --cwd PATH --cmd CMD [--native] [--distro D]
                                          spawn CMD in a daemon-owned ConPTY (real TTY, runs headless,
                                          survives terminal close — tmux-style)
  wmux connect --id ID                   attach this terminal to a surface (Ctrl-] detaches, session keeps running)
  wmux tui [--with CMD --cwd PATH [--id ID] [--native] [--distro D]]
                                          full-screen multi-pane multiplexer over daemon-owned surfaces —
                                          the primary way to run multiple sessions; see 'wmux tui -h'
  wmux theme [midnight|frost|gradient]   print the active sidebar theme (no arg), or persist a
                                          new one for the next 'wmux tui' launch
  wmux log [tail [-n N]|level [NAME]|path]
                                          inspect wmuxd's structured log (default: path + level);
                                          'level' with no arg prints it, with a name persists it
                                          for the next wmuxd start
  wmux debug state|panics|events|dump|pprof [cpu|heap|goroutine [seconds]]
                                          inspect wmuxd's own runtime state — session table,
                                          recovered panics, recent events, a bug-report bundle
                                          ('dump'), or a pprof profile written to disk
  wmux close --id ID                     kill a session's tracked process
  wmux list                              list sessions and their state
  wmux prune                             remove all exited sessions from daemon state
  wmux watch                             stream notifications as they arrive
  wmux update [--repo PATH] [--no-pull] [--release latest|vX.Y.Z] [--kill-surfaces]
                                          self-update wmux + wmuxd: rebuild from source, or with
                                          --release install a published GitHub release (SHA256-verified;
                                          also the automatic fallback when no source repo is configured)
                                          (refuses while live surfaces exist unless --kill-surfaces)
  wmux autostart install|uninstall|status
                                          register/remove wmuxd as a Task Scheduler logon task
  wmux version                           print the wmux version

  --cmd - (also --with -) reads the command from stdin instead — use for
  commands with $(), quotes, or semicolons that shells/wsl.exe would mangle`)
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

// resolveCmd expands a --cmd value of "-" by reading the command from
// stdin. Between the caller's shell and the daemon a command can cross
// Git Bash, PowerShell, wsl.exe, and JSON — each mangles quoting and
// metacharacters ($(), quotes, semicolons) differently; stdin passes the
// bytes through untouched.
func resolveCmd(cmd string) string {
	if cmd != "-" {
		return cmd
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux: could not read --cmd from stdin: %v\n", err)
		os.Exit(1)
	}
	c := strings.TrimSpace(string(b))
	if c == "" {
		fmt.Fprintln(os.Stderr, "wmux: --cmd - given but stdin was empty")
		os.Exit(1)
	}
	return c
}

// multiFlag collects a repeatable string flag's values in order.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, " ") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// cmdAttach runs a command interactively with full TTY passthrough — real
// stdin/stdout/stderr, so colors, readline, and prompts all work — while
// registering with the daemon purely for tracking (branch/ports, and as a
// target for hook-claude/hook-codex notifications), unlike `wmux new`,
// which pipes output through the daemon and has no TTY at all.
func cmdAttach(args []string) {
	fs := newFlagSet("attach")
	id := fs.String("id", "", "session ID")
	cwd := fs.String("cwd", ".", "working directory")
	distro := fs.String("distro", "", "WSL distro name, recorded for daemon metadata only")
	fs.Parse(args)

	if *id == "" {
		fmt.Fprintln(os.Stderr, "wmux attach: --id is required")
		os.Exit(2)
	}
	cmdArgs := fs.Args()
	if len(cmdArgs) == 0 {
		fmt.Fprintln(os.Stderr, "wmux attach: missing command, e.g. 'wmux attach --id x -- claude'")
		os.Exit(2)
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
	resp, err := daemonPost("/sessions/register", "application/json", bytes.NewReader(b))
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux attach: could not reach wmuxd (is it running?): %v\n", err)
		os.Exit(3)
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
	if resp, err := daemonPost("/sessions/deregister", "application/json", bytes.NewReader(b)); err == nil {
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

func cmdNew(args []string) {
	fs := newFlagSet("new")
	id := fs.String("id", "", "session ID")
	cwd := fs.String("cwd", ".", "working directory")
	command := fs.String("cmd", "", "command to run, e.g. 'claude'")
	distro := fs.String("distro", "", "WSL distro name (Windows only; ignored elsewhere)")
	fs.Parse(args)
	*command = resolveCmd(*command)

	if *id == "" || *command == "" {
		fmt.Fprintln(os.Stderr, "wmux new: --id and --cmd are required")
		os.Exit(2)
	}

	req := proto.NewSessionRequest{ID: *id, Cwd: *cwd, Command: *command, Distro: *distro}
	b, _ := json.Marshal(req)

	resp, err := daemonPost("/sessions", "application/json", bytes.NewReader(b))
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux new: could not reach wmuxd: %v\n", err)
		os.Exit(3)
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
	fmt.Println(info.ID)
}

// cmdClose kills a session's tracked process (daemon-owned for `wmux new`,
// or the registered PID for `wmux attach`/`wmux pane`). For a pane opened
// by `wmux pane`, killing the agent tears down the pane's whole process
// chain, and the "wmux" profile's closeOnExit:"always" then makes Windows
// Terminal remove the pane itself from the layout — nothing left to close
// by hand. (A session run in some other terminal just ends; what its
// terminal does about it is that terminal's business.)
func cmdClose(args []string) {
	fs := newFlagSet("close")
	id := fs.String("id", "", "session ID")
	fs.Parse(args)

	if *id == "" {
		fmt.Fprintln(os.Stderr, "wmux close: --id is required")
		os.Exit(2)
	}

	err := closeSession(*id)
	if err == nil {
		return // silent on success, per the mutate-commands-print-nothing convention
	}
	// Unknown locally: a WSL-path pane session only ever registered with
	// the WSL-resident daemon (see bridge.go) — try there before failing.
	if errors.Is(err, errSessionNotFound) {
		if werr := wslDaemonClose(*id); werr == nil {
			return
		}
	}
	fmt.Fprintf(os.Stderr, "wmux close: %v\n", err)
	var unreachable *errWmuxdUnreachable
	if errors.As(err, &unreachable) {
		os.Exit(3)
	}
	os.Exit(1)
}

// errSessionNotFound marks a close rejected because the daemon doesn't
// know the ID — the one failure where trying the WSL daemon makes sense
// (any other error would just repeat over there).
var errSessionNotFound = errors.New("session not found")

// closeSession asks the daemon to kill a session's tracked process —
// shared by `wmux close` and the sidebar's x action.
func closeSession(id string) error {
	req := proto.CloseSessionRequest{ID: id}
	b, _ := json.Marshal(req)
	resp, err := daemonPost("/sessions/close", "application/json", bytes.NewReader(b))
	if err != nil {
		return &errWmuxdUnreachable{err}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%w: %s", errSessionNotFound, strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New(describeStatus(resp))
	}
	return nil
}

// fetchSessions is the same GET /sessions call cmdList makes, factored
// out as its own name for a clearer error prefix at its one other
// caller.
func fetchSessions(cmdName string) []proto.SessionInfo {
	resp, err := daemonGet("/sessions")
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux %s: could not reach wmuxd: %v\n", cmdName, err)
		os.Exit(3)
	}
	defer resp.Body.Close()
	// Without this, a non-200 (notably 401) decodes an error string into
	// nothing and reports "no sessions" — the daemon's actual complaint
	// silently discarded, and `wmux list` cheerfully wrong.
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "wmux %s: %s\n", cmdName, describeStatus(resp))
		os.Exit(1)
	}

	var sessions []proto.SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		fmt.Fprintf(os.Stderr, "wmux %s: could not parse daemon response: %v\n", cmdName, err)
		os.Exit(1)
	}
	return sessions
}

func cmdList(args []string) {
	fs := newFlagSet("list")
	jsonOut := fs.Bool("json", false, "print the local daemon's session list as JSON (this is also the WSL bridge's wire format)")
	fs.Parse(args)

	sessions := fetchSessions("list")

	// --json is the bridge's own wire format (see bridge.go), so it stays
	// strictly local — a bridged bridge would recurse across the boundary.
	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(sessions)
		return
	}

	remote := wslDaemonSessions()
	if len(sessions) == 0 && len(remote) == 0 {
		fmt.Println("no sessions")
		return
	}
	printSessionRow := func(s proto.SessionInfo, origin string) {
		status := "idle"
		if !s.Running {
			status = "exited"
		}
		fmt.Printf("%-20s %-10s %-20s branch=%-15s ports=%v note=%q%s\n",
			s.ID, status, s.Cwd, s.Branch, s.Ports, s.LastNote, origin)
	}
	for _, s := range sessions {
		printSessionRow(s, "")
	}
	for _, s := range remote {
		printSessionRow(s, " [wsl]")
	}
}

// cmdPrune clears exited sessions out of daemon state. Entries are kept
// after exit on purpose (`wmux list` shows last known state), but they
// accumulate forever otherwise — this is the manual cleanup.
func cmdPrune(args []string) {
	fs := newFlagSet("prune")
	jsonOut := fs.Bool("json", false, "print the removed session IDs as a JSON array instead of one per line")
	fs.Parse(args)

	resp, err := daemonPost("/sessions/prune", "application/json", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux prune: could not reach wmuxd: %v\n", err)
		os.Exit(3)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "wmux prune: daemon returned %s: %s\n", resp.Status, string(body))
		os.Exit(1)
	}
	var result proto.PruneResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		fmt.Fprintf(os.Stderr, "wmux prune: could not parse daemon response: %v\n", err)
		os.Exit(1)
	}
	sort.Strings(result.Removed)
	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(result.Removed)
		return
	}
	for _, id := range result.Removed {
		fmt.Println(id)
	}
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
