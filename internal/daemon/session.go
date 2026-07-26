package daemon

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/peterkure3/wmux/internal/proto"
)

// Session represents one running agent session (a shell running Claude
// Code, Codex, etc.) that the daemon is watching.
//
// Field ownership is split deliberately, because this struct is read
// concurrently by the HTTP handlers, the metadata poller, the output
// watcher, and the reaper:
//
//   - The exported fields are set once at construction and never written
//     again, so they are safe to read without holding mu.
//   - Everything below mu is guarded by mu. That includes cmd and sfc,
//     which are now assigned *after* the session is published into
//     d.sessions (see reserveID) rather than before — so reading them
//     unlocked is a real race, not merely an untidy one. Use proc() and
//     surfaceRef() instead of touching them directly.
type Session struct {
	// Immutable after construction — safe to read without mu.
	ID      string
	Cwd     string
	Distro  string
	Command string

	// Guarded by mu.
	mu         sync.Mutex
	cmd        *exec.Cmd
	sfc        *Surface  // non-nil for surface sessions (daemon-owned ConPTY; see surface.go)
	job        jobHandle // process-tree kill handle; zero value off Windows (see jobobject_*.go)
	wasSurface bool      // restored surface whose ConPTY died with the previous daemon run
	pid        int
	native     bool
	branch     string
	ports      []int
	lastNote   string
	running    bool
	deadStreak int // consecutive failed WSL liveness probes; see pollMetadata
}

// proc returns the exec.Cmd a Spawn-mode session owns, or nil for a
// registered, restored, or surface session.
func (s *Session) proc() *exec.Cmd {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cmd
}

// surfaceRef returns the session's Surface, or nil if it isn't one.
func (s *Session) surfaceRef() *Surface {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sfc
}

func (s *Session) Info() proto.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return proto.SessionInfo{
		ID: s.ID, Cwd: s.Cwd, Branch: s.branch,
		Ports: s.ports, LastNote: s.lastNote, Running: s.running,
		PID: s.pid, Native: s.native, Surface: s.sfc != nil || s.wasSurface,
	}
}

// Daemon owns all active sessions and fans out notification events to
// subscribers (the CLI's `wmux watch`, or a tray UI's SSE client).
type Daemon struct {
	mu       sync.RWMutex
	sessions map[string]*Session

	subMu sync.Mutex
	// subs maps each subscriber channel to its dropped-notify counter —
	// how many notifications were evicted/lost for that subscriber since
	// the last notify it actually received (see publish).
	subs map[chan proto.Event]*int

	// statePath is where sessions are persisted between restarts; empty
	// disables persistence entirely.
	statePath string

	// token authenticates every request to the HTTP API except /healthz
	// (see auth.go). Empty disables authentication entirely — for tests
	// only; the daemon binary always provisions one.
	token string

	startedAt    time.Time
	panics       *ring[proto.PanicEntry]
	recentEvents *ring[proto.Event]
}

// New creates a daemon and restores any sessions found at statePath from a
// previous run (see load). Pass an empty statePath to disable persistence,
// and an empty token to disable API authentication (tests only — the API
// can execute arbitrary commands, so the daemon binary always supplies one).
func New(statePath, token string) *Daemon {
	d := &Daemon{
		sessions:     make(map[string]*Session),
		subs:         make(map[chan proto.Event]*int),
		statePath:    statePath,
		token:        token,
		startedAt:    time.Now(),
		panics:       newRing[proto.PanicEntry](50),
		recentEvents: newRing[proto.Event](200),
	}
	d.load()
	return d
}

func (d *Daemon) Subscribe() chan proto.Event {
	ch := make(chan proto.Event, 256)
	d.subMu.Lock()
	d.subs[ch] = new(int)
	d.subMu.Unlock()
	return ch
}

func (d *Daemon) Unsubscribe(ch chan proto.Event) {
	d.subMu.Lock()
	delete(d.subs, ch)
	d.subMu.Unlock()
	close(ch)
}

// stampDropped attaches a subscriber's missed-notify count to an outgoing
// notify event. The event is copied so one subscriber's count never leaks
// into another subscriber's copy.
func stampDropped(evt proto.Event, dropped int) proto.Event {
	if dropped == 0 || evt.Type != proto.EventNotify || evt.Notify == nil {
		return evt
	}
	n := *evt.Notify
	n.Dropped = dropped
	evt.Notify = &n
	return evt
}

