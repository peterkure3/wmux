package daemon

import (
	"fmt"
	"image/color"
	"log/slog"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"sync"

	pty "github.com/aymanbagabas/go-pty"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"

	"github.com/peterkure3/wmux/internal/proto"
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

	// cellClients and cellSnapshot back the rich (mode=cells) attach path
	// — see cellsframe.go. cellSnapshot is the Runs last broadcast for
	// each row, shared across every cells client (not per-client: attach
	// and the write-batch broadcast below both hold mu for their whole
	// duration, so "changed since the last frame" is unambiguous for
	// every currently-attached cells client at any instant — a new
	// client's own baseline is the direct read its attach takes, not
	// this cache).
	cellClients  map[chan proto.CellsFrame]struct{}
	cellSnapshot [][]proto.Run
}

// SpawnSurface creates a surface session: a ConPTY the daemon owns, the
// agent command running inside it, and a Session entry so the surface
// shows up in `wmux list`/the sidebar like any other session.
func (d *Daemon) SpawnSurface(req proto.NewSurfaceRequest) (*Session, error) {
	if req.ID == "" || req.Command == "" {
		return nil, fmt.Errorf("surface needs id and command")
	}

	// Claim the ID before allocating anything, so a concurrent request for
	// the same ID cannot end up with two live ConPTYs where only one is
	// reachable — see reserveID.
	sess := &Session{
		ID: req.ID, Cwd: req.Cwd, Distro: req.Distro, Command: req.Command,
		native: req.Native || runtime.GOOS != "windows", running: true,
	}
	if err := d.reserveID(sess); err != nil {
		return nil, err
	}

	cols, rows := req.Cols, req.Rows
	if cols < 2 || rows < 2 {
		cols, rows = defaultSurfaceCols, defaultSurfaceRows
	}

	p, err := pty.New()
	if err != nil {
		d.releaseID(sess)
		return nil, fmt.Errorf("could not allocate pty: %w", err)
	}
	if err := p.Resize(cols, rows); err != nil {
		p.Close()
		d.releaseID(sess)
		return nil, fmt.Errorf("could not size pty: %w", err)
	}

	cmd := buildSurfaceCommand(p, req)
	if err := cmd.Start(); err != nil {
		p.Close()
		d.releaseID(sess)
		return nil, fmt.Errorf("could not start %q: %w", req.Command, err)
	}

	sfc := &Surface{
		pty:         p,
		cmd:         cmd,
		emu:         vt.NewEmulator(cols, rows),
		cols:        cols,
		rows:        rows,
		clients:     make(map[chan proto.SurfaceFrame]struct{}),
		cellClients: make(map[chan proto.CellsFrame]struct{}),
	}

	// killOnClose is true for surfaces, unlike Spawn-mode sessions: a
	// surface already cannot survive a daemon restart (its ConPTY dies with
	// the daemon, which load() encodes by restoring it as exited), so tying
	// the process tree's lifetime to the daemon's loses nothing and stops
	// the agent and its children from leaking on every wmuxd exit.
	job, jerr := newJob(cmd.Process, true)
	if jerr != nil {
		slog.Warn("could not confine surface to a job object; descendants may outlive it",
			"id", sess.ID, "err", jerr)
	}

	sess.mu.Lock()
	sess.pid = cmd.Process.Pid
	sess.sfc = sfc
	sess.job = job
	sess.mu.Unlock()

	d.safeGo("readSurface:"+sess.ID, func() { d.readSurface(sess) })
	d.safeGo("reapSurface:"+sess.ID, func() { d.reapSurface(sess) })
	d.safeGo("pollMetadata:"+sess.ID, func() { d.pollMetadata(sess) })
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
		// --exec for the same reason as buildCommand: the plain -- form
		// double-expands the command through the distro's default shell.
		args := append(wslArgs(req.Distro), "--cd", req.Cwd, "--exec", "bash", "-lc", req.Command)
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
	sfc := sess.surfaceRef()
	if sfc == nil {
		return
	}
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
			sfc.broadcastCellsUpdateLocked()
			sfc.mu.Unlock()

			// OSC notify scan on the raw stream, identical policy to
			// watchOutput (see its comment for why this is byte-, not
			// line-, oriented).
			pending = append(pending, chunk...)
			pending = trimPending(d.scanNotes(sess, pending))
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
	sfc := sess.surfaceRef()
	if sfc == nil {
		return
	}
	err := sfc.cmd.Wait()

	sfc.mu.Lock()
	sfc.exited = true
	for ch := range sfc.clients {
		select {
		case ch <- proto.SurfaceFrame{Type: proto.FrameExit}:
		default:
		}
	}
	for ch := range sfc.cellClients {
		select {
		case ch <- proto.CellsFrame{Type: proto.CellsExit}:
		default:
		}
	}
	sfc.mu.Unlock()

	sfc.pty.Close()

	// The tree is gone; drop the job handle so it isn't held for the life
	// of the daemon. On a killOnClose job this is also what reaps any
	// descendant that outlived its parent.
	sess.mu.Lock()
	job := sess.job
	sess.job = jobHandle{}
	sess.mu.Unlock()
	job.release()

	d.markExited(sess)
	if err != nil {
		slog.Info("surface exited", "id", sess.ID, "err", err)
	} else {
		slog.Info("surface exited cleanly", "id", sess.ID)
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
	sfc := sess.surfaceRef()
	if sfc == nil {
		sess.mu.Lock()
		wasSurface := sess.wasSurface
		sess.mu.Unlock()
		if wasSurface {
			return nil, nil, fmt.Errorf("surface %q did not survive a daemon restart (its ConPTY died with the old wmuxd process)", id)
		}
		return nil, nil, fmt.Errorf("session %q is not a surface (created by wmux new/attach, not wmux surface)", id)
	}
	sfc.mu.Lock()
	exited := sfc.exited
	sfc.mu.Unlock()
	if exited {
		return nil, nil, fmt.Errorf("surface %q has exited", id)
	}
	return sess, sfc, nil
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
	if !ok {
		return
	}
	sfc := sess.surfaceRef()
	if sfc == nil {
		return
	}
	sfc.mu.Lock()
	delete(sfc.clients, ch)
	sfc.mu.Unlock()
}

// AttachSurfaceCells is AttachSurface for the rich (mode=cells) client
// path (see proto.CellsFrame): the returned channel first carries a full
// "replay" frame (every row), then "update" frames naming only the rows
// that changed since the previous frame this client received, then an
// "exit" frame if the process ends. Call DetachSurfaceCells when the
// client goes away.
func (d *Daemon) AttachSurfaceCells(id string) (chan proto.CellsFrame, error) {
	_, sfc, err := d.surface(id)
	if err != nil {
		return nil, err
	}

	ch := make(chan proto.CellsFrame, 256)

	sfc.mu.Lock()
	ch <- sfc.cellsReplayFrameLocked()
	sfc.cellClients[ch] = struct{}{}
	if sfc.exited {
		ch <- proto.CellsFrame{Type: proto.CellsExit}
	}
	sfc.mu.Unlock()

	return ch, nil
}

// DetachSurfaceCells drops a client subscription created by
// AttachSurfaceCells.
func (d *Daemon) DetachSurfaceCells(id string, ch chan proto.CellsFrame) {
	d.mu.RLock()
	sess, ok := d.sessions[id]
	d.mu.RUnlock()
	if !ok {
		return
	}
	sfc := sess.surfaceRef()
	if sfc == nil {
		return
	}
	sfc.mu.Lock()
	delete(sfc.cellClients, ch)
	sfc.mu.Unlock()
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

	cellsFrame := sfc.cellsReplayFrameLocked()
	for ch := range sfc.cellClients {
		select {
		case ch <- cellsFrame:
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

// cellsCursorLocked reports the cursor position for a CellsFrame. Caller
// must hold sfc.mu.
//
// Visible is always true: the vt emulator tracks DECTCEM cursor-hidden
// state internally but doesn't expose a public getter for it (verified
// against the package's exported API), and replayLocked has the same gap
// today — every ANSI replay already positions the cursor unconditionally.
// This isn't a new limitation this feature introduces.
func (s *Surface) cellsCursorLocked() (proto.Pos, bool) {
	pos := s.emu.CursorPosition()
	return proto.Pos{X: pos.X, Y: pos.Y}, true
}

// buildRunsForRowLocked scans row y and coalesces consecutive same-style
// cells into Runs — the CellsFrame equivalent of replayLocked's per-row
// ANSI loop, minus the escape sequences. Caller must hold sfc.mu.
func (s *Surface) buildRunsForRowLocked(y int) []proto.Run {
	var runs []proto.Run
	var cur uv.Style
	var text strings.Builder
	startX := -1

	flush := func() {
		if startX == -1 || text.Len() == 0 {
			return
		}
		runs = append(runs, proto.Run{
			X: startX, Text: text.String(),
			FG: colorHex(cur.Fg), BG: colorHex(cur.Bg), Attrs: styleAttrs(&cur),
		})
		text.Reset()
	}

	for x := 0; x < s.cols; {
		cell := s.emu.CellAt(x, y)
		content := " "
		var style uv.Style
		width := 1
		if cell != nil {
			if cell.Content != "" {
				content = cell.Content
			}
			style = cell.Style
			if cell.Width > 1 {
				width = cell.Width
			}
		}
		if startX == -1 || !style.Equal(&cur) {
			flush()
			cur = style
			startX = x
		}
		text.WriteString(content)
		x += width
	}
	flush()
	return runs
}

// cellsReplayFrameLocked builds a full "replay" CellsFrame — every row —
// and resets cellSnapshot to match it, so the next write-batch diff
// (broadcastCellsUpdateLocked) starts clean instead of re-sending
// everything as a redundant "update" right after. Caller must hold
// sfc.mu; called from AttachSurfaceCells (a new client's baseline) and
// ResizeSurface (dimensions changed, every existing client needs a fresh
// baseline too).
func (s *Surface) cellsReplayFrameLocked() proto.CellsFrame {
	rows := make([]proto.RowUpdate, s.rows)
	snapshot := make([][]proto.Run, s.rows)
	for y := 0; y < s.rows; y++ {
		runs := s.buildRunsForRowLocked(y)
		rows[y] = proto.RowUpdate{Y: y, Runs: runs}
		snapshot[y] = runs
	}
	s.cellSnapshot = snapshot

	cursor, visible := s.cellsCursorLocked()
	return proto.CellsFrame{
		Type: proto.CellsReplay, Rows: rows, Cursor: cursor, Visible: visible,
		Cols: s.cols, RowCnt: s.rows,
	}
}

// broadcastCellsUpdateLocked diffs every row's current Runs against
// cellSnapshot (the last frame broadcast to cells clients) and, if
// anything changed, sends an "update" CellsFrame naming only the changed
// rows to every attached cells client. Skips the scan entirely when no
// cells client is attached, so a plain `wmux connect` session pays
// nothing for this feature. Caller must hold sfc.mu; called after every
// pty read in readSurface, mirroring the existing bytes-mode broadcast
// right above it.
func (s *Surface) broadcastCellsUpdateLocked() {
	if len(s.cellClients) == 0 {
		return
	}
	if len(s.cellSnapshot) != s.rows {
		s.cellSnapshot = make([][]proto.Run, s.rows)
	}

	var changed []proto.RowUpdate
	for y := 0; y < s.rows; y++ {
		runs := s.buildRunsForRowLocked(y)
		if !rowsEqual(runs, s.cellSnapshot[y]) {
			changed = append(changed, proto.RowUpdate{Y: y, Runs: runs})
			s.cellSnapshot[y] = runs
		}
	}
	if len(changed) == 0 {
		return
	}

	cursor, visible := s.cellsCursorLocked()
	frame := proto.CellsFrame{Type: proto.CellsUpdate, Rows: changed, Cursor: cursor, Visible: visible}
	for ch := range s.cellClients {
		select {
		case ch <- frame:
		default: // slow client; drop rather than block the pty reader
		}
	}
}

// rowsEqual compares two rows' Runs for equality — small slices of small
// value structs, so reflect.DeepEqual's overhead is negligible next to
// the CellAt() scan that built them.
func rowsEqual(a, b []proto.Run) bool {
	return reflect.DeepEqual(a, b)
}

// colorHex renders a color.Color as "#rrggbb", or "" for nil (meaning
// "default color", not black). RGBA() returns 16-bit premultiplied
// components; terminal colors are always fully opaque, so the low byte
// of each is discarded rather than unpremultiplied.
func colorHex(c color.Color) string {
	if c == nil {
		return ""
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

// styleAttrs translates ultraviolet's Style bits into proto's own Attr*
// bitmask (see Run.Attrs) — kept as an explicit translation, not a raw
// copy of ultraviolet's bit numbering, so the wire format doesn't change
// out from under clients if that library's internal layout ever does.
func styleAttrs(s *uv.Style) uint8 {
	var a uint8
	if s.Attrs&uv.AttrBold != 0 {
		a |= proto.AttrBold
	}
	if s.Attrs&uv.AttrFaint != 0 {
		a |= proto.AttrFaint
	}
	if s.Attrs&uv.AttrItalic != 0 {
		a |= proto.AttrItalic
	}
	if s.Attrs&uv.AttrBlink != 0 {
		a |= proto.AttrBlink
	}
	if s.Attrs&uv.AttrReverse != 0 {
		a |= proto.AttrReverse
	}
	if s.Attrs&uv.AttrStrikethrough != 0 {
		a |= proto.AttrStrikethrough
	}
	if s.Underline != uv.UnderlineNone {
		a |= proto.AttrUnderline
	}
	return a
}
