package daemon

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/peterkure3/wmux/internal/proto"
)

// TestSpawnSameIDConcurrentlyLeavesNoOrphan is the regression test for the
// reserveID change.
//
// Before it, Spawn checked for a conflicting session, released d.mu,
// started the process, and only then re-took the lock to install the
// entry. Two concurrent calls for the same ID both passed the check and
// both started a process; the second's map write clobbered the first,
// leaving a live process that nothing referenced — `wmux close` resolves
// IDs through this map, so it was unkillable for the life of the daemon.
//
// The test asserts the invariant that failure implies no process: exactly
// one caller must win, and every caller that got an error must not have
// left a running process behind. It is written against the map rather
// than the OS so it holds on both platforms; run with -race.
func TestSpawnSameIDConcurrentlyLeavesNoOrphan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX sleep command; the logic under test is platform-independent")
	}

	const id = "contended"
	const goroutines = 8

	d := New("", "") // no persistence, no auth — pure in-memory daemon

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		okCount int
		spawned []*Session
	)

	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them all at once to widen the window
			sess, err := d.Spawn(proto.NewSessionRequest{
				ID:      id,
				Cwd:     t.TempDir(),
				Command: "sleep 30",
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				okCount++
				spawned = append(spawned, sess)
			}
		}()
	}
	close(start)
	wg.Wait()

	t.Cleanup(func() {
		// Best effort: don't leave sleeps behind even if the test fails.
		_ = d.Close(id)
	})

	if okCount != 1 {
		t.Fatalf("Spawn succeeded %d times for the same id; exactly one caller may win", okCount)
	}

	// The winner must be the entry in the map — if a loser had also
	// started a process and overwritten it, this is what would diverge.
	d.mu.RLock()
	inMap := d.sessions[id]
	total := len(d.sessions)
	d.mu.RUnlock()

	if total != 1 {
		t.Fatalf("session table holds %d entries; want exactly 1", total)
	}
	if inMap != spawned[0] {
		t.Fatal("the session in the table is not the one Spawn returned: a losing caller " +
			"clobbered the winner, and the winner's process is now unreachable")
	}

	// And the surviving session must actually be closable through the
	// public path — the property the orphan bug destroyed.
	if err := d.Close(id); err != nil {
		t.Fatalf("Close on the surviving session failed: %v", err)
	}
}

// TestSpawnFailureReleasesID checks the other half of reserveID: a
// reservation whose process never starts must not wedge the ID, or a
// single bad command would make that session name unusable until restart.
func TestSpawnFailureReleasesID(t *testing.T) {
	d := New("", "")

	// A command that cannot start at all. buildCommand runs everything
	// through a shell, so an unresolvable *shell* is what fails Start();
	// use a cwd that does not exist, which fails for the same reason on
	// both platforms.
	_, err := d.Spawn(proto.NewSessionRequest{
		ID:      "doomed",
		Cwd:     "/nonexistent-dir-for-wmux-test",
		Command: "true",
	})
	if err == nil {
		t.Skip("spawn unexpectedly succeeded in this environment; nothing to assert")
	}

	d.mu.RLock()
	_, stillThere := d.sessions["doomed"]
	d.mu.RUnlock()
	if stillThere {
		t.Fatal("a failed spawn left its reservation in the session table; " +
			"the ID is now permanently unusable")
	}
}

// TestCloseIsIdempotentUnderConcurrency guards the new job-handle
// bookkeeping: terminate() and release() both close the underlying
// handle, so a session must never hand the same handle to two callers.
func TestCloseIsIdempotentUnderConcurrency(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX sleep command")
	}

	d := New("", "")
	if _, err := d.Spawn(proto.NewSessionRequest{
		ID: "twice", Cwd: t.TempDir(), Command: "sleep 30",
	}); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = d.Close("twice") }()
	}
	wg.Wait()

	// Give waitExit a moment to observe the kill and run its own
	// job-release path concurrently with the Closes above.
	time.Sleep(200 * time.Millisecond)

	d.mu.RLock()
	sess := d.sessions["twice"]
	d.mu.RUnlock()
	if sess == nil {
		t.Fatal("session vanished from the table")
	}
	sess.mu.Lock()
	running := sess.running
	sess.mu.Unlock()
	if running {
		t.Fatal("session still marked running after Close")
	}
}
