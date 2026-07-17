// Package agentprofile turns per-agent hook integrations into data.
//
// Instead of one Go subcommand per AI coding agent (hook-claude,
// hook-codex, ...), `wmux hook run <agent>` reads a declarative profile
// describing how that agent delivers its hook payload (stdin JSON vs a
// final argv argument) and which JSON fields carry the session ID, cwd,
// message, and event name. Several newer CLI agents (Kimi, Kiro) copied
// Claude Code's hook shape outright, so one generic handler plus small
// TOML files covers them all.
//
// Profiles resolve in two layers: defaults bundled into the binary via
// embed.FS (so a fresh install recognizes known agents with zero setup),
// and user files in ~/.wmux/agents/<name>.toml which replace the bundled
// profile of the same name wholesale.
package agentprofile

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed profiles/*.toml
var bundled embed.FS

// Profile describes how one agent delivers hook payloads and where the
// interesting fields live inside them. Field paths are dot-paths into the
// decoded JSON ("properties.sessionID"); every confirmed agent so far uses
// shallow payloads, so nothing more expressive is supported.
type Profile struct {
	Name string `toml:"name"`

	// Wire is how the payload arrives: "stdin-json" (Claude Code, Kimi,
	// Kiro — JSON on stdin) or "argv-json" (Codex — JSON as the final CLI
	// argument).
	Wire string `toml:"wire"`

	// SessionField / CwdField locate the session ID and its fallback in
	// the payload. Session resolution order: --session flag, then
	// SessionField, then CwdField, then SessionFallback.
	SessionField string `toml:"session_field"`
	CwdField     string `toml:"cwd_field"`

	// MessageField locates the human-readable notification text.
	MessageField string `toml:"message_field"`

	// EventField names the payload field holding the event type;
	// EventAllow, when non-empty, limits notifications to those events
	// (anything else is silently ignored, mirroring Codex's
	// agent-turn-complete filter).
	EventField string   `toml:"event_field"`
	EventAllow []string `toml:"event_allow"`

	// DefaultMessage substitutes when the message field is empty or
	// absent. Empty means an empty message suppresses the notification
	// entirely (Claude Code behavior).
	DefaultMessage string `toml:"default_message"`

	// SessionFallback is the last-resort session ID source when the flag
	// and both payload fields come up empty: "getwd" uses the current
	// working directory (Codex behavior); "" leaves the session empty.
	SessionFallback string `toml:"session_fallback"`
}

const (
	WireStdinJSON = "stdin-json"
	WireArgvJSON  = "argv-json"
)

// Validate rejects profiles that the generic handler cannot execute.
func (p *Profile) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("profile is missing name")
	}
	switch p.Wire {
	case WireStdinJSON, WireArgvJSON:
	default:
		return fmt.Errorf("profile %q: unknown wire %q (want %q or %q)", p.Name, p.Wire, WireStdinJSON, WireArgvJSON)
	}
	if p.MessageField == "" {
		return fmt.Errorf("profile %q: message_field is required", p.Name)
	}
	switch p.SessionFallback {
	case "", "getwd":
	default:
		return fmt.Errorf("profile %q: unknown session_fallback %q (want \"getwd\" or empty)", p.Name, p.SessionFallback)
	}
	if len(p.EventAllow) > 0 && p.EventField == "" {
		return fmt.Errorf("profile %q: event_allow requires event_field", p.Name)
	}
	return nil
}

// EventAllowed reports whether a payload's event value passes the
// profile's filter. An empty allow-list admits everything.
func (p *Profile) EventAllowed(event string) bool {
	if len(p.EventAllow) == 0 {
		return true
	}
	for _, e := range p.EventAllow {
		if e == event {
			return true
		}
	}
	return false
}

// userDir is the directory holding user-supplied profile overrides.
func userDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".wmux", "agents")
}

// Load returns the profile for the named agent. A user file at
// ~/.wmux/agents/<name>.toml replaces the bundled profile of the same
// name entirely — whole-file override, no field merging, so what you
// wrote is exactly what runs.
func Load(name string) (*Profile, error) {
	if strings.ContainsAny(name, `/\.`) {
		return nil, fmt.Errorf("invalid agent name %q", name)
	}

	var data []byte
	if dir := userDir(); dir != "" {
		if b, err := os.ReadFile(filepath.Join(dir, name+".toml")); err == nil {
			data = b
		}
	}
	if data == nil {
		b, err := bundled.ReadFile("profiles/" + name + ".toml")
		if err != nil {
			known := strings.Join(List(), ", ")
			return nil, fmt.Errorf("no profile for agent %q (bundled: %s; user profiles live in ~/.wmux/agents/)", name, known)
		}
		data = b
	}

	var p Profile
	if err := toml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("profile %q: %v", name, err)
	}
	if p.Name == "" {
		p.Name = name
	}
	if p.Name != name {
		return nil, fmt.Errorf("profile file %s.toml declares mismatched name %q", name, p.Name)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// List returns the names of all known profiles — bundled plus user
// overrides/additions — sorted, deduplicated.
func List() []string {
	seen := map[string]bool{}
	if entries, err := fs.ReadDir(bundled, "profiles"); err == nil {
		for _, e := range entries {
			seen[strings.TrimSuffix(e.Name(), ".toml")] = true
		}
	}
	if dir := userDir(); dir != "" {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if n, ok := strings.CutSuffix(e.Name(), ".toml"); ok {
					seen[n] = true
				}
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Extract resolves a dot-path ("properties.sessionID") inside a decoded
// JSON payload, returning the string value or "" when the path is
// missing, not a string, or empty.
func Extract(payload map[string]any, path string) string {
	if path == "" {
		return ""
	}
	cur := any(payload)
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur, ok = m[part]
		if !ok {
			return ""
		}
	}
	s, _ := cur.(string)
	return s
}
