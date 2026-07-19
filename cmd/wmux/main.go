// wmux is the CLI used to control wmuxd: create sessions, list state, and
// (most importantly) let agent hooks push notifications.
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/peterkure/wmux/internal/proto"
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
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "notify":
		cmdNotify(os.Args[2:])
	case "hook":
		cmdHook(os.Args[2:])
	case "hook-claude": // legacy alias, kept so existing ~/.claude/settings.json wiring keeps working
		runHook("hook-claude", "claude", os.Args[2:])
	case "hook-codex": // legacy alias, kept so existing ~/.codex/config.toml wiring keeps working
		runHook("hook-codex", "codex", os.Args[2:])
	case "new":
		cmdNew(os.Args[2:])
	case "attach":
		cmdAttach(os.Args[2:])
	case "surface":
		cmdSurface(os.Args[2:])
	case "connect":
		cmdConnect(os.Args[2:])
	case "pane":
		cmdPane(os.Args[2:])
	case "grid":
		cmdGrid(os.Args[2:])
	case "pane-exec":
		cmdPaneExec(os.Args[2:])
	case "sidebar":
		cmdSidebar(os.Args[2:])
	case "sidebar-ui":
		cmdSidebarUI(os.Args[2:])
	case "focus":
		cmdFocus(os.Args[2:])
	case "close":
		cmdClose(os.Args[2:])
	case "list":
		cmdList(os.Args[2:])
	case "prune":
		cmdPrune(os.Args[2:])
	case "watch":
		cmdWatch(os.Args[2:])
	case "update":
		cmdUpdate(os.Args[2:])
	case "autostart":
		cmdAutostart(os.Args[2:])
	case "panes":
		cmdPanes(os.Args[2:])
	case "send-keys":
		cmdSendKeys(os.Args[2:])
	case "version":
		cmdVersion(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
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
  wmux pane --id ID --cwd PATH --distro D --cmd CMD [--split right|down|tab]
                                          open a new wt.exe pane running 'wmux attach' inside WSL
  wmux pane --native --id ID --cwd PATH --cmd CMD [--split right|down|tab]
                                          same, but runs CMD directly on Windows, no WSL
  wmux grid --ids A,B[,C[,D]] --cwd PATH --cmd CMD [--native] [--distro D]
                                          open 2-4 panes at once in one new tab (equal splits),
                                          each running CMD as its own session
  wmux sidebar [--bare]                  open the live session sidebar (~20% wide) plus a default shell pane;
                                          --bare opens only the sidebar
  wmux sidebar --with CMD --cwd PATH [--id ID] [--native] [--distro D]
                                          same, but the right pane runs an agent session instead of a shell
  wmux sidebar --grid A,B[,C[,D]] --with CMD --cwd PATH [--native] [--distro D]
                                          sidebar plus a 2-4 pane grid beside it in one tab,
                                          every pane running CMD as its own session
  wmux focus --id ID                     bring a session's wt.exe pane/tab into focus
  wmux focus --dir left|right|up|down    move pane focus within the current wt.exe window
  wmux close --id ID                     kill a session's tracked process (a wmux pane closes itself too)
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
  wmux panes                              list sessions with live console-window status (introspection wt.exe has no API for)
  wmux send-keys --id ID -- KEYS...       inject keystrokes into a native session's console (e.g. Enter, "Ctrl c", literal text)
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
		os.Exit(1)
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

	resp, err := http.Post(daemonAddr+"/notify", "application/json", bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("could not reach wmuxd: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("daemon returned %s: %s", resp.Status, string(bodyBytes))
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
	*command = resolveCmd(*command)

	if *id == "" || *cwd == "" || *command == "" {
		fmt.Fprintln(os.Stderr, "wmux pane: --id, --cwd, and --cmd are required")
		os.Exit(1)
	}
	if *id == sidebarTitle {
		fmt.Fprintf(os.Stderr, "wmux pane: session ID %q is reserved for the sidebar itself\n", sidebarTitle)
		os.Exit(1)
	}

	// Catch the "native agent, forgot --native" mistake up front: plain
	// mode hands --cmd to bash inside WSL, where a Windows path or .exe can
	// never run — without this check the failure is a pane that flashes
	// open and instantly closes (closeOnExit "always") with no readable
	// error.
	if !*native && looksLikeWindowsCommand(*command) {
		fmt.Fprintf(os.Stderr, "wmux pane: --cmd %q looks like a native Windows command, but plain 'wmux pane' runs --cmd inside WSL — add --native\n", *command)
		os.Exit(1)
	}

	// The pane runs the fixed "wmux" Windows Terminal profile instead of a
	// commandline passed through wt.exe. A pane only honors its profile's
	// closeOnExit setting when it runs the profile's own commandline —
	// passing one on the wt.exe command line leaves an inert, already-dead
	// pane in the layout when the process exits, no matter the exit code
	// (verified empirically; there is no wt.exe API to remove such a pane
	// afterwards). The profile's commandline is `wmux pane-exec`, which
	// claims the spec filed below and runs the real agent command — so the
	// pane genuinely disappears when the agent exits or `wmux close` kills
	// it.
	if err := ensureWTProfileFragment(); err != nil {
		fmt.Fprintf(os.Stderr, "wmux pane: could not install the 'wmux' Windows Terminal profile: %v\n", err)
		os.Exit(1)
	}

	// File the spec before launching wt.exe so pane-exec can't beat it to
	// the daemon.
	spec := proto.PaneSpec{ID: *id, Cwd: *cwd, Distro: *distro, Command: *command, Native: *native}
	if err := filePaneSpec(spec); err != nil {
		fmt.Fprintf(os.Stderr, "wmux pane: %v\n", err)
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
	// --title carries the session ID into the pane: it becomes the new
	// console's title, which is the only channel wt.exe gives us to tell
	// pane-exec which spec is its own (the profile flow means we can't pass
	// it arguments). --suppressApplicationTitle keeps the agent from
	// renaming the tab afterwards, so `wmux focus --id` can keep finding
	// the pane by session ID for the whole session lifetime.
	wtArgs = append(wtArgs,
		"--title", *id, "--suppressApplicationTitle", "--profile", "wmux")

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

// ensureWTProfileFragment installs (or refreshes) the "wmux" Windows
// Terminal profile via WT's JSON fragment mechanism —
// %LOCALAPPDATA%\Microsoft\Windows Terminal\Fragments\wmux\wmux.json —
// which adds a profile without ever touching the user's own settings.json
// content. The profile's commandline runs this same binary's `pane-exec`,
// and closeOnExit "always" is what makes a wmux pane actually disappear
// when its process dies (including via `wmux close`), instead of leaving
// an inert pane behind.
//
// A running Windows Terminal only reads new fragments during a settings
// reload, so after writing the file this touches settings.json to nudge
// the live WT process into importing it immediately (verified working
// against WT 1.24 without a restart).
func ensureWTProfileFragment() error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("wmux pane drives wt.exe and must run on Windows")
	}
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return fmt.Errorf("LOCALAPPDATA is not set")
	}
	wmuxExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not resolve wmux.exe's own path: %w", err)
	}

	// The path may contain spaces (user profile dirs usually do), so it is
	// always quoted — WT parses the profile commandline itself, Windows-style.
	fragment := fmt.Sprintf(
		`{"profiles":[{"name":"wmux","commandline":%s,"closeOnExit":"always","suppressApplicationTitle":true}]}`,
		mustJSON(fmt.Sprintf(`"%s" pane-exec`, wmuxExe)))

	dir := filepath.Join(localAppData, "Microsoft", "Windows Terminal", "Fragments", "wmux")
	path := filepath.Join(dir, "wmux.json")

	if existing, err := os.ReadFile(path); err == nil && string(existing) == fragment {
		return nil // already installed and current; WT has had time to import it
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(fragment), 0o644); err != nil {
		return err
	}

	// Nudge any running WT into a settings reload so the fragment is
	// imported now rather than at its next restart.
	touched := false
	now := time.Now()
	for _, settings := range []string{
		filepath.Join(localAppData, `Packages\Microsoft.WindowsTerminal_8wekyb3d8bbwe\LocalState\settings.json`),
		filepath.Join(localAppData, `Packages\Microsoft.WindowsTerminalPreview_8wekyb3d8bbwe\LocalState\settings.json`),
		filepath.Join(localAppData, `Microsoft\Windows Terminal\settings.json`),
	} {
		if err := os.Chtimes(settings, now, now); err == nil {
			touched = true
		}
	}
	if touched {
		// Give WT a beat to finish the import before wt.exe is asked to
		// open a pane with --profile wmux.
		time.Sleep(2 * time.Second)
	}
	return nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// cmdPaneExec is what runs inside every pane `wmux pane` opens — it is the
// "wmux" Windows Terminal profile's fixed commandline, never something a
// user runs by hand. It reads its own console title (wt.exe's --title set
// it to the session ID), claims the matching pane spec from the daemon,
// and runs the agent via the exact same inner commands `wmux pane` used
// to hand to wt.exe directly.
func cmdPaneExec(args []string) {
	title, err := consoleTitle()
	if err != nil {
		paneExecFail("could not read console title: %v", err)
	}
	id := strings.TrimSpace(title)

	// The reserved sidebar title means "run the sidebar TUI here" — no pane
	// spec, no wmux attach, no session registration (see cmdSidebar).
	if id == sidebarTitle {
		cmdSidebarUI(nil)
		return
	}

	// Claim with a few retries: on a loaded machine this pane can start
	// before `wmux pane`'s own POST /panes/pending has landed.
	var spec proto.PaneSpec
	claimed := false
	for attempt := 0; attempt < 10 && !claimed; attempt++ {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		b, _ := json.Marshal(proto.ClaimPaneRequest{ID: id})
		resp, err := http.Post(daemonAddr+"/panes/claim", "application/json", bytes.NewReader(b))
		if err != nil {
			continue // daemon not reachable (yet); retry
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue // spec not filed yet; retry
		}
		if err := json.Unmarshal(body, &spec); err == nil {
			claimed = true
		}
	}
	if !claimed {
		pushNotifyErr(id, fmt.Sprintf("pane %s failed: no pending pane spec claimed", id))
		paneExecFail("no pending pane spec for session %q — was this pane opened by 'wmux pane'?", id)
	}

	var cmd *exec.Cmd
	if spec.Native {
		wmuxExe, err := os.Executable()
		if err != nil {
			wmuxExe = "wmux.exe" // fall back to PATH lookup
		}
		// PowerShell parses spec.Command (it may be a full commandline with
		// arguments), and -EncodedCommand keeps the whole script one opaque
		// token — same quoting rationale as the old direct wt.exe flow.
		innerCmd := fmt.Sprintf("& %s attach --id %s --cwd %s -- %s",
			psQuote(wmuxExe), psQuote(spec.ID), psQuote(spec.Cwd), spec.Command)
		cmd = exec.Command("powershell.exe", "-NoProfile", "-EncodedCommand", psEncodedCommand(innerCmd))
	} else {
		// Runs inside the distro: exec wmux attach with a real TTY, tracked
		// under the session ID, which then execs the actual agent command
		// interactively. --exec so the distro's default shell never
		// re-expands the tail (see buildCommand), and base64-piped as
		// defense in depth: the b64 charset survives any remaining layer.
		innerCmd := fmt.Sprintf("wmux attach --id %s --cwd %s --distro %s -- %s",
			shellQuote(spec.ID), shellQuote(spec.Cwd), shellQuote(spec.Distro), spec.Command)
		encoded := base64.StdEncoding.EncodeToString([]byte(innerCmd))

		wslArgs := []string{}
		if spec.Distro != "" {
			wslArgs = append(wslArgs, "-d", spec.Distro)
		}
		wslArgs = append(wslArgs, "--cd", spec.Cwd, "--exec", "bash", "-lc",
			fmt.Sprintf("echo %s|base64 -d|bash", encoded))
		cmd = exec.Command("wsl.exe", wslArgs...)
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	start := time.Now()
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// A non-zero exit this soon after start is almost always setup
			// failing (command not found in the distro, wmux attach unable
			// to reach the daemon, ...), not the agent's own business — and
			// closeOnExit "always" would reduce whatever it printed above
			// to an unreadable flash. Hold the pane open long enough to
			// read it, then still propagate the real exit code.
			if elapsed := time.Since(start); elapsed < 10*time.Second {
				hint := ""
				if !spec.Native {
					hint = " — if the agent is a native Windows install, reopen with 'wmux pane --native'"
				}
				fmt.Fprintf(os.Stderr, "wmux pane-exec: command %q exited with code %d after %s%s\n",
					spec.Command, exitErr.ExitCode(), elapsed.Round(100*time.Millisecond), hint)
				// The pane erases itself when the hold ends (closeOnExit
				// "always"), taking the error text with it — anyone not
				// staring at the pane sees nothing. Push the failure to the
				// daemon the spec was claimed from so it lands in `wmux
				// watch` and the sidebar too. Best-effort: the hold and exit
				// code matter more than a notify that can't be delivered.
				pushNotifyErr(spec.ID, fmt.Sprintf("pane %s failed: %q exited with code %d after %s%s",
					spec.ID, spec.Command, exitErr.ExitCode(), elapsed.Round(100*time.Millisecond), hint))
				time.Sleep(paneHoldOpen)
			}
			os.Exit(exitErr.ExitCode())
		}
		paneExecFail("%v", err)
	}
}

