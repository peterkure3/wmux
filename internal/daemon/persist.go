package daemon

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
)

// persistedSession is the on-disk snapshot of a Session — a plain struct
// (no mutex, no *exec.Cmd) so it round-trips through JSON cleanly.
type persistedSession struct {
	ID       string `json:"id"`
	Cwd      string `json:"cwd"`
	Distro   string `json:"distro"`
	Command  string `json:"command"`
	PID      int    `json:"pid"`
	Native   bool   `json:"native"`
	Branch   string `json:"branch"`
	Ports    []int  `json:"ports"`
	LastNote string `json:"lastNote"`
	Running  bool   `json:"running"`
	Surface  bool   `json:"surface,omitempty"`
	// StartTime/Exe are the tracked process's identity (see identity.go).
	// Restoring running=true is only safe when the process at PID still
	// matches them; PID alone is not identity because the OS reuses PIDs.
	StartTime int64  `json:"startTime,omitempty"`
	Exe       string `json:"exe,omitempty"`
}

// DefaultStatePath is where the daemon persists session state between
// restarts, absent an explicit --state flag. Lives under the user's home
// directory rather than next to the binary or in the process's cwd, since
// wmuxd is typically launched from Startup/Task Scheduler/nohup with an
// arbitrary or irrelevant working directory.
func DefaultStatePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "wmuxd-state.json" // last resort: cwd
	}
	return filepath.Join(home, ".wmux", "state.json")
}

// save snapshots all sessions to disk. Called after every lifecycle
// transition (spawn/register/deregister/close) and once per pollMetadata
// tick, so a restart loses at most a few seconds of branch/port/note
// drift, never the session list itself.
func (d *Daemon) save() {
	if d.statePath == "" {
		return
	}
	d.saveMu.Lock()
	defer d.saveMu.Unlock()

	d.mu.RLock()
	snap := make([]persistedSession, 0, len(d.sessions))
	for _, s := range d.sessions {
		s.mu.Lock()
		snap = append(snap, persistedSession{
			ID: s.ID, Cwd: s.Cwd, Distro: s.Distro, Command: s.Command,
			PID: s.pid, Native: s.native, Branch: s.branch, Ports: s.ports,
			LastNote: s.lastNote, Running: s.running, Surface: s.sfc != nil,
			StartTime: s.ident.startTime, Exe: s.ident.exe,
		})
		s.mu.Unlock()
	}
	d.mu.RUnlock()

	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		slog.Error("could not marshal session state", "err", err)
		return
	}

	stateDir := filepath.Dir(d.statePath)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		slog.Error("could not create state directory", "err", err)
		return
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		slog.Error("could not secure state directory", "err", err)
		return
	}

	// Write to a temp file and rename over the real path so a crash
	// mid-write never leaves a truncated/corrupt state file behind.
	//
	// The fsync is load-bearing, not belt-and-braces: rename() is atomic
	// for the *directory entry*, but it says nothing about whether the
	// temp file's contents ever reached the disk. Without the sync, a
	// power loss between the write and the flush leaves a perfectly
	// atomic rename of a zero-length file over the good state — the exact
	// corruption the rename was supposed to prevent, just harder to spot.
	tmp, err := os.CreateTemp(stateDir, ".state-*.tmp")
	if err != nil {
		slog.Error("could not create state temp file", "err", err)
		return
	}
	tmpName := tmp.Name()
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(b)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmpName)
		slog.Error("could not write session state", "err", err)
		return
	}
	if err := os.Rename(tmpName, d.statePath); err != nil {
		slog.Error("could not finalize session state", "err", err)
		os.Remove(tmpName) // don't leave the orphan behind for the next run to trip over
		return
	}
	if err := os.Chmod(d.statePath, 0o600); err != nil {
		slog.Error("could not secure session state", "err", err)
	}
}

// load restores sessions from a previous run's snapshot, if one exists.
// Each restored session's PID is re-checked for identity — the process
// at that PID must still match the creation time and executable recorded
// with the session (see identity.go), since the OS reuses PIDs. A
// confirmed identity comes back as running, with metadata polling
// resumed; anything unconfirmed (dead process, recycled PID, state
// written before identity was recorded) comes back as exited — same as
// how a live session looks after its process ends normally, just
// discovered at startup instead of live.
//
// A restored Spawn-mode session loses two things a live one has: OSC
// notify parsing (the daemon no longer holds its stdout pipe after a
// restart) and clean reaping via cmd.Wait() (Go can only Wait() on a
// process it actually Start()ed as its own child, not an arbitrary PID
// discovered after the fact) — pollMetadata's liveness re-check on each
// tick is what eventually notices such a session has exited instead.
func (d *Daemon) load() {
	if d.statePath == "" {
		return
	}

	b, err := os.ReadFile(d.statePath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("could not read session state", "path", d.statePath, "err", err)
		}
		return
	}

	// Migrate state left by an older daemon with permissive modes: the file
	// holds command lines, paths, notes, and PIDs. Best-effort — a
	// read-only state directory must not break startup.
	stateDir := filepath.Dir(d.statePath)
	if err := os.MkdirAll(stateDir, 0o700); err == nil {
		os.Chmod(stateDir, 0o700)
	}
	os.Chmod(d.statePath, 0o600)

	var snap []persistedSession
	if err := json.Unmarshal(b, &snap); err != nil {
		slog.Error("could not parse session state", "path", d.statePath, "err", err)
		return
	}

	restored := 0
	var toPoll []*Session
	d.mu.Lock()
	for _, p := range snap {
		running := p.Running && p.PID != 0
		// A surface's ConPTY and screen state died with the old daemon
		// process — there is nothing to reattach to, so it always comes
		// back exited (its child, if somehow alive, lost its console).
		if p.Surface {
			running = false
		}
		// A persisted PID is not process identity: the OS may have reused
		// it for an unrelated process since the last save. A session only
		// comes back running — and thus controllable — when the process at
		// PID still matches the creation time and executable recorded with
		// it. Unconfirmed identity (stale PID, or state written before
		// identity was recorded) restores as exited; nothing killable by
		// number alone.
		want := procIdent{startTime: p.StartTime, exe: p.Exe}
		var ident procIdent
		// Only re-check identity for PIDs in our own namespace — a
		// WSL-registered session's PID means nothing to this side's
		// process table (see pidVisible), so its identity can never be
		// confirmed and it restores exited, never "running by hope".
		if running && pidVisible(p.Native, p.Command) && processAlive(p.PID) {
			ident, _ = processIdentity(p.PID)
		}
		if running && !sameProc(ident, want) {
			slog.Info("restored session identity unconfirmed; restoring as exited",
				"id", p.ID, "pid", p.PID)
			running = false
		}

		sess := &Session{
			ID: p.ID, Cwd: p.Cwd, Distro: p.Distro, Command: p.Command,
			pid: p.PID, native: p.Native, branch: p.Branch, ports: p.Ports,
			lastNote: p.LastNote, running: running, wasSurface: p.Surface,
			restored: true, ident: ident,
		}
		d.sessions[p.ID] = sess
		restored++

		if running {
			toPoll = append(toPoll, sess)
		}
	}
	d.mu.Unlock()

	// Started outside the lock, matching Register/Spawn's pattern —
	// pollMetadata only ever touches sess.mu and d.mu via d.save(), never
	// d.mu directly, but holding d.mu across goroutine starts is needless
	// scope creep.
	for _, sess := range toPoll {
		d.safeGo("pollMetadata:"+sess.ID, func() { d.pollMetadata(sess) })
	}

	if restored > 0 {
		slog.Info("restored sessions", "count", restored, "path", d.statePath)
	}
}
