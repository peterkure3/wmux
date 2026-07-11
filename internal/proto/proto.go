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
