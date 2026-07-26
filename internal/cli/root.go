// Package cli implements every `wmux` subcommand: session lifecycle,
// surfaces, agent notification hooks, daemon administration, and the
// launchers for the full-screen TUI (internal/tui).
//
// cmd/wmux is a three-line shim over Main below. Keeping the commands in
// an ordinary package — rather than in package main — is what lets the
// TUI and the layout engine be separate, independently testable packages
// that this one depends on, instead of 36 files sharing one namespace.
package cli

import (
	"fmt"
	"os"

	"github.com/alecthomas/kong"
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
  wmux                                   open the multiplexer (this is the main way in)
  wmux grid [N] [--claude|--codex|...]   open it with N panes in a grid, each running that agent
                                          (N defaults to 2; without an agent the panes are shells)
  wmux tui [CMD...]                      open it with one pane running CMD

  Inside the multiplexer: ctrl+o cycles panes, ctrl+b switches to COMMAND mode
  (| and - split, n new pane, x close, b sidebar, g grid, esc back, q quit),
  and clicking a pane or the sidebar focuses it.

  Every command below takes the same optional flags where they apply:
  --id (default: named after the directory), --cwd (default: here),
  --native/--distro (default: WSL on Windows). Pass a command as trailing
  arguments; --cmd - reads it from stdin instead, for commands with $(),
  quotes, or semicolons that shells/wsl.exe would mangle.

  wmux notify <message> [--session ID]   manually push a notification (testing)
  wmux hook run <agent> [--session ID]   generic agent hook target, driven by a per-agent profile
                                          (bundled: claude, codex, kimi, kiro; override or add via
                                          ~/.wmux/agents/<agent>.toml);
                                          --forward TOKEN (repeatable) chains a pre-existing notify handler
  wmux hook list                         list known agent profiles
  wmux hook-claude                       alias for 'wmux hook run claude' (reads stdin JSON)
  wmux hook-codex --session ID <json>    alias for 'wmux hook run codex' (JSON as final arg)
  wmux new [CMD...]                      spawn a HEADLESS agent session (no TTY; daemon owns the pipe)
  wmux attach [CMD...]                   run CMD interactively here (real TTY), tracked by the daemon
  wmux surface [CMD...]                  spawn CMD in a daemon-owned ConPTY (real TTY, runs headless,
                                          survives terminal close — tmux-style; defaults to a shell)
  wmux connect [ID]                      attach this terminal to a surface (Ctrl-] detaches, session
                                          keeps running); with no ID, the only running surface
  wmux theme [midnight|frost|gradient]   print the active theme (no arg), or persist a
                                          new one for the next launch
  wmux log [tail [-n N]|level [NAME]|path]
                                          inspect wmuxd's structured log (default: path + level);
                                          'level' with no arg prints it, with a name persists it
                                          for the next wmuxd start
  wmux debug state|panics|events|dump|pprof [cpu|heap|goroutine [seconds]]
                                          inspect wmuxd's own runtime state — session table,
                                          recovered panics, recent events, a bug-report bundle
                                          ('dump'), or a pprof profile written to disk
  wmux close [ID]                        kill a session's tracked process (with no ID, the only
                                          running one)
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
  wmux version                           print the wmux version`)
}

// Main is the wmux entry point (cmd/wmux is a shim over it).
//
// Bare `wmux` opens the multiplexer rather than printing usage: the TUI
// is the primary interface, and its own footer documents the keys, so a
// usage banner as the default landing screen was making the common case
// pay for the rare one. `wmux help` still prints it. Everything else
// goes through kong, whose usage errors (unknown or incomplete command)
// exit 2, leaving each command's own exit codes untouched.
func Main() {
	if len(os.Args) < 2 {
		cmdTui(nil)
		return
	}
	if os.Args[1] == "help" || os.Args[1] == "--help" || os.Args[1] == "-h" {
		usage()
		return
	}

	k, err := kong.New(&cli, kong.Name("wmux"), kong.NoDefaultHelp())
	if err != nil {
		panic(err) // static grammar error — a bug in this file, not a runtime condition
	}
	ctx, err := k.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux: %v\n", err)
		os.Exit(2)
	}
	if err := ctx.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "wmux: %v\n", err)
		os.Exit(1)
	}
}
