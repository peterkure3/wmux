// Package proto defines the wire types shared between wmuxd (daemon) and
// wmux (CLI/UI clients) over the local HTTP API.
package proto

import "time"

// NotifyEvent is raised whenever a session's output stream contains an
// OSC 9 / OSC 99 / OSC 777 notification escape sequence.
type NotifyEvent struct {
	SessionID string    `json:"sessionId"`
	Body      string    `json:"body"`
	Time      time.Time `json:"time"`
}

// SessionInfo is the public, JSON-serializable view of a session, returned
// by GET /sessions and embedded in notify events for UI consumption.
type SessionInfo struct {
	ID       string `json:"id"`
	Cwd      string `json:"cwd"`
	Branch   string `json:"branch"`
	Ports    []int  `json:"ports"`
	LastNote string `json:"lastNote"`
	Running  bool   `json:"running"`
}

// NewSessionRequest is the body for POST /sessions.
type NewSessionRequest struct {
	ID      string `json:"id"`
	Cwd     string `json:"cwd"`
	Command string `json:"command"`
	Distro  string `json:"distro,omitempty"` // WSL distro name; empty = host shell
}

// RegisterSessionRequest is the body for POST /sessions/register — used by
// `wmux attach`, where the daemon tracks metadata but doesn't own the
// process (the caller keeps a real TTY attached to the agent directly). PID
// is the attached process's own process ID, in the daemon's local process
// namespace — it lets `wmux close` terminate a registered (not just
// daemon-spawned) session. Native is true when the attached command runs
// directly on the same OS as wmux attach's own process (set from that
// process's own runtime.GOOS, not user-supplied) — it tells a
// Windows-native daemon whether to poll this session's git branch/ports
// directly or by shelling into WSL.
type RegisterSessionRequest struct {
	ID     string `json:"id"`
	Cwd    string `json:"cwd"`
	Distro string `json:"distro,omitempty"`
	PID    int    `json:"pid,omitempty"`
	Native bool   `json:"native,omitempty"`
}

// PaneSpec is the body for POST /panes/pending — `wmux pane` files one
// right before launching wt.exe, describing the session the new pane
// should run. The pane itself starts a fixed `wmux pane-exec` process
// (via the "wmux" Windows Terminal profile), which claims the spec back
// by session ID and execs the real agent command. This indirection exists
// because a wt.exe pane only honors its profile's closeOnExit setting
// when running the profile's own commandline — passing a commandline on
// the wt.exe command line leaves an inert pane behind on exit (verified
// empirically), which is exactly what the profile flow is here to fix.
type PaneSpec struct {
	ID      string `json:"id"`
	Cwd     string `json:"cwd"`
	Distro  string `json:"distro,omitempty"`
	Command string `json:"command"`
	Native  bool   `json:"native,omitempty"`
}

// ClaimPaneRequest is the body for POST /panes/claim — sent by `wmux
// pane-exec` from inside the freshly opened pane, with the session ID it
// read from its own console title (wt.exe's --title flag sets it).
type ClaimPaneRequest struct {
	ID string `json:"id"`
}

// DeregisterSessionRequest is the body for POST /sessions/deregister.
type DeregisterSessionRequest struct {
	ID string `json:"id"`
}

// CloseSessionRequest is the body for POST /sessions/close — kills the
// session's tracked process (daemon-owned for `wmux new`, or the
// registered PID for `wmux attach`).
type CloseSessionRequest struct {
	ID string `json:"id"`
}
