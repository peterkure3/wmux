package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// statePath returns a throwaway state file path for one test.
func statePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state.json")
}

// addSession installs a session directly, bypassing Register/Spawn so no
// pollMetadata goroutine starts shelling out to git during a unit test.
func addSession(d *Daemon, s *Session) {
	d.mu.Lock()
	d.sessions[s.ID] = s
	d.mu.Unlock()
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := statePath(t)

	d := New(path, "")
	addSession(d, &Session{
		ID: "alpha", Cwd: "/home/p/proj", Distro: "Arch", Command: "claude",
		pid: 4242, native: true, branch: "main", ports: []int{3000, 8080},
		lastNote: "build done", running: true,
	})
	d.save()

	// A fresh daemon over the same file is exactly what a restart is.
	d2 := New(path, "")
	d2.mu.RLock()
	got, ok := d2.sessions["alpha"]
	d2.mu.RUnlock()
	if !ok {
		t.Fatal("session alpha did not survive the restart")
	}

	if got.Cwd != "/home/p/proj" || got.Distro != "Arch" || got.Command != "claude" {
		t.Errorf("identity fields lost: %+v", got)
	}
	if got.branch != "main" || got.lastNote != "build done" {
		t.Errorf("metadata lost: branch=%q lastNote=%q", got.branch, got.lastNote)
	}
	if len(got.ports) != 2 || got.ports[0] != 3000 || got.ports[1] != 8080 {
		t.Errorf("ports = %v, want [3000 8080]", got.ports)
	}
	if !got.native {
		t.Error("native flag lost — a Windows daemon would start shelling into WSL for a native session")
	}
}

// TestLoadSurfaceAlwaysExited pins the rule in load()'s doc comment: a
// surface's ConPTY and screen state die with the daemon process that owned
// them, so a restored surface can never come back running no matter what
// the snapshot said or whether its PID happens to still exist.
func TestLoadSurfaceAlwaysExited(t *testing.T) {
	path := statePath(t)

	d := New(path, "")
	addSession(d, &Session{
		ID: "surf", Cwd: "/tmp", running: true, native: true,
		// This process's own PID is guaranteed alive, so a naive liveness
		// re-check would wrongly restore this as running.
		pid: os.Getpid(),
		sfc: &Surface{},
	})
	d.save()

	d2 := New(path, "")
	d2.mu.RLock()
	got := d2.sessions["surf"]
	d2.mu.RUnlock()
	if got == nil {
		t.Fatal("surface session not restored at all")
	}
	if got.running {
		t.Error("restored surface is running; its ConPTY died with the old daemon")
	}
	if !got.wasSurface {
		t.Error("wasSurface not set — surface() would report the wrong error to wmux connect")
	}
	if info := got.Info(); !info.Surface {
		t.Error("SessionInfo.Surface false for a restored surface")
	}
}

// TestLoadCorruptStateDoesNotPanic: state.json is written by a process
// that can be hard-killed (wmux update taskkills a stale daemon), and a
// daemon that panics on startup is unrecoverable without hand-editing a
// file most users don't know exists.
func TestLoadCorruptStateDoesNotPanic(t *testing.T) {
	for _, content := range []string{
		"",
		"{",
		"null",
		`[{"id":"x","ports":"not-an-array"}]`,
		`[{"id":"x"},`,
	} {
		path := statePath(t)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		d := New(path, "") // must not panic
		if d == nil {
			t.Fatalf("New returned nil for state %q", content)
		}
	}
}

// TestSaveIsAtomic verifies the temp-file-and-rename dance leaves no
// stray .tmp behind, and that the file it produces is loadable.
func TestSaveIsAtomic(t *testing.T) {
	path := statePath(t)
	d := New(path, "")
	addSession(d, &Session{ID: "a", running: true})
	d.save()

	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("state.json.tmp left behind after save")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file missing after save: %v", err)
	}
}

// TestMarkExitedClearsPorts pins the invariant markExited's comment states:
// an exited session owns no processes, so it owns no listening ports.
// Reported live once as `wmux list` showing ports for a dead session.
func TestMarkExitedClearsPorts(t *testing.T) {
	d := New("", "")
	sess := &Session{ID: "a", running: true, ports: []int{3000}}
	addSession(d, sess)

	d.markExited(sess)

	info := sess.Info()
	if info.Running {
		t.Error("session still marked running after markExited")
	}
	if len(info.Ports) != 0 {
		t.Errorf("ports = %v, want empty", info.Ports)
	}
}

// TestPruneRemovesOnlyExited: prune is the cleanup for entries kept on
// purpose after exit, and must never touch a live session.
func TestPruneRemovesOnlyExited(t *testing.T) {
	d := New("", "")
	addSession(d, &Session{ID: "live", running: true})
	addSession(d, &Session{ID: "dead", running: false})
	addSession(d, &Session{ID: "alsodead", running: false})

	removed := d.Prune()
	if len(removed) != 2 {
		t.Fatalf("removed %v, want 2 entries", removed)
	}

	d.mu.RLock()
	defer d.mu.RUnlock()
	if _, ok := d.sessions["live"]; !ok {
		t.Error("prune removed a running session")
	}
	if len(d.sessions) != 1 {
		t.Errorf("%d sessions left, want 1", len(d.sessions))
	}
}

// TestRegisterRejectsRunningReplacesExited covers both halves of the
// ID-reuse rule: an ID in use is a conflict, but an ID whose session has
// exited is reusable so the same agent can be restarted under its own name.
func TestRegisterRejectsRunningReplacesExited(t *testing.T) {
	d := New("", "")
	addSession(d, &Session{ID: "agent", running: true, pid: 1})

	if _, err := d.Register("agent", "/tmp", "", 2, true); err == nil {
		t.Fatal("Register accepted an ID that is already running")
	}

	d.mu.RLock()
	d.sessions["agent"].mu.Lock()
	d.sessions["agent"].running = false
	d.sessions["agent"].mu.Unlock()
	d.mu.RUnlock()

	sess, err := d.Register("agent", "/tmp/new", "", 99, true)
	if err != nil {
		t.Fatalf("Register rejected a reusable exited ID: %v", err)
	}
	if info := sess.Info(); info.PID != 99 || info.Cwd != "/tmp/new" {
		t.Errorf("replacement session kept stale fields: %+v", info)
	}
}