// looksLikeWindowsCommand reports whether a --cmd value can only run
// natively on Windows: a drive-letter path, which bash inside a distro
// can never exec. Deliberately nothing broader — a bare foo.exe can be
// legitimate in plain mode via WSL interop, so that case is left to the
// fast-failure hold inside the pane instead of being rejected up front.
func looksLikeWindowsCommand(cmdline string) bool {
	fields := strings.Fields(cmdline)
	if len(fields) == 0 {
		return false
	}
	first := strings.Trim(fields[0], `"'`)
	return len(first) >= 3 && first[1] == ':' && (first[2] == '\\' || first[2] == '/')
}

// paneHoldOpen is how long a failing pane stays on screen before closing —
// the pane closes the instant its process exits (closeOnExit "always"),
// which would otherwise reduce every failure to an unreadable flash.
const paneHoldOpen = 10 * time.Second

// paneExecFail reports an error, holds the pane open long enough for a
// human to actually read it, and exits.
func paneExecFail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "wmux pane-exec: "+format+"\n", a...)
	time.Sleep(paneHoldOpen)
	os.Exit(1)
}

// focusScript is the PowerShell/UI-Automation script behind `wmux focus
// --id`. wt.exe can only move focus relative to the currently focused pane
// (move-focus) or by tab index — neither addresses "the pane running
// session X" — but every wmux pane keeps its session ID as its title
// (--title + --suppressApplicationTitle), so UIA can find it by name:
// bring its top-level terminal window to the foreground, select its tab,
// and put keyboard focus on the exact TermControl (which handles the
// split-pane case, where several panes share one tab). %s is the
// PowerShell-single-quoted session ID.
const focusScript = `
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
Add-Type @'
using System;
using System.Runtime.InteropServices;
public class WmuxFG { [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h); }
'@
$root = [System.Windows.Automation.AutomationElement]::RootElement
$cond = New-Object System.Windows.Automation.PropertyCondition([System.Windows.Automation.AutomationElement]::ClassNameProperty, 'CASCADIA_HOSTING_WINDOW_CLASS')
$wins = $root.FindAll([System.Windows.Automation.TreeScope]::Children, $cond)
foreach ($w in $wins) {
  $nc = New-Object System.Windows.Automation.PropertyCondition([System.Windows.Automation.AutomationElement]::NameProperty, %s)
  $hits = $w.FindAll([System.Windows.Automation.TreeScope]::Descendants, $nc)
  $tab = $null; $term = $null
  foreach ($h in $hits) {
    if ($h.Current.ControlType -eq [System.Windows.Automation.ControlType]::TabItem) { $tab = $h }
    if ($h.Current.ClassName -eq 'TermControl') { $term = $h }
  }
  if ($term -or $tab) {
    [WmuxFG]::SetForegroundWindow([IntPtr]$w.Current.NativeWindowHandle) | Out-Null
    if ($tab) { try { ($tab.GetCurrentPattern([System.Windows.Automation.SelectionItemPattern]::Pattern)).Select() } catch {} }
    if ($term) { $term.SetFocus() }
    Write-Output 'ok'
    exit 0
  }
}
Write-Output 'not-found'
exit 1
`

