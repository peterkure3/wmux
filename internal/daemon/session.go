package daemon

import (
	"fmt"
	"io"
	"log"
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
}

func New() *Daemon {
	return &Daemon{
		sessions: make(map[string]*Session),
		subs:     make(map[chan proto.NotifyEvent]struct{}),
	}
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

// buildCommand constructs the process to run for a session. On Windows,
// agent sessions run inside a WSL2 distro so the fleet-parity story with
// Linux boxes holds; on any other OS (used for local dev/testing of this
// daemon itself) it runs the command directly in a login shell.
func buildCommand(cwd, distro, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		d := distro
		if d == "" {
			d = "Ubuntu"
		}
		return exec.Command("wsl.exe", "-d", d, "--cd", cwd, "--", "bash", "-lc", command)
	}
	cmd := exec.Command("bash", "-lc", command)
	cmd.Dir = cwd
	return cmd
}

// Register creates a session entry for "attach mode": the daemon doesn't
// own or pipe the process (see Spawn for that) — the caller (wmux attach)
// runs the real agent command with a full TTY passthrough itself, and just
// asks the daemon to track metadata (branch/ports) and accept notify events
// under this ID. Used when the actual interactive terminal needs to stay
// attached to a real console/pty rather than a daemon-owned pipe.
func (d *Daemon) Register(id, cwd, distro string) (*Session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing, exists := d.sessions[id]; exists {
		existing.mu.Lock()
		stillRunning := existing.running
		existing.mu.Unlock()
		if stillRunning {
			return nil, fmt.Errorf("session %q is already running", id)
		}
		// existing entry has exited — fall through and replace it, so the
		// same session ID can be reused across restarts of the same agent.
	}

	sess := &Session{ID: id, Cwd: cwd, Distro: distro, running: true}
	d.sessions[id] = sess

	go d.pollMetadata(sess)

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
	sess.mu.Lock()
	sess.running = false
	sess.mu.Unlock()
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
		cmd: cmd, running: true,
	}

	d.mu.Lock()
	d.sessions[req.ID] = sess
	d.mu.Unlock()

	go d.watchOutput(sess, stdout)
	go d.pollMetadata(sess)
	go d.waitExit(sess)

	return sess, nil
}

func (d *Daemon) waitExit(sess *Session) {
	err := sess.cmd.Wait()
	sess.mu.Lock()
	sess.running = false
	sess.mu.Unlock()
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
		sess.mu.Unlock()
		if !running {
			return
		}

		branch := gitBranch(sess.Cwd, sess.Distro)
		ports := listeningPorts(sess.Cwd, sess.Distro)

		sess.mu.Lock()
		sess.branch = branch
		sess.ports = ports
		sess.mu.Unlock()
	}
}

func gitBranch(cwd, distro string) string {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		d := distro
		if d == "" {
			d = "Ubuntu"
		}
		cmd = exec.Command("wsl.exe", "-d", d, "--", "git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD")
	} else {
		cmd = exec.Command("git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD")
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// listeningPorts shells out to `ss -ltn` and returns the local ports the
// session's working directory's project appears to be listening on. This
// is intentionally coarse — it lists all listening ports system-wide
// rather than scoping to the session's process tree, which is a
// reasonable v1 given most dev setups run one project at a time.
func listeningPorts(cwd, distro string) []int {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		d := distro
		if d == "" {
			d = "Ubuntu"
		}
		cmd = exec.Command("wsl.exe", "-d", d, "--", "ss", "-ltn")
	} else {
		cmd = exec.Command("ss", "-ltn")
	}
	out, err := cmd.Output()
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
