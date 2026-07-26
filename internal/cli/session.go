// Session lifecycle: creating, listing, and ending the sessions the
// daemon tracks. Every command here follows args.go's defaulting policy.
package cli

import (
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

// cmdAttach runs a command interactively with full TTY passthrough — real
// stdin/stdout/stderr, so colors, readline, and prompts all work — while
// registering with the daemon purely for tracking (branch/ports, and as a
// target for hook-claude/hook-codex notifications), unlike `wmux new`,
// which pipes output through the daemon and has no TTY at all.
// Usage is `wmux attach [command...]`; only the command is required —
// this process is the one running it, so there is nothing to run without
// it. The ID and working directory default like everywhere else.
func cmdAttach(args []string) {
	fs := newFlagSet("attach")
	idFlag := fs.String("id", "", "session ID (default: derived from --cwd)")
	cwd := fs.String("cwd", "", "working directory (default: the current directory)")
	distro := fs.String("distro", "", "WSL distro name, recorded for daemon metadata only")
	fs.Parse(args)

	cmdArgs := fs.Args()
	if len(cmdArgs) == 0 {
		fmt.Fprintln(os.Stderr, "wmux attach: missing command, e.g. 'wmux attach claude'")
		os.Exit(2)
	}
	dir := resolveCwd(*cwd)
	id := resolveSessionID(*idFlag, dir)

	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Dir = dir
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
		ID: id, Cwd: dir, Distro: *distro, PID: cmd.Process.Pid,
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
	deregReq := proto.DeregisterSessionRequest{ID: id}
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

// cmdNew spawns a headless, piped (no TTY) session. Usage is `wmux new
// [command...]`; the ID and working directory default like every other
// session-creating command (see args.go). A command is the one thing
// with no sensible default here — a piped session with no process is
// nothing at all — so it stays required.
func cmdNew(args []string) {
	fs := newFlagSet("new")
	id := fs.String("id", "", "session ID (default: derived from --cwd)")
	cwd := fs.String("cwd", "", "working directory (default: the current directory)")
	command := fs.String("cmd", "", "command to run (or pass it as trailing arguments)")
	distro := fs.String("distro", "", "WSL distro name (Windows only; ignored elsewhere)")
	fs.Parse(args)

	dir := resolveCwd(*cwd)
	cmd := resolveCmd(positionalCommand(*command, fs.Args()))
	if cmd == "" {
		fmt.Fprintln(os.Stderr, "wmux new: missing command, e.g. 'wmux new claude'")
		os.Exit(2)
	}

	req := proto.NewSessionRequest{ID: resolveSessionID(*id, dir), Cwd: dir, Command: cmd, Distro: *distro}
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
// Usage is `wmux close [ID]`; with no ID it closes the only running
// session, and names them all rather than guessing if there are several.
func cmdClose(args []string) {
	fs := newFlagSet("close")
	idFlag := fs.String("id", "", "session ID (default: the only running session)")
	fs.Parse(args)

	id := *idFlag
	if id == "" {
		id = resolveRunningID("close", fs.Args())
	}

	err := closeSession(id)
	if err == nil {
		return // silent on success, per the mutate-commands-print-nothing convention
	}
	// Unknown locally: a WSL-path pane session only ever registered with
	// the WSL-resident daemon (see bridge.go) — try there before failing.
	if errors.Is(err, errSessionNotFound) {
		if werr := wslDaemonClose(id); werr == nil {
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