// cmdFocus switches Windows Terminal focus — the counterpart to `wmux
// pane` that lets an agent (or the user) steer which pane is active
// without touching the mouse. Two addressing modes:
//
//	--id ID    focus the pane/tab whose session ID this is, wherever it
//	           lives (any WT window; verified to land keyboard focus on
//	           the exact pane, including one half of a split)
//	--dir D    move focus one pane left/right/up/down within the most
//	           recently used WT window (plain `wt move-focus`) — relative,
//	           for "jump to the pane I just opened next to me"
//
// Like `wmux pane`, this drives wt.exe/UIA and must run from the Windows
// side, not from inside a WSL distro.
func cmdFocus(args []string) {
	fs := newFlagSet("focus")
	id := fs.String("id", "", "session ID whose pane/tab to focus (as set by wmux pane)")
	dir := fs.String("dir", "", "move pane focus within the current window: left, right, up, or down")
	fs.Parse(args)

	switch {
	case *dir != "" && *id == "":
		valid := map[string]bool{"left": true, "right": true, "up": true, "down": true}
		if !valid[*dir] {
			fmt.Fprintln(os.Stderr, "wmux focus: --dir must be left, right, up, or down")
			os.Exit(1)
		}
		// -w 0 targets the most recently used WT window — for an agent
		// running inside a pane, that's its own window.
		if err := exec.Command("wt.exe", "-w", "0", "move-focus", *dir).Run(); err != nil {
			fmt.Fprintf(os.Stderr, "wmux focus: could not launch wt.exe (is Windows Terminal installed and on PATH?): %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("moved focus %s\n", *dir)

	case *id != "" && *dir == "":
		if err := focusSessionByID(*id); err != nil {
			fmt.Fprintf(os.Stderr, "wmux focus: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("focused session %s\n", *id)

	default:
		fmt.Fprintln(os.Stderr, "wmux focus: exactly one of --id or --dir is required")
		os.Exit(1)
	}
}

// focusSessionByID runs the UIA focus script against a session ID — shared
// by `wmux focus --id` and the sidebar's Enter/click action.
func focusSessionByID(id string) error {
	script := fmt.Sprintf(focusScript, psQuote(id))
	// Read stdout only — powershell.exe emits CLIXML progress noise on
	// stderr ("Preparing modules for first use...") that would drown the
	// script's own ok/not-found verdict in a combined stream.
	out, err := exec.Command("powershell.exe", "-NoProfile", "-EncodedCommand", psEncodedCommand(script)).Output()
	switch result := strings.TrimSpace(string(out)); result {
	case "ok":
		return nil
	case "not-found":
		return fmt.Errorf("no pane or tab titled %q found in any Windows Terminal window", id)
	default:
		return fmt.Errorf("%v: %s", err, result)
	}
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
	*command = resolveCmd(*command)

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
		os.Exit(1)
	}

	err := closeSession(*id)
	if err == nil {
		fmt.Printf("closed session %s (a pane opened by wmux pane closes itself)\n", *id)
		return
	}
	// Unknown locally: a WSL-path pane session only ever registered with
	// the WSL-resident daemon (see bridge.go) — try there before failing.
	if errors.Is(err, errSessionNotFound) {
		if werr := wslDaemonClose(*id); werr == nil {
			fmt.Printf("closed session %s on the WSL daemon (a pane opened by wmux pane closes itself)\n", *id)
			return
		}
	}
	fmt.Fprintf(os.Stderr, "wmux close: %v\n", err)
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
	resp, err := http.Post(daemonAddr+"/sessions/close", "application/json", bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("could not reach wmuxd: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%w: %s", errSessionNotFound, strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("daemon returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
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
	resp, err := http.Post(daemonAddr+"/sessions/prune", "application/json", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux prune: could not reach wmuxd: %v\n", err)
		os.Exit(1)
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
	if len(result.Removed) == 0 {
		fmt.Println("nothing to prune (no exited sessions)")
		return
	}
	sort.Strings(result.Removed)
	fmt.Printf("pruned %d exited session(s): %s\n", len(result.Removed), strings.Join(result.Removed, ", "))
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
