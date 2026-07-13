package daemon

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/peterkure/wmux/internal/proto"
)

// Matches OSC 9 (basic notify), OSC 99 (extended notify, iTerm2-style),
// and OSC 777 (rxvt-style notify). All three are terminated by BEL (\x07)
// or ST (\x1b\\).
var oscNotifyRe = regexp.MustCompile(`\x1b\](?:9|99|777);([^\x07\x1b]*)(?:\x07|\x1b\\)`)

// Session represents one running agent session (a shell running Claude
// Code, Codex, etc.) that the daemon is watching.
type Session struct {
	ID      string
	Cwd     string
	Distro  string
	Command string

	mu       sync.Mutex
	cmd      *exec.Cmd
	pid      int
	native   bool
	branch   string
	ports    []int
	lastNote string
	running  bool
}

func (s *Session) Info() proto.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return proto.SessionInfo{
		ID: s.ID, Cwd: s.Cwd, Branch: s.branch,
		Ports: s.ports, LastNote: s.lastNote, Running: s.running,
	}
}

// Daemon owns all active sessions and fans out notification events to
// subscribers (the CLI's `wmux watch`, or a tray UI's SSE client).
type Daemon struct {
	mu       sync.RWMutex
	sessions map[string]*Session

	subMu sync.Mutex
	subs  map[chan proto.NotifyEvent]struct{}

	// panes holds pending pane specs (see panes.go) — the handshake between
	// `wmux pane` and the `wmux pane-exec` process inside the new wt.exe pane.
	panes paneSpecs

	// statePath is where sessions are persisted between restarts; empty
	// disables persistence entirely.
	statePath string
}

// New creates a daemon and restores any sessions found at statePath from a
// previous run (see load). Pass an empty statePath to disable persistence.
func New(statePath string) *Daemon {
	d := &Daemon{
		sessions:  make(map[string]*Session),
		subs:      make(map[chan proto.NotifyEvent]struct{}),
		statePath: statePath,
	}
	d.load()
	return d
}

func (d *Daemon) Subscribe() chan proto.NotifyEvent {
	ch := make(chan proto.NotifyEvent, 32)
	d.subMu.Lock()
	d.subs[ch] = struct{}{}
	d.subMu.Unlock()
	return ch
}

func (d *Daemon) Unsubscribe(ch chan proto.NotifyEvent) {
	d.subMu.Lock()
	delete(d.subs, ch)
	d.subMu.Unlock()
	close(ch)
}

func (d *Daemon) publish(evt proto.NotifyEvent) {
	d.subMu.Lock()
	defer d.subMu.Unlock()
	for ch := range d.subs {
		select {
		case ch <- evt:
		default: // slow subscriber; drop rather than block the watcher
		}
	}
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
		args := append(wslArgs(distro), "--cd", cwd, "--", "bash", "-lc", cmdline)
		return hiddenCommand("wsl.exe", args...)
	}
	cmd := hiddenCommand("bash", "-lc", cmdline)
	cmd.Dir = cwd
	return cmd
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

	go d.pollMetadata(sess)
	d.save()

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

	sess.mu.Lock()
	pid := sess.pid
	running := sess.running
	sess.mu.Unlock()

	if !running {
		return fmt.Errorf("session %q is not running", id)
	}
	if pid == 0 {
		return fmt.Errorf("session %q has no tracked process to close", id)
	}

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

// Spawn starts a new agent session and begins watching its combined
// stdout/stderr stream for notification escape sequences.
func (d *Daemon) Spawn(req proto.NewSessionRequest) (*Session, error) {
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

	cmd := buildCommand(req.Cwd, req.Distro, req.Command)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	sess := &Session{
		ID: req.ID, Cwd: req.Cwd, Distro: req.Distro, Command: req.Command,
		cmd: cmd, pid: cmd.Process.Pid, running: true,
	}

	d.mu.Lock()
	d.sessions[req.ID] = sess
	d.mu.Unlock()

	go d.watchOutput(sess, stdout)
	go d.pollMetadata(sess)
	go d.waitExit(sess)
	d.save()

	return sess, nil
}

func (d *Daemon) waitExit(sess *Session) {
	err := sess.cmd.Wait()
	d.markExited(sess)
	if err != nil {
		log.Printf("session %s exited: %v", sess.ID, err)
	} else {
		log.Printf("session %s exited cleanly", sess.ID)
	}
}

// watchOutput reads raw bytes as they arrive (not line-buffered) so a
// notification is detected the instant its terminating BEL/ST byte shows
// up, even if the agent never emits a trailing newline after it. Line
// buffering here would delay detection until the next newline — which,
// in the worst case, is whenever the session happens to exit.
func (d *Daemon) watchOutput(sess *Session, r io.Reader) {
	const maxPending = 16 * 1024
	buf := make([]byte, 4096)
	var pending []byte

	for {
		n, err := r.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)

			for {
				loc := oscNotifyRe.FindSubmatchIndex(pending)
				if loc == nil {
					break
				}
				body := string(pending[loc[2]:loc[3]])

				sess.mu.Lock()
				sess.lastNote = body
				sess.mu.Unlock()

				evt := proto.NotifyEvent{SessionID: sess.ID, Body: body, Time: time.Now()}
				d.publish(evt)
				log.Printf("[notify] session=%s body=%q", sess.ID, body)

				pending = pending[loc[1]:] // drop everything through the matched sequence
			}

			// Cap growth for chatty agents that never emit a notify sequence;
			// keep only the tail, since a partial match can only ever start
			// within the last few bytes read.
			if len(pending) > maxPending {
				pending = pending[len(pending)-maxPending:]
			}
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("session %s: read error: %v", sess.ID, err)
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
		daemonOwned := sess.cmd != nil
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
		if !daemonOwned && pidVisible(native, sess.Command) && pid != 0 && !processAlive(pid) {
			d.markExited(sess)
			log.Printf("session %s: tracked process %d is gone; marking exited", sess.ID, pid)
			return
		}

		branch := gitBranch(sess.Cwd, sess.Distro, native)
		ports := listeningPorts(sess.Distro, pid, native)

		sess.mu.Lock()
		sess.branch = branch
		sess.ports = ports
		sess.mu.Unlock()
		d.save()
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
	sess.mu.Unlock()
	d.save()
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
		args := append(wslArgs(distro), "--", "git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD")
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
		return listeningPortsForTree(processTree(pid))
	}

	args := append(wslArgs(distro), "--", "ss", "-ltn")
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
	return ports
}