// publish fans an event out to every subscriber without ever blocking the
// session reader. A subscriber whose buffer is full loses its OLDEST
// queued event (a notification consumer wants recency), and the loss is
// accounted for: the next notify it receives carries Dropped = how many
// notifies were evicted since the last one it saw.
func (d *Daemon) publish(evt proto.Event) {
	d.recentEvents.add(evt)
	isNotify := evt.Type == proto.EventNotify && evt.Notify != nil
	d.subMu.Lock()
	defer d.subMu.Unlock()
	for ch, dropped := range d.subs {
		select {
		case ch <- stampDropped(evt, *dropped):
			if isNotify {
				*dropped = 0
			}
			continue
		default:
		}

		// Full: evict the oldest queued event to make room. An evicted
		// notify — including any Dropped count it was itself carrying,
		// which now never reaches the subscriber — adds to the counter.
		select {
		case old := <-ch:
			if old.Type == proto.EventNotify && old.Notify != nil {
				*dropped += 1 + old.Notify.Dropped
			}
		default:
			// subscriber drained it between the two selects
		}

		select {
		case ch <- stampDropped(evt, *dropped):
			if isNotify {
				*dropped = 0
			}
		default:
			if isNotify {
				*dropped++ // still full; this event is the one lost
			}
		}
	}
}

// publishNotify pushes a notification to every /events subscriber.
func (d *Daemon) publishNotify(evt proto.NotifyEvent) {
	d.publish(proto.Event{Type: proto.EventNotify, Notify: &evt})
}

// publishSessions pushes the full session list to every /events subscriber.
// Called on every lifecycle transition and metadata change so a sidebar/tray
// UI can re-render from push alone instead of re-polling GET /sessions.
func (d *Daemon) publishSessions() {
	d.publish(proto.Event{Type: proto.EventSessions, Sessions: d.List()})
}

func (d *Daemon) List() []proto.SessionInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]proto.SessionInfo, 0, len(d.sessions))
	for _, s := range d.sessions {
		out = append(out, s.Info())
	}
	return out
}

// hiddenCommand builds an exec.Cmd with the platform's console-window
// hiding applied — daemon shell-outs must use this instead of
// exec.Command directly, or each one flashes a visible console window
// whenever wmuxd runs detached without a console of its own (see
// hideConsole).
func hiddenCommand(name string, arg ...string) *exec.Cmd {
	cmd := exec.Command(name, arg...)
	hideConsole(cmd)
	return cmd
}

// wslArgs builds the leading wsl.exe argv for a given distro, omitting
// -d entirely when distro is empty so wsl.exe falls back to whatever the
// user actually configured as their system default distro (`wsl.exe
// --status`) instead of us guessing a name — "Ubuntu" is a common default
// but by no means universal, and guessing wrong makes every session exit
// instantly with no useful error.
func wslArgs(distro string) []string {
	if distro == "" {
		return nil
	}
	return []string{"-d", distro}
}

// buildCommand constructs the process to run for a session. On Windows,
// agent sessions run inside a WSL2 distro so the fleet-parity story with
// Linux boxes holds; on any other OS (used for local dev/testing of this
// daemon itself) it runs the command directly in a login shell.
func buildCommand(cwd, distro, cmdline string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		// --exec, not --: without it wsl.exe routes the command tail through
		// the distro's default shell, which expands $(), $vars, and quotes
		// ONCE before bash -lc ever sees the script (verified empirically —
		// `x=$(seq 1 3)` arrived as the multiline `x=1\n2\n3`). --exec
		// passes argv straight to bash.
		args := append(wslArgs(distro), "--cd", cwd, "--exec", "bash", "-lc", cmdline)
		return hiddenCommand("wsl.exe", args...)
	}
	cmd := hiddenCommand("bash", "-lc", cmdline)
	cmd.Dir = cwd
	return cmd
}

