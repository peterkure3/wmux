package daemon

import (
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	pty "github.com/aymanbagabas/go-pty"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"

	"github.com/peterkure/wmux/internal/proto"
)

// defaultSurfaceCols/Rows size a surface before its first client resize.
const (
	defaultSurfaceCols = 120
	defaultSurfaceRows = 30
)

// Surface is the daemon-owned side of a ConPTY session: the daemon holds
// the pseudo-terminal and feeds its output through a VT emulator, so it
// always knows the session's current screen. Clients attach over
// GET /surfaces/attach and receive a VT replay (the current screen as an
// ANSI repaint) followed by ordered live output — tmux-style
// detach/reattach, where the session outlives any client terminal.
type Surface struct {
	mu      sync.Mutex
	pty     pty.Pty
	cmd     *pty.Cmd
	emu     *vt.Emulator
	cols    int
	rows    int
	exited  bool
	clients map[chan proto.SurfaceFrame]struct{}
}

// SpawnSurface creates a surface session: a ConPTY the daemon owns, the
// agent command running inside it, and a Session entry so the surface
// shows up in `wmux list`/the sidebar like any other session.
func (d *Daemon) SpawnSurface(req proto.NewSurfaceRequest) (*Session, error) {
	if req.ID == "" || req.Command == "" {
		return nil, fmt.Errorf("surface needs id and command")
	}

	d.mu.Lock()
	if existing, exists := d.sessions[req.ID]; exists {
		existing.mu.Lock()
		stillRunning := existing.running
		existing.mu.Unlock()
		if stillRunning {
			d.mu.Unlock()
			return nil, fmt.Errorf("session %q is already running", req.ID)
		}
		// existing entry has exited — fall through and replace it below.
	}
	d.mu.Unlock()

	cols, rows := req.Cols, req.Rows
	if cols < 2 || rows < 2 {
		cols, rows = defaultSurfaceCols, defaultSurfaceRows
	}

	p, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("could not allocate pty: %w", err)
	}
	if err := p.Resize(cols, rows); err != nil {
		p.Close()
		return nil, fmt.Errorf("could not size pty: %w", err)
	}

	cmd := buildSurfaceCommand(p, req)
	if err := cmd.Start(); err != nil {
		p.Close()
		return nil, fmt.Errorf("could not start %q: %w", req.Command, err)
	}

	sfc := &Surface{
		pty:     p,
		cmd:     cmd,
		emu:     vt.NewEmulator(cols, rows),
		cols:    cols,
		rows:    rows,
		clients: make(map[chan proto.SurfaceFrame]struct{}),
	}

	sess := &Session{
		ID: req.ID, Cwd: req.Cwd, Distro: req.Distro, Command: req.Command,
		pid: cmd.Process.Pid, native: req.Native || runtime.GOOS != "windows",
		running: true, sfc: sfc,
	}

	d.mu.Lock()
	d.sessions[req.ID] = sess
	d.mu.Unlock()

	go d.readSurface(sess)
	go d.reapSurface(sess)
	go d.pollMetadata(sess)
	d.save()
	d.publishSessions()

	return sess, nil
}

// buildSurfaceCommand constructs the process to run inside a surface's
// pty — the same platform split as buildCommand, but through the pty's
// own Command so the child is attached to the ConPTY. hideConsole is not
// needed: a ConPTY child renders into the pseudo-console, never a window
// of its own.
func buildSurfaceCommand(p pty.Pty, req proto.NewSurfaceRequest) *pty.Cmd {
	if runtime.GOOS == "windows" {
		if req.Native {
			// cmd.exe parses the commandline (it may carry arguments) and
			// resolves bare names against PATH; the child stays fully
			// interactive — it owns the ConPTY the same as under wt.exe.
			c := p.Command(resolveExe("cmd.exe"), "/c", req.Command)
			c.Dir = req.Cwd
			return c
		}
		args := append(wslArgs(req.Distro), "--cd", req.Cwd, "--", "bash", "-lc", req.Command)
		return p.Command(resolveExe("wsl.exe"), args...)
	}
	c := p.Command(resolveExe("bash"), "-lc", req.Command)
	c.Dir = req.Cwd
	return c
}

