package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

// TestSaveIsAtomic verifies the temp-file-and-rename dance leaves no stray
// temp file behind, produces a loadable file, and keeps private state modes.
func TestSaveIsAtomic(t *testing.T) {
	path := statePath(t)
	d := New(path, "")
	addSession(d, &Session{ID: "a", running: true})
	d.save()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("state file missing after save: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		// Windows has no Unix permission bits — os.Chmod only toggles the
		// read-only attribute there, and Mode().Perm() always reports 0666.
		// The 0600 contract is a Unix guarantee; on Windows the state file
		// is protected by the user-profile ACL it lives under.
		if runtime.GOOS != "windows" {
			t.Errorf("state mode = %o, want 600", info.Mode().Perm())
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(path) {
			t.Errorf("stray state temp file %q", entry.Name())
		}
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

// TestLoadIdentityUnconfirmedRestoresExited pins the identity rule in
// load(): a live PID is not enough to restore a session as running. The
// snapshot must also record an identity (creation time + executable) that
// the process at that PID still matches — PIDs get reused by the OS. A
// state file with no recorded identity (written by an older daemon) or a
// mismatch restores the session as exited, never killable by number alone.
func TestLoadIdentityUnconfirmedRestoresExited(t *testing.T) {
	for _, name := range []string{"no-identity", "mismatched-identity"} {
		t.Run(name, func(t *testing.T) {
			var snap []persistedSession
			switch name {
			case "no-identity":
				snap = []persistedSession{{
					ID: "agent", Cwd: "/tmp", PID: os.Getpid(), Native: true, Running: true,
				}}
			case "mismatched-identity":
				snap = []persistedSession{{
					ID: "agent", Cwd: "/tmp", PID: os.Getpid(), Native: true, Running: true,
					// Fabricated identity that can never match the live
					// process — the PID-reuse scenario.
					StartTime: 1, Exe: "/nonexistent/never-the-exe",
				}}
			}
			path := statePath(t)
			b, _ := json.Marshal(snap)
			if err := os.WriteFile(path, b, 0o600); err != nil {
				t.Fatal(err)
			}

			d := New(path, "")
			d.mu.RLock()
			got := d.sessions["agent"]
			d.mu.RUnlock()
			if got == nil {
				t.Fatal("session not restored at all")
			}
			if got.running {
				t.Error("session restored running despite unconfirmed process identity")
			}
			if info := got.Info(); info.Running {
				t.Error("SessionInfo.Running true for an identity-unconfirmed session")
			}
		})
	}
}

// TestLoadIdentityConfirmedRestoresRunning is the positive half of the
// identity rule: when the snapshot records this process's own identity
// and that PID is alive and still matching, the session is restored as
// running and controllable — the daemon restart case that is meant to
// survive.
func TestLoadIdentityConfirmedRestoresRunning(t *testing.T) {
	ident, ok := processIdentity(os.Getpid())
	if !ok {
		t.Skip("platform provides no process identity")
	}
	path := statePath(t)
	snap := []persistedSession{{
		ID: "agent", Cwd: "/tmp", PID: os.Getpid(), Native: true, Running: true,
		StartTime: ident.startTime, Exe: ident.exe,
	}}
	b, _ := json.Marshal(snap)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	d := New(path, "")
	d.mu.RLock()
	got := d.sessions["agent"]
	d.mu.RUnlock()
	if got == nil {
		t.Fatal("session not restored at all")
	}
	if !got.running {
		t.Error("identity-confirmed live session not restored running")
	}
	// ...and Close's gate accepts it: confirmed identity is what makes a
	// restored session controllable again.
	if err := got.confirmIdentity(os.Getpid()); err != nil {
		t.Errorf("confirmIdentity rejected a matching process: %v", err)
	}
}

// TestConfirmIdentityRefusesMismatch is the refusal half of Close's gate,
// tested without actually killing anything: a restored session whose PID
// now belongs to a process with a different identity must never pass.
func TestConfirmIdentityRefusesMismatch(t *testing.T) {
	ident, ok := processIdentity(os.Getpid())
	if !ok {
		t.Skip("platform provides no process identity")
	}
	wrong := procIdent{startTime: ident.startTime + 1, exe: ident.exe}
	sess := &Session{ID: "agent", pid: os.Getpid(), running: true, restored: true, ident: wrong}
	if err := sess.confirmIdentity(os.Getpid()); err == nil {
		t.Error("confirmIdentity accepted a mismatched process identity")
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
