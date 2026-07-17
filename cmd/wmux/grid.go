package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/peterkure/wmux/internal/proto"
)

// cmdGrid opens several agent panes at once in a single new Windows
// Terminal tab — the "start my whole workspace" version of `wmux pane`.
// Every pane runs the same command in the same cwd under its own session
// ID (the common case: N shells or N agents on one repo). Layouts are
// equal splits:
//
//	2 panes: side by side
//	3 panes: one full-height left, two stacked right
//	4 panes: 2x2 grid (top-left, top-right, bottom-right, bottom-left)
//
// It reuses the whole `wmux pane` machinery per pane — pane specs filed
// up front, the "wmux" WT profile, pane-exec claiming by title — chained
// into one wt.exe invocation with ";"-separated subcommands, so the tab
// appears fully laid out at once.
func cmdGrid(args []string) {
	fs := newFlagSet("grid")
	ids := fs.String("ids", "", "comma-separated session IDs, one per pane (2-4)")
	cwd := fs.String("cwd", "", "working directory for every pane")
	command := fs.String("cmd", "", "command every pane runs interactively")
	distro := fs.String("distro", "", "WSL distro name (ignored with --native)")
	native := fs.Bool("native", false, "run --cmd directly on Windows, no WSL")
	fs.Parse(args)
	*command = resolveCmd(*command)

	if *ids == "" || *cwd == "" || *command == "" {
		fmt.Fprintln(os.Stderr, "wmux grid: --ids, --cwd, and --cmd are required")
		os.Exit(1)
	}

	idList := parseGridIDs("grid", *ids)

	// Same up-front check as `wmux pane`: a Windows path handed to bash
	// inside WSL fails as an unreadable pane flash.
	if !*native && looksLikeWindowsCommand(*command) {
		fmt.Fprintf(os.Stderr, "wmux grid: --cmd %q looks like a native Windows command, but plain 'wmux grid' runs --cmd inside WSL — add --native\n", *command)
		os.Exit(1)
	}

	if err := ensureWTProfileFragment(); err != nil {
		fmt.Fprintf(os.Stderr, "wmux grid: could not install the 'wmux' Windows Terminal profile: %v\n", err)
		os.Exit(1)
	}

	// File every spec before wt.exe launches so no pane-exec can beat its
	// own spec to the daemon.
	for _, id := range idList {
		spec := proto.PaneSpec{ID: id, Cwd: *cwd, Distro: *distro, Command: *command, Native: *native}
		if err := filePaneSpec(spec); err != nil {
			fmt.Fprintf(os.Stderr, "wmux grid: %v\n", err)
			os.Exit(1)
		}
	}

	cmd := exec.Command("wt.exe", gridWTArgs(idList)...)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "wmux grid: could not launch wt.exe (is Windows Terminal installed and on PATH?): %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("opened %d-pane tab for sessions %s\n", len(idList), strings.Join(idList, ", "))
}

// parseGridIDs validates a comma-separated 2-4 pane ID list, shared by
// `wmux grid` and `wmux sidebar --grid`. Exits with a diagnostic under
// cmdName on any problem.
func parseGridIDs(cmdName, raw string) []string {
	var idList []string
	for _, id := range strings.Split(raw, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if id == sidebarTitle {
			fmt.Fprintf(os.Stderr, "wmux %s: session ID %q is reserved for the sidebar itself\n", cmdName, sidebarTitle)
			os.Exit(1)
		}
		idList = append(idList, id)
	}
	if len(idList) < 2 || len(idList) > 4 {
		fmt.Fprintf(os.Stderr, "wmux %s: need 2-4 pane IDs, got %d — for one pane use 'wmux pane'\n", cmdName, len(idList))
		os.Exit(1)
	}
	seen := map[string]bool{}
	for _, id := range idList {
		if seen[id] {
			fmt.Fprintf(os.Stderr, "wmux %s: duplicate pane ID %q\n", cmdName, id)
			os.Exit(1)
		}
		seen[id] = true
	}
	return idList
}

// gridPaneArgs are the per-pane wt.exe args of the wmux-profile pane flow.
func gridPaneArgs(id string) []string {
	return []string{"--title", id, "--suppressApplicationTitle", "--profile", "wmux"}
}

// gridWTArgs builds one chained wt.exe command line laying out 2-4 panes
// with equal splits in a fresh tab.
func gridWTArgs(ids []string) []string {
	args := []string{"-w", "0", "new-tab"}
	args = append(args, gridPaneArgs(ids[0])...)
	args = append(args, gridSplitArgs(ids)...)
	return args
}

// gridSplitArgs lays out ids[1:] around an already-placed, focused ids[0]
// pane. Every split halves the focused pane, and wt moves focus onto each
// new pane, so the sequences below land every ID in a predictable
// position (see cmdGrid's doc comment). "-V" splits side-by-side, "-H"
// stacked — wt names the flag after the divider's orientation, not the
// layout (same trap as cmdPane's --split handling). Shared with
// `wmux sidebar --grid`, which places ids[0] in the region right of the
// sidebar before applying these same splits.
func gridSplitArgs(ids []string) []string {
	var args []string
	switch len(ids) {
	case 2:
		// [0 | 1]
		args = append(args, ";", "split-pane", "-V")
		args = append(args, gridPaneArgs(ids[1])...)
	case 3:
		// [0 | 1]
		// [0 | 2]
		args = append(args, ";", "split-pane", "-V")
		args = append(args, gridPaneArgs(ids[1])...)
		args = append(args, ";", "split-pane", "-H")
		args = append(args, gridPaneArgs(ids[2])...)
	case 4:
		// [0 | 1]
		// [3 | 2]
		args = append(args, ";", "split-pane", "-V")
		args = append(args, gridPaneArgs(ids[1])...)
		args = append(args, ";", "split-pane", "-H")
		args = append(args, gridPaneArgs(ids[2])...)
		args = append(args, ";", "move-focus", "left")
		args = append(args, ";", "split-pane", "-H")
		args = append(args, gridPaneArgs(ids[3])...)
	}
	return args
}