// resolveExe turns a bare executable name into an absolute path. go-pty's
// Cmd does not do exec.Cmd's PATH lookup — with a Dir set it resolves a
// relative name against Dir instead, so a plain "cmd.exe" becomes
// "<cwd>\cmd.exe" and fails (verified empirically).
func resolveExe(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

// readSurface pumps pty output into the VT emulator, scans it for OSC
// notify sequences (same sequences watchOutput handles for pipe
// sessions), and fans it out to attached clients. It returns when the
// pty is closed — ConPTY reads do NOT return EOF when the child exits,
// so reapSurface closing the pty is what actually unblocks this.
func (d *Daemon) readSurface(sess *Session) {
	sfc := sess.sfc
	const maxPending = 16 * 1024
	buf := make([]byte, 8192)
	var pending []byte

	for {
		n, err := sfc.pty.Read(buf)
		if n > 0 {
			chunk := buf[:n]

			sfc.mu.Lock()
			sfc.emu.Write(chunk)
			frame := proto.SurfaceFrame{Type: proto.FrameOutput, Data: append([]byte(nil), chunk...)}
			for ch := range sfc.clients {
				select {
				case ch <- frame:
				default: // slow client; drop rather than block the pty reader
				}
			}
			sfc.mu.Unlock()

			// OSC notify scan on the raw stream, identical policy to
			// watchOutput (see its comment for why this is byte-, not
			// line-, oriented).
			pending = append(pending, chunk...)
			for {
				loc := oscNotifyRe.FindSubmatchIndex(pending)
				if loc == nil {
					break
				}
				body := string(pending[loc[2]:loc[3]])

				sess.mu.Lock()
				sess.lastNote = body
				sess.mu.Unlock()

				d.publishNotify(proto.NotifyEvent{SessionID: sess.ID, Body: body, Time: time.Now()})
				log.Printf("[notify] session=%s body=%q", sess.ID, body)

				pending = pending[loc[1]:]
			}
			if len(pending) > maxPending {
				pending = pending[len(pending)-maxPending:]
			}
		}
		if err != nil {
			return
		}
	}
}

// reapSurface waits for the surface's child to exit, then closes the pty
// (which unblocks readSurface — ConPTY never delivers EOF on its own),
// marks the session exited, and tells attached clients.
func (d *Daemon) reapSurface(sess *Session) {
	sfc := sess.sfc
	err := sfc.cmd.Wait()

	sfc.mu.Lock()
	sfc.exited = true
	for ch := range sfc.clients {
		select {
		case ch <- proto.SurfaceFrame{Type: proto.FrameExit}:
		default:
		}
	}
	sfc.mu.Unlock()

	sfc.pty.Close()
	d.markExited(sess)
	if err != nil {
		log.Printf("surface %s exited: %v", sess.ID, err)
	} else {
		log.Printf("surface %s exited cleanly", sess.ID)
	}
}

// surface looks up a session's surface, or errors if the session doesn't
// exist, isn't a surface, or has already exited.
func (d *Daemon) surface(id string) (*Session, *Surface, error) {
	d.mu.RLock()
	sess, ok := d.sessions[id]
	d.mu.RUnlock()
	if !ok {
		return nil, nil, fmt.Errorf("session %q not found", id)
	}
	if sess.sfc == nil {
		if sess.wasSurface {
			return nil, nil, fmt.Errorf("surface %q did not survive a daemon restart (its ConPTY died with the old wmuxd process)", id)
		}
		return nil, nil, fmt.Errorf("session %q is not a surface (created by wmux new/attach, not wmux surface)", id)
	}
	sess.sfc.mu.Lock()
	exited := sess.sfc.exited
	sess.sfc.mu.Unlock()
	if exited {
		return nil, nil, fmt.Errorf("surface %q has exited", id)
	}
	return sess, sess.sfc, nil
}

// AttachSurface subscribes a client to a surface's output. The returned
// channel first carries a replay frame (the full current screen), then
// live output frames, then an exit frame if the process ends. Call
// DetachSurface when the client goes away.
func (d *Daemon) AttachSurface(id string) (chan proto.SurfaceFrame, error) {
	_, sfc, err := d.surface(id)
	if err != nil {
		return nil, err
	}

	// Big enough that a burst of output while the client drains the replay
	// doesn't hit the drop-if-full fan-out policy immediately.
	ch := make(chan proto.SurfaceFrame, 256)

	sfc.mu.Lock()
	ch <- proto.SurfaceFrame{
		Type: proto.FrameReplay, Cols: sfc.cols, Rows: sfc.rows,
		Data: sfc.replayLocked(),
	}
	sfc.clients[ch] = struct{}{}
	// The process may have exited between the surface() check above and
	// registering this client — reapSurface's exit broadcast would have
	// missed it, leaving the stream open forever. Re-check under the lock.
	if sfc.exited {
		ch <- proto.SurfaceFrame{Type: proto.FrameExit}
	}
	sfc.mu.Unlock()

	return ch, nil
}

// DetachSurface drops a client subscription created by AttachSurface.
func (d *Daemon) DetachSurface(id string, ch chan proto.SurfaceFrame) {
	d.mu.RLock()
	sess, ok := d.sessions[id]
	d.mu.RUnlock()
	if !ok || sess.sfc == nil {
		return
	}
	sess.sfc.mu.Lock()
	delete(sess.sfc.clients, ch)
	sess.sfc.mu.Unlock()
}

// InputSurface writes client keystrokes to the surface's pty.
func (d *Daemon) InputSurface(id string, data []byte) error {
	_, sfc, err := d.surface(id)
	if err != nil {
		return err
	}
	_, err = sfc.pty.Write(data)
	return err
}

// ResizeSurface resizes the pty and the VT screen model, then pushes a
// fresh replay at the new size to every attached client — inline in the
// same ordered stream, so clients repaint without a gap (the cmux-tui
// protocol's "resized" behavior).
func (d *Daemon) ResizeSurface(id string, cols, rows int) error {
	_, sfc, err := d.surface(id)
	if err != nil {
		return err
	}
	if cols < 2 || rows < 2 {
		return fmt.Errorf("size %dx%d too small", cols, rows)
	}

	sfc.mu.Lock()
	defer sfc.mu.Unlock()
	if cols == sfc.cols && rows == sfc.rows {
		return nil // no-op resize; no redundant replay (cmux-tui does the same)
	}
	if err := sfc.pty.Resize(cols, rows); err != nil {
		return fmt.Errorf("pty resize: %w", err)
	}
	sfc.emu.Resize(cols, rows)
	sfc.cols, sfc.rows = cols, rows

	frame := proto.SurfaceFrame{
		Type: proto.FrameReplay, Cols: cols, Rows: rows,
		Data: sfc.replayLocked(),
	}
	for ch := range sfc.clients {
		select {
		case ch <- frame:
		default:
		}
	}
	return nil
}

// replayLocked serializes the emulator's current screen into an ANSI
// repaint: clear, every row as minimal SGR runs, then the real cursor
// position. Emulator.Render() can't be used here — it returns plain text
// with all attributes stripped (verified empirically), which would lose
// colors on every reattach. Caller must hold sfc.mu.
func (s *Surface) replayLocked() []byte {
	var b strings.Builder
	b.Grow(s.cols * s.rows * 2)

	// Reset attributes, clear, home. A replay always repaints the whole
	// screen, so the client needs no prior state.
	b.WriteString("\x1b[0m\x1b[2J\x1b[H")

	cur := uv.Style{} // SGR state already emitted; zero value = fully reset
	for y := 0; y < s.rows; y++ {
		fmt.Fprintf(&b, "\x1b[%d;1H", y+1)
		for x := 0; x < s.cols; {
			cell := s.emu.CellAt(x, y)
			if cell == nil {
				b.WriteByte(' ')
				x++
				continue
			}
			if !cell.Style.Equal(&cur) {
				b.WriteString(cell.Style.Diff(&cur))
				cur = cell.Style
			}
			if cell.Content == "" {
				b.WriteByte(' ')
			} else {
				b.WriteString(cell.Content)
			}
			if cell.Width > 1 {
				x += cell.Width // wide grapheme occupies the following cell(s) too
			} else {
				x++
			}
		}
	}

	// Restore attributes and put the cursor where the session really has it.
	pos := s.emu.CursorPosition()
	fmt.Fprintf(&b, "\x1b[0m\x1b[%d;%dH", pos.Y+1, pos.X+1)

	return []byte(b.String())
}