// reserveID atomically claims a session ID and installs sess as its entry,
// before the caller has started any process.
//
// Spawn and SpawnSurface used to check for a conflicting session, release
// d.mu, start the process, and only then re-take the lock to install the
// entry. Two concurrent requests for the same ID both passed the check and
// both started a process; the second's map write then clobbered the first,
// leaving a live process that nothing referenced — unreachable by `wmux
// close` (which resolves the ID through this map) and, for a surface,
// holding a leaked ConPTY and pty reader goroutine besides. Reserving the
// ID up front makes the loser of that race fail at the check instead of
// after it has already spawned. Register never had the bug: it has always
// held d.mu across both the check and the insert.
//
// sess must already have running=true; the caller fills in cmd/sfc/pid
// under sess.mu once its process is actually started, and must call
// releaseID if starting fails.
func (d *Daemon) reserveID(sess *Session) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing, exists := d.sessions[sess.ID]; exists {
		existing.mu.Lock()
		stillRunning := existing.running
		existing.mu.Unlock()
		if stillRunning {
			return fmt.Errorf("session %q is already running", sess.ID)
		}
		// The existing entry has exited — fall through and replace it, so
		// the same session ID can be reused across restarts of the agent.
	}
	d.sessions[sess.ID] = sess
	return nil
}

// releaseID drops a reservation whose process failed to start.
//
// The pointer comparison is defensive: no other caller can currently
// replace a reserved entry (reserveID rejects a running one, and a
// reservation is running until released), but deleting by ID alone would
// silently evict someone else's session the moment that stops being true.
func (d *Daemon) releaseID(sess *Session) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cur, ok := d.sessions[sess.ID]; ok && cur == sess {
		delete(d.sessions, sess.ID)
	}
}

// Register creates a session entry for "attach mode": the daemon doesn't
// own or pipe the process (see Spawn for that) — the caller (wmux attach)
// runs the real agent command with a full TTY passthrough itself, and just
// asks the daemon to track metadata (branch/ports) and accept notify events
// under this ID. Used when the actual interactive terminal needs to stay
// attached to a real console/pty rather than a daemon-owned pipe.
func (d *Daemon) Register(id, cwd, distro string, pid int, native bool) (*Session, error) {
	d.mu.Lock()
	if existing, exists := d.sessions[id]; exists {
		existing.mu.Lock()
		stillRunning := existing.running
		existing.mu.Unlock()
		if stillRunning {
			d.mu.Unlock()
			return nil, fmt.Errorf("session %q is already running", id)
		}
		// existing entry has exited — fall through and replace it, so the
		// same session ID can be reused across restarts of the same agent.
	}

	sess := &Session{ID: id, Cwd: cwd, Distro: distro, pid: pid, native: native, running: true}
	d.sessions[id] = sess
	d.mu.Unlock()

	d.safeGo("pollMetadata:"+sess.ID, func() { d.pollMetadata(sess) })
	d.save()
	d.publishSessions()

	return sess, nil
}

// Deregister marks a registered session as no longer running. It doesn't
// remove the entry — `wmux list` still shows its last known state, same as
// a Spawn-owned session after its process exits.
func (d *Daemon) Deregister(id string) error {
	d.mu.RLock()
	sess, ok := d.sessions[id]
	d.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	d.markExited(sess)
	return nil
}

// Prune removes every exited session from daemon state — entries are
// otherwise kept forever so `wmux list` can show last known state, which
// accumulates test runs and one-offs until the sidebar/list is mostly
// noise. Running sessions are never touched. Returns the removed IDs.
func (d *Daemon) Prune() []string {
	d.mu.Lock()
	removed := []string{}
	for id, sess := range d.sessions {
		sess.mu.Lock()
		running := sess.running
		sess.mu.Unlock()
		if !running {
			delete(d.sessions, id)
			removed = append(removed, id)
		}
	}
	d.mu.Unlock()

	if len(removed) > 0 {
		d.save()
		d.publishSessions()
	}
	return removed
}

