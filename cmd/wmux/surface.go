package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/peterkure/wmux/internal/proto"
)

// detachByte is the connect client's reserved key: Ctrl-] (0x1D, telnet's
// escape). It detaches from the surface without ending it and is never
// forwarded to the session.
const detachByte = 0x1d

// cmdSurface creates a surface session: the daemon spawns the command
// inside a ConPTY it owns, so the session has a real TTY (interactive
// agents work, unlike `wmux new`) but no terminal of its own — it runs
// headless until a `wmux connect` views it, and it survives that client
// detaching or its terminal closing. tmux semantics, one session per
// surface.
func cmdSurface(args []string) {
	fs := newFlagSet("surface")
	id := fs.String("id", "", "session ID")
	cwd := fs.String("cwd", ".", "working directory")
	command := fs.String("cmd", "", "command to run, e.g. 'claude'")
	distro := fs.String("distro", "", "WSL distro name (ignored with --native)")
	native := fs.Bool("native", false, "run --cmd directly on the daemon's OS, no WSL")
	fs.Parse(args)

	if *id == "" || *command == "" {
		fmt.Fprintln(os.Stderr, "wmux surface: --id and --cmd are required")
		os.Exit(1)
	}

	req := proto.NewSurfaceRequest{
		ID: *id, Cwd: *cwd, Command: *command, Distro: *distro, Native: *native,
	}
	b, _ := json.Marshal(req)
	resp, err := http.Post(daemonAddr+"/surfaces", "application/json", bytes.NewReader(b))
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux surface: could not reach wmuxd: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "wmux surface: daemon returned %s: %s\n", resp.Status, string(body))
		os.Exit(1)
	}

	var info proto.SessionInfo
	if err := json.Unmarshal(body, &info); err != nil {
		fmt.Fprintf(os.Stderr, "wmux surface: could not parse daemon response: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("created surface %s (cwd=%s) — view it with: wmux connect --id %s\n",
		info.ID, info.Cwd, info.ID)
}

// cmdConnect attaches this terminal to a surface session: the daemon
// first sends a VT replay (the surface's full current screen), then
// streams live output; keystrokes are forwarded back, and terminal
// resizes propagate to the surface's pty. Ctrl-] detaches, leaving the
// session running — reconnect any time, from any terminal.
func cmdConnect(args []string) {
	fs := newFlagSet("connect")
	id := fs.String("id", "", "surface session ID to attach to")
	fs.Parse(args)

	if *id == "" {
		fmt.Fprintln(os.Stderr, "wmux connect: --id is required")
		os.Exit(1)
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "wmux connect: stdin is not a terminal")
		os.Exit(1)
	}

	// The attach stream lives on a cancellable context so detach can shut
	// it down cleanly from the stdin goroutine.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	attachURL := daemonAddr + "/surfaces/attach?id=" + url.QueryEscape(*id)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, attachURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux connect: could not reach wmuxd: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "wmux connect: daemon returned %s: %s", resp.Status, string(body))
		os.Exit(1)
	}

	// Raw mode from here on: keystrokes go to the surface, not this
	// process. enableVTOutput is a no-op outside Windows; inside a classic
	// console it turns on VT processing so the replay's escapes render.
	if err := enableVTOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "wmux connect: could not enable VT output: %v\n", err)
		os.Exit(1)
	}
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux connect: could not enter raw mode: %v\n", err)
		os.Exit(1)
	}
	restore := func() { term.Restore(int(os.Stdin.Fd()), oldState) }

	// Size the surface to this terminal before the replay is on screen
	// long — the daemon answers the resize with a fresh replay anyway, so
	// a size mismatch self-corrects immediately.
	lastCols, lastRows := -1, -1
	sendResize := func() {
		cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil || (cols == lastCols && rows == lastRows) {
			return
		}
		lastCols, lastRows = cols, rows
		b, _ := json.Marshal(proto.SurfaceResizeRequest{ID: *id, Cols: cols, Rows: rows})
		if resp, err := http.Post(daemonAddr+"/surfaces/resize", "application/json", bytes.NewReader(b)); err == nil {
			resp.Body.Close()
		}
	}
	sendResize()

	// Windows has no SIGWINCH; poll for size changes instead. 500ms is
	// imperceptible next to the repaint the resize itself causes.
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sendResize()
			}
		}
	}()

	// Forward stdin to the surface, watching for the detach key. Reads
	// block; after cancel() the main goroutine exits the process, so a
	// blocked read here never leaks past process end.
	detached := make(chan struct{})
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if i := bytes.IndexByte(buf[:n], detachByte); i != -1 {
					// Forward what preceded the detach key, then stop.
					if i > 0 {
						postInput(*id, buf[:i])
					}
					close(detached)
					cancel()
					return
				}
				postInput(*id, buf[:n])
			}
			if err != nil {
				cancel()
				return
			}
		}
	}()

	fmt.Fprintf(os.Stderr, "[wmux: attached to %s — Ctrl-] to detach]\r\n", *id)

	// Main loop: decode frames off the stream and paint them. Replay
	// frames start with a clear-screen, so writing them verbatim is a
	// full repaint; output frames are raw pty bytes.
	dec := json.NewDecoder(resp.Body)
	exited := false
	for {
		var frame proto.SurfaceFrame
		if err := dec.Decode(&frame); err != nil {
			break // stream closed: daemon gone, detach cancel, or exit frame already handled
		}
		switch frame.Type {
		case proto.FrameReplay, proto.FrameOutput:
			os.Stdout.Write(frame.Data)
		case proto.FrameExit:
			exited = true
		}
		if exited {
			break
		}
	}

	restore()
	select {
	case <-detached:
		fmt.Fprintf(os.Stderr, "\r\n[wmux: detached from %s — it keeps running; reconnect with 'wmux connect --id %s']\r\n", *id, *id)
	default:
		if exited {
			fmt.Fprintf(os.Stderr, "\r\n[wmux: surface %s exited]\r\n", *id)
		} else {
			fmt.Fprintf(os.Stderr, "\r\n[wmux: connection to wmuxd lost]\r\n")
		}
	}
	// The stdin goroutine may still be parked in a blocking Read; exiting
	// the process is the only reliable way to end it on Windows.
	os.Exit(0)
}

// postInput sends keystrokes to the surface's pty. Errors are swallowed:
// mid-typing there is nothing useful to do with one, and the attach
// stream ending is what actually tells the user the session is gone.
func postInput(id string, data []byte) {
	b, _ := json.Marshal(proto.SurfaceInputRequest{ID: id, Data: data})
	if resp, err := http.Post(daemonAddr+"/surfaces/input", "application/json", bytes.NewReader(b)); err == nil {
		resp.Body.Close()
	}
}
