// The two launchers for internal/tui: `wmux` / `wmux tui` (one optional
// starting pane) and `wmux grid` (N panes at once, optionally all running
// the same agent).
//
// Both follow the same flag policy: nothing is required. Every value has
// a defensible default — the current directory, a session ID derived from
// it, WSL or native chosen from the daemon's own platform — and the flags
// exist only for the cases where the default is wrong.
package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/peterkure3/wmux/internal/agentprofile"
	"github.com/peterkure3/wmux/internal/tui"
)

// defaultGridPanes is what `wmux grid` opens with no count — two panes
// side by side, the smallest arrangement that is actually a grid.
const defaultGridPanes = 2

// maxGridPanes caps `wmux grid N`. Past this the panes are too small to
// hold an agent's output on any normal terminal, and the daemon is
// spawning N real ptys per invocation.
const maxGridPanes = 16

// runTUI is the shared tail of every TUI launcher: point the TUI at the
// same daemon the CLI uses, then hand over the terminal.
func runTUI(cmdName string, opts tui.Options) {
	tui.SetClient(dc())
	if err := tui.Run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "wmux %s: %v\n", cmdName, err)
		os.Exit(1)
	}
}

// cmdTui opens the multiplexer. This is also what plain `wmux` runs.
//
// It always opens with the sidebar and one pane — trailing arguments (or
// --with) name the pane's command; with none it falls back to a shell,
// same as `wmux surface`/`wmux grid` do when no command was named.
func cmdTui(args []string) {
	fs := newFlagSet("tui")
	with := fs.String("with", "", "command for the first pane (or pass it as trailing arguments)")
	cwd := fs.String("cwd", "", "working directory for the first pane (default: the current directory)")
	id := fs.String("id", "", "session ID for the first pane (default: derived from --cwd)")
	distro := fs.String("distro", "", "WSL distro for the first pane (ignored with --native)")
	native := fs.Bool("native", false, "run the first pane directly on the daemon's OS, no WSL")
	fs.Parse(args)

	command := resolveCmd(positionalCommand(*with, fs.Args()))
	if command == "" {
		command = defaultShellCommand(*native)
	}

	opts := tui.Options{
		Open: []tui.PaneSpec{{
			ID: *id, Cwd: resolveCwd(*cwd), Command: command,
			Distro: *distro, Native: *native,
		}},
	}
	runTUI("tui", opts)
}

// cmdGrid opens the multiplexer with N panes arranged in a balanced grid:
//
//	wmux grid 4               four empty shells, 2x2
//	wmux grid 4 --claude      the same grid, every pane running claude
//	wmux grid --codex         the default two panes, both running codex
//
// The agent flags (--claude/--codex/--kimi/--mimo/...) are generated from
// the same profile set `wmux hook run` uses, so an agent added there is
// immediately spawnable here.
func cmdGrid(args []string) {
	fs := newFlagSet("grid")
	agent := fs.String("agent", "", "run this agent in every pane (equivalent to --<name>)")
	command := fs.String("cmd", "", "run this exact command in every pane")
	cwd := fs.String("cwd", "", "working directory for every pane (default: the current directory)")
	distro := fs.String("distro", "", "WSL distro for every pane (ignored with --native)")
	native := fs.Bool("native", false, "run the panes directly on the daemon's OS, no WSL")

	// One boolean per known agent, so `wmux grid 4 --claude` works as
	// written rather than needing --agent=claude.
	agentFlags := map[string]*bool{}
	for _, name := range agentprofile.List() {
		agentFlags[name] = fs.Bool(name, false, "run "+name+" in every pane")
	}

	// The count is pulled out before parsing: flag stops at the first
	// non-flag argument, so leaving "4" in place would silently discard
	// the --claude in `wmux grid 4 --claude` — the exact form this
	// command exists to support.
	count, rest := splitGridArgs(args)
	fs.Parse(rest)

	for name, set := range agentFlags {
		if *set {
			*agent = name
		}
	}

	n, err := gridCount(append(count, fs.Args()...))
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux grid: %v\n", err)
		os.Exit(2)
	}

	paneCmd := resolveCmd(*command)
	if paneCmd == "" && *agent != "" {
		p, err := agentprofile.Load(*agent)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wmux grid: %v\n", err)
			os.Exit(2)
		}
		paneCmd = p.Command()
	}
	if paneCmd == "" {
		paneCmd = defaultShellCommand(*native)
	}

	dir := resolveCwd(*cwd)
	base := paneIDBase(dir, *agent)
	// Every pane runs the same command; they differ only by ID, which is
	// what the sidebar and `wmux close` address them by.
	opts := tui.Options{Grid: true}
	for i := 1; i <= n; i++ {
		opts.Open = append(opts.Open, tui.PaneSpec{
			ID:      fmt.Sprintf("%s-%d", base, i),
			Cwd:     dir,
			Command: paneCmd,
			Distro:  *distro,
			Native:  *native,
		})
	}
	runTUI("grid", opts)
}

// splitGridArgs lifts the pane count out of `wmux grid`'s arguments,
// wherever it sits, and returns it separately from the flags. Anything
// that isn't a bare number is left in rest, so a typo still reaches
// gridCount and gets a real error rather than being silently ignored.
func splitGridArgs(args []string) (count, rest []string) {
	for _, a := range args {
		if len(count) == 0 && a != "" && !strings.HasPrefix(a, "-") && isAllDigits(a) {
			count = append(count, a)
			continue
		}
		rest = append(rest, a)
	}
	return count, rest
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// gridCount reads the optional pane count positional argument.
func gridCount(args []string) (int, error) {
	if len(args) == 0 {
		return defaultGridPanes, nil
	}
	n, err := strconv.Atoi(args[0])
	if err != nil {
		return 0, fmt.Errorf("%q is not a pane count — usage: wmux grid [N] [--<agent>]", args[0])
	}
	if n < 1 || n > maxGridPanes {
		return 0, fmt.Errorf("pane count %d out of range (1-%d)", n, maxGridPanes)
	}
	return n, nil
}

// paneIDBase names a grid's panes after the agent filling them, falling
// back to the directory they run in.
func paneIDBase(cwd, agent string) string {
	if agent != "" {
		return agent
	}
	return tui.DefaultPaneID(cwd)
}