// Close terminates a session's underlying process — the daemon-owned
// process for a `wmux new` session, or the registered PID for a `wmux
// attach`/`wmux pane` session. This is what `wmux close` calls: it ends
// the agent, and for a `wmux pane` session the pane's process chain
// unwinds with it, at which point the "wmux" WT profile's
// closeOnExit:"always" removes the pane from the layout entirely.
func (d *Daemon) Close(id string) error {
	d.mu.RLock()
	sess, ok := d.sessions[id]
	d.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}

	// Validate first, then take ownership. Taking the job handle out before
	// the checks would strand it on every error return — including the
	// common "already closed" case, where the handle still belongs to
	// whoever is about to release it.
	sess.mu.Lock()
	pid := sess.pid
	running := sess.running
	native := sess.native
	sess.mu.Unlock()

	if !running {
		return fmt.Errorf("session %q is not running", id)
	}
	if pid == 0 {
		return fmt.Errorf("session %q has no tracked process to close", id)
	}

	// Take the job handle *out* of the session rather than copying it: both
	// terminate() and release() close the underlying kernel handle, so two
	// concurrent Closes — or a Close racing waitExit's release — must not
	// each end up holding it. Whoever removes it owns it; everyone else
	// gets the zero value and falls through to the single-PID path below,
	// which is harmless against an already-dead process.
	sess.mu.Lock()
	job := sess.job
	sess.job = jobHandle{}
	sess.mu.Unlock()

	// A WSL-registered session's PID lives inside the distro's own PID
	// namespace, where it means nothing to this side's process table.
	// os.FindProcess always succeeds on Windows and Kill is a raw
	// OpenProcess(PROCESS_TERMINATE), so killing such a PID locally
	// terminates whatever unrelated Windows process happens to hold that
	// number. pollMetadata already respects this boundary via pidVisible;
	// so must this. Kill it inside the distro instead.
	if !pidVisible(native, sess.Command) {
		// A WSL-registered session never has a job (we did not spawn it),
		// but release defensively so a future code path that gives it one
		// cannot leak the handle here.
		job.release()
		if err := killInWSL(sess.Distro, pid); err != nil {
			return err
		}
		d.markExited(sess)
		return nil
	}

	// Job object path: takes the agent AND every descendant down together.
	// Without it, killing the root of `cmd.exe /c claude` leaves the agent
	// itself running as an orphan — the launcher is what dies.
	if job.valid() {
		if err := job.terminate(); err != nil {
			return fmt.Errorf("could not kill process tree for session %q: %w", id, err)
		}
		d.markExited(sess)
		return nil
	}

	// No job (pre-existing session restored from state, or job creation
	// failed at spawn): fall back to killing the root PID alone.
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("could not find process %d: %w", pid, err)
	}
	if err := proc.Kill(); err != nil {
		return fmt.Errorf("could not kill process %d: %w", pid, err)
	}

	// waitExit/the caller's own exit-path deregister will normally flip
	// `running` to false once the kill is observed, but set it explicitly
	// too so `wmux list` reflects it immediately rather than racing.
	d.markExited(sess)

	return nil
}

// wslTermGrace is how long a session inside a WSL distro gets to handle
// SIGTERM before it is killed outright. Agents write conversation/session
// state on shutdown, so the graceful attempt is worth the wait; the
// escalation is what stops a wedged process from being reported closed
// while it is still running.
const wslTermGrace = 5 * time.Second

