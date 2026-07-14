package daemon

import (
	"encoding/json"
	"log"
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

	d.mu.RLock()
	snap := make([]persistedSession, 0, len(d.sessions))
	for _, s := range d.sessions {
		s.mu.Lock()
		snap = append(snap, persistedSession{
			ID: s.ID, Cwd: s.Cwd, Distro: s.Distro, Command: s.Command,
			PID: s.pid, Native: s.native, Branch: s.branch, Ports: s.ports,
			LastNote: s.lastNote, Running: s.running,
		})
		s.mu.Unlock()
	}
	d.mu.RUnlock()

	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		log.Printf("wmuxd: could not marshal session state: %v", err)
		return
	}

	if err := os.MkdirAll(filepath.Dir(d.statePath), 0o755); err != nil {
		log.Printf("wmuxd: could not create state directory: %v", err)
		return
	}

	// Write to a temp file and rename over the real path so a crash
	// mid-write never leaves a truncated/corrupt state file behind.
	tmp := d.statePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		log.Printf("wmuxd: could not write session state: %v", err)
		return
	}
	if err := os.Rename(tmp, d.statePath); err != nil {
		log.Printf("wmuxd: could not finalize session state: %v", err)
	}
}

// load restores sessions from a previous run's snapshot, if one exists.
// Each restored session's PID is re-checked for liveness: still-alive
// processes come back as running (with metadata polling resumed), dead
// ones come back as exited — same as how a live session looks after its
// process ends normally, just discovered at startup instead of live.
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
			log.Printf("wmuxd: could not read session state at %s: %v", d.statePath, err)
		}
		return
	}

	var snap []persistedSession
	if err := json.Unmarshal(b, &snap); err != nil {
		log.Printf("wmuxd: could not parse session state at %s: %v", d.statePath, err)
		return
	}

	restored := 0
	var toPoll []*Session
	d.mu.Lock()
	for _, p := range snap {
		running := p.Running && p.PID != 0
		// Only re-check liveness for PIDs in our own namespace — a
		// WSL-registered session's PID means nothing to this side's
		// process table, and a wrong verdict here either kills a live
		// session's tracking or resurrects a dead one (see pidVisible).
		if running && pidVisible(p.Native, p.Command) {
			running = processAlive(p.PID)
		}

		sess := &Session{
			ID: p.ID, Cwd: p.Cwd, Distro: p.Distro, Command: p.Command,
			pid: p.PID, native: p.Native, branch: p.Branch, ports: p.Ports,
			lastNote: p.LastNote, running: running,
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
		go d.pollMetadata(sess)
	}

	if restored > 0 {
		log.Printf("wmuxd: restored %d session(s) from %s", restored, d.statePath)
	}
}
