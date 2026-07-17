// Package proto defines the wire types shared between wmuxd (daemon) and
// wmux (CLI/UI clients) over the local HTTP API.
package proto

import "time"

// NotifyEvent is raised whenever a session's output stream contains an
// OSC 9 / OSC 99 / OSC 777 notification escape sequence. Body is always
// the human-readable message; Title and Kind are set when the sequence
// carried structure (OSC 99 title=/type= keys, OSC 777's title field).
type NotifyEvent struct {
	SessionID string    `json:"sessionId"`
	Title     string    `json:"title,omitempty"`
	Body      string    `json:"body"`
	Kind      string    `json:"kind,omitempty"` // e.g. "agent_done", "agent_input", "error"
	Time      time.Time `json:"time"`
}

// Display renders the event as a one-line human-readable note.
func (n NotifyEvent) Display() string {
	switch {
	case n.Title != "" && n.Body != "":
		return n.Title + ": " + n.Body
	case n.Title != "":
		return n.Title
	default:
		return n.Body
	}
}

// Event is the envelope streamed over GET /events. Exactly one payload
// field is set, matching Type:
//
//	{"type":"notify","notify":{...NotifyEvent...}}
//	{"type":"sessions","sessions":[...SessionInfo...]}
//
// "sessions" events fire on every session lifecycle transition and on
// branch/port changes, so a sidebar/tray UI can re-render from push alone
// instead of polling GET /sessions.
type Event struct {
	Type     string        `json:"type"`
	Notify   *NotifyEvent  `json:"notify,omitempty"`
	Sessions []SessionInfo `json:"sessions,omitempty"`
}

// Event types for Event.Type.
const (
	EventNotify   = "notify"
	EventSessions = "sessions"
)

// SessionInfo is the public, JSON-serializable view of a session, returned
// by GET /sessions and embedded in notify events for UI consumption.
type SessionInfo struct {
	ID       string `json:"id"`
	Cwd      string `json:"cwd"`
	Branch   string `json:"branch"`
	Ports    []int  `json:"ports"`
	LastNote string `json:"lastNote"`
	Running  bool   `json:"running"`
	PID      int    `json:"pid"`
	Native   bool   `json:"native"`
	Surface  bool   `json:"surface,omitempty"` // daemon-owned ConPTY session; attachable via wmux connect
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

// PruneResult is the response for POST /sessions/prune — the IDs of the
// exited sessions that were removed from daemon state.
type PruneResult struct {
	Removed []string `json:"removed"`
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

// NewSurfaceRequest is the body for POST /surfaces — creates a
// daemon-owned ConPTY session (a "surface"): the daemon holds the
// pseudo-terminal and a server-side VT screen model, so the session has a
// real TTY (unlike `wmux new`'s pipe) but survives its client terminal
// closing (unlike `wmux attach`). Clients view/control it via
// GET /surfaces/attach.
type NewSurfaceRequest struct {
	ID      string `json:"id"`
	Cwd     string `json:"cwd"`
	Command string `json:"command"`
	Distro  string `json:"distro,omitempty"` // WSL distro; ignored with Native
	Native  bool   `json:"native,omitempty"` // run directly on the daemon's OS, no WSL
	Cols    int    `json:"cols,omitempty"`   // initial size; defaults to 120x30
	Rows    int    `json:"rows,omitempty"`
}

// SurfaceFrame is one JSON line in the GET /surfaces/attach stream.
//
//	{"type":"replay","cols":120,"rows":30,"data":"<b64 ANSI repaint>"}   full current screen; sent first and after every resize
//	{"type":"output","data":"<b64 raw pty bytes>"}                       ordered live output
//	{"type":"exit"}                                                      the surface's process ended
//
// A client renders a replay by writing its bytes verbatim (it begins with
// a clear-screen), then applies output frames as they arrive.
type SurfaceFrame struct {
	Type string `json:"type"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Data []byte `json:"data,omitempty"` // JSON-encodes as base64
}

// Frame types for SurfaceFrame.Type.
const (
	FrameReplay = "replay"
	FrameOutput = "output"
	FrameExit   = "exit"
)

// SurfaceInputRequest is the body for POST /surfaces/input — raw bytes
// (keystrokes) written to the surface's PTY.
type SurfaceInputRequest struct {
	ID   string `json:"id"`
	Data []byte `json:"data"`
}

// SurfaceResizeRequest is the body for POST /surfaces/resize. The daemon
// resizes the PTY and its VT screen model, then pushes a fresh replay to
// every attached client.
type SurfaceResizeRequest struct {
	ID   string `json:"id"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}