// killInWSL ends a process inside a WSL distro with a real signal ladder:
// SIGTERM, poll for it to actually go, then SIGKILL.
//
// The previous behavior sent SIGTERM once and immediately reported the
// session closed, so an agent that ignored or was slow to handle the
// signal stayed alive while `wmux list` said otherwise.
//
// The negative PID targets the process *group*, so a shell that spawned
// children takes them with it; a session whose leader is not a group
// leader falls back to signalling the PID alone.
func killInWSL(distro string, pid int) error {
	term := func(sig string, target string) error {
		args := append(wslArgs(distro), "--exec", "kill", sig, target)
		out, err := hiddenCommand("wsl.exe", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("kill %s %s in distro %q: %w: %s",
				sig, target, distro, err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	group := "-" + strconv.Itoa(pid)
	self := strconv.Itoa(pid)

	// Group first; if this PID does not lead a group, fall back to it alone.
	if err := term("-TERM", group); err != nil {
		if err := term("-TERM", self); err != nil {
			return err
		}
	}

	deadline := time.Now().Add(wslTermGrace)
	for time.Now().Before(deadline) {
		if !processAliveWSL(distro, pid) {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}

	// Still there: stop asking.
	if err := term("-KILL", group); err != nil {
		return term("-KILL", self)
	}
	return nil
}

// Spawn starts a new agent session and begins watching its combined
// stdout/stderr stream for notification escape sequences.
func (d *Daemon) Spawn(req proto.NewSessionRequest) (*Session, error) {
	sess := &Session{
		ID: req.ID, Cwd: req.Cwd, Distro: req.Distro, Command: req.Command,
		running: true,
	}
	if err := d.reserveID(sess); err != nil {
		return nil, err
	}

	cmd := buildCommand(req.Cwd, req.Distro, req.Command)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		d.releaseID(sess)
		return nil, err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		d.releaseID(sess)
		return nil, err
	}

	// Confine the child's whole process tree so Close can take it down as
	// a unit. killOnClose is deliberately false here: a Spawn-mode session
	// is expected to outlive a daemon restart (load() re-checks its PID and
	// resumes tracking), which JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE would
	// break by killing it the moment wmuxd exits.
	job, jerr := newJob(cmd.Process, false)
	if jerr != nil {
		// Not fatal — the session runs fine, Close just falls back to
		// killing the root PID alone, which is the old behavior.
		slog.Warn("could not confine session to a job object; close will not kill descendants",
			"id", sess.ID, "err", jerr)
	}

	sess.mu.Lock()
	sess.cmd = cmd
	sess.pid = cmd.Process.Pid
	sess.job = job
	sess.mu.Unlock()

	d.safeGo("watchOutput:"+sess.ID, func() { d.watchOutput(sess, stdout) })
	d.safeGo("pollMetadata:"+sess.ID, func() { d.pollMetadata(sess) })
	d.safeGo("waitExit:"+sess.ID, func() { d.waitExit(sess) })
	d.save()
	d.publishSessions()

	return sess, nil
}

func (d *Daemon) waitExit(sess *Session) {
	cmd := sess.proc()
	if cmd == nil {
		return // not a Spawn-mode session; nothing of ours to reap
	}
	err := cmd.Wait()

	// Drop the job handle now the tree is gone, rather than holding a
	// kernel handle per dead session for the life of the daemon.
	sess.mu.Lock()
	job := sess.job
	sess.job = jobHandle{}
	sess.mu.Unlock()
	job.release()

	d.markExited(sess)
	if err != nil {
		slog.Info("session exited", "id", sess.ID, "err", err)
	} else {
		slog.Info("session exited cleanly", "id", sess.ID)
	}
}

// watchOutput reads raw bytes as they arrive (not line-buffered) so a
// notification is detected the instant its terminating BEL/ST byte shows
// up, even if the agent never emits a trailing newline after it. Line
// buffering here would delay detection until the next newline — which,
// in the worst case, is whenever the session happens to exit.
func (d *Daemon) watchOutput(sess *Session, r io.Reader) {
	buf := make([]byte, 4096)
	var pending []byte

	for {
		n, err := r.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			pending = trimPending(d.scanNotes(sess, pending))
		}
		if err != nil {
			if err != io.EOF {
				slog.Warn("session read error", "id", sess.ID, "err", err)
			}
			return
		}
	}
}

func (d *Daemon) pollMetadata(sess *Session) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		sess.mu.Lock()
		running := sess.running
		pid := sess.pid
		native := sess.native
		// Surface sessions are daemon-owned too: reapSurface's cmd.Wait()
		// reaps them, so the WSL liveness probe below must not apply.
		daemonOwned := sess.cmd != nil || sess.sfc != nil // guarded: mu held
		sess.mu.Unlock()
		if !running {
			return
		}

		// A registered session's deregister never arrives if its whole
		// console was torn down at once (terminal window closed, process
		// tree hard-killed) — without this re-check the session stays
		// "running", and this poll keeps shelling out every 3 seconds
		// forever. Daemon-owned sessions are reaped by waitExit instead;
		// restored ones (cmd == nil after a restart) rely on this check.
		if !daemonOwned && pid != 0 {
			var alive bool
			if pidVisible(native, sess.Command) {
				alive = processAlive(pid)
			} else {
				// WSL-registered session (plain `wmux attach`/`wmux pane`):
				// this PID lives inside the distro's own PID namespace,
				// invisible to processAlive (see pidVisible). Shelling into
				// the distro is the only way to check it at all, but unlike
				// the direct OS probe above, a wsl.exe failure here can mean
				// the distro was transiently unresponsive rather than the
				// process actually gone — so this requires two consecutive
				// misses before it's trusted, instead of acting on one.
				if processAliveWSL(sess.Distro, pid) {
					sess.mu.Lock()
					sess.deadStreak = 0
					sess.mu.Unlock()
					alive = true
				} else {
					sess.mu.Lock()
					sess.deadStreak++
					streak := sess.deadStreak
					sess.mu.Unlock()
					alive = streak < 2
				}
			}
			if !alive {
				d.markExited(sess)
				slog.Info("tracked process gone; marking exited", "id", sess.ID, "pid", pid)
				return
			}
		}

		branch := gitBranch(sess.Cwd, sess.Distro, native)
		ports := listeningPorts(sess.Distro, pid, native)

		sess.mu.Lock()
		// The shell-outs above take real time; if the session was closed
		// meanwhile, writing their results would resurrect metadata on an
		// exited session — markExited just cleared ports for good reason.
		// (Seen live: `wmux list` showing a dead session with ports.)
		if !sess.running {
			sess.mu.Unlock()
			return
		}
		changed := branch != sess.branch || !slices.Equal(ports, sess.ports)
		sess.branch = branch
		sess.ports = ports
		sess.mu.Unlock()

		// Only persist and push on an actual diff — this ticks every 3s per
		// session, and subscribers (the sidebar) re-render on every push.
		if changed {
			d.save()
			d.publishSessions()
		}
	}
}

