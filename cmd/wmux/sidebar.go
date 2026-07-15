// wmux sidebar opens the sidebar TUI (see sidebarui.go) in a Windows
// Terminal pane, reusing the same self-closing "wmux" profile flow as
// `wmux pane`. The launcher passes the reserved title "wmux-sidebar";
// `wmux pane-exec` recognizes it and runs the TUI in-process instead of
// claiming a pane spec — so the sidebar never registers as a session and
// needs no daemon handshake to start.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/peterkure/wmux/internal/proto"
)

// sidebarTitle is the reserved pane title that tells pane-exec to run the
// sidebar TUI. Session IDs may not collide with it (see cmdPane).
const sidebarTitle = "wmux-sidebar"

// cmdSidebar opens the sidebar as a new tab's first (and leftmost) pane.
// wt.exe's CLI can only split right or down of the focused pane — there is
// no split-left and no swap-pane — so "sidebar on the left" is guaranteed
// by opening the sidebar first and splitting agent panes right from it,
// not by moving an existing pane. --with opens the first agent pane in the
// same wt.exe invocation, sized so the sidebar keeps ~22% of the tab.
func cmdSidebar(args []string) {
	fs := newFlagSet("sidebar")
	with := fs.String("with", "", "also open a first agent pane running this command (native Windows)")
	cwd := fs.String("cwd", "", "working directory for --with")
	id := fs.String("id", "", "session ID for --with (defaults to the cwd's base name)")
	distro := fs.String("distro", "", "WSL distro for --with (implies a WSL pane)")
	native := fs.Bool("native", false, "run --with directly on Windows, no WSL")
	fs.Parse(args)

	if err := ensureWTProfileFragment(); err != nil {
		fmt.Fprintf(os.Stderr, "wmux sidebar: could not install the 'wmux' Windows Terminal profile: %v\n", err)
		os.Exit(1)
	}

	wtArgs := []string{"-w", "0", "new-tab",
		"--title", sidebarTitle, "--suppressApplicationTitle", "--profile", "wmux"}

	if *with != "" {
		if *cwd == "" {
			fmt.Fprintln(os.Stderr, "wmux sidebar: --with requires --cwd")
			os.Exit(1)
		}
		if !*native && looksLikeWindowsCommand(*with) {
			fmt.Fprintf(os.Stderr, "wmux sidebar: --with %q looks like a native Windows command — add --native\n", *with)
			os.Exit(1)
		}
		paneID := *id
		if paneID == "" {
			paneID = defaultPaneID(*cwd)
		}
		if paneID == sidebarTitle {
			fmt.Fprintf(os.Stderr, "wmux sidebar: session ID %q is reserved for the sidebar itself\n", sidebarTitle)
			os.Exit(1)
		}
		spec := proto.PaneSpec{ID: paneID, Cwd: *cwd, Distro: *distro, Command: *with, Native: *native}
		if err := filePaneSpec(spec); err != nil {
			fmt.Fprintf(os.Stderr, "wmux sidebar: %v\n", err)
			os.Exit(1)
		}
		// One chained wt.exe invocation: the split targets the tab the
		// first subcommand just created, and -s 0.78 gives the new (agent)
		// pane 78% of the width, leaving the sidebar 22%.
		wtArgs = append(wtArgs, ";", "split-pane", "-V", "-s", "0.78",
			"--title", paneID, "--suppressApplicationTitle", "--profile", "wmux")
	}

	if err := exec.Command("wt.exe", wtArgs...).Start(); err != nil {
		fmt.Fprintf(os.Stderr, "wmux sidebar: could not launch wt.exe (is Windows Terminal installed and on PATH?): %v\n", err)
		os.Exit(1)
	}
	if *with != "" {
		fmt.Println("opened sidebar tab with agent pane")
	} else {
		fmt.Println("opened sidebar tab (open agent panes with 'wmux pane --split right' or the sidebar's n key)")
	}
}

// defaultPaneID derives a session ID from a working directory's base name.
func defaultPaneID(cwd string) string {
	base := filepath.Base(strings.TrimRight(cwd, `\/`))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "pane"
	}
	return base
}

// filePaneSpec files a pending pane spec with the daemon — the handshake
// half of the wmux-profile pane flow, shared by cmdPane, cmdSidebar, and
// the sidebar TUI's n action.
func filePaneSpec(spec proto.PaneSpec) error {
	b, _ := json.Marshal(spec)
	resp, err := http.Post(daemonAddr+"/panes/pending", "application/json", bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("could not reach wmuxd (is it running?): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("daemon returned %s: %s", resp.Status, string(body))
	}
	return nil
}