// pidVisible reports whether a session's tracked PID lives in the
// daemon's own PID namespace, i.e. whether processAlive can say anything
// meaningful about it. True for native sessions, everything on a
// non-Windows (WSL-resident) daemon, and daemon-spawned sessions (whose
// PID is the Windows-side wsl.exe frontend, and which are the only kind
// with a non-empty Command). False for a WSL-registered attach/pane
// session on a Windows daemon: its PID comes from inside the distro,
// where tasklist/OpenProcess can't see — the same namespace boundary
// listeningPorts already respects via runsDirectly.
func pidVisible(native bool, command string) bool {
	return runsDirectly(native) || command != ""
}

// markExited flips a session to not-running and persists the change —
// the shared tail of every exit path (deregister, close, reap, liveness).
func (d *Daemon) markExited(sess *Session) {
	sess.mu.Lock()
	sess.running = false
	// An exited session owns no processes, so it owns no listening ports —
	// keeping the last polled set around just misleads (`wmux list` showing
	// ports for a dead session).
	sess.ports = nil
	sess.mu.Unlock()
	d.save()
	d.publishSessions()
}

// runsDirectly reports whether a session's own process (and thus its git
// checkout and any ports it opens) lives in the daemon's own process/OS
// namespace, as opposed to inside a WSL distro the daemon has to shell
// into. True for: any session on a non-Windows (i.e. WSL-resident) daemon,
// and native Windows sessions on a Windows-native daemon. False for:
// WSL-targeted sessions on a Windows-native daemon (the default `wmux
// new`/plain `wmux pane` case).
func runsDirectly(native bool) bool {
	return runtime.GOOS != "windows" || native
}

func gitBranch(cwd, distro string, native bool) string {
	var cmd *exec.Cmd
	if runsDirectly(native) {
		cmd = hiddenCommand("git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD")
	} else {
		args := append(wslArgs(distro), "--exec", "git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD")
		cmd = hiddenCommand("wsl.exe", args...)
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// listeningPorts returns the local listening ports opened by a session's
// own process tree.
//
// When the session's process lives in the daemon's own namespace
// (runsDirectly), this is exact: it walks the real process tree rooted at
// pid and matches it against the OS's own port->owning-PID data (see
// portscope.go).
//
// When it doesn't — a WSL-targeted session on a Windows-native daemon,
// which is what plain `wmux new`/`wmux pane` (no --native) always are —
// pid is the Windows-side wsl.exe frontend's PID, which has no
// correlation to PIDs inside the WSL distro's own /proc namespace. There
// is no reliable way to scope to just this session's processes in that
// case, so this intentionally falls back to every listening port inside
// the distro (the original, pre-scoping behavior) rather than silently
// showing nothing.
func listeningPorts(distro string, pid int, native bool) []int {
	if runsDirectly(native) {
		if pid == 0 {
			return nil
		}
		return normalizePorts(listeningPortsForTree(processTree(pid)))
	}

	args := append(wslArgs(distro), "--exec", "ss", "-ltn")
	out, err := hiddenCommand("wsl.exe", args...).Output()
	if err != nil {
		return nil
	}

	var ports []int
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		addr := fields[3] // Local Address:Port column
		idx := strings.LastIndex(addr, ":")
		if idx == -1 {
			continue
		}
		if p, err := strconv.Atoi(addr[idx+1:]); err == nil {
			ports = append(ports, p)
		}
	}
	return normalizePorts(ports)
}
