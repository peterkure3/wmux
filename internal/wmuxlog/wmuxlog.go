// Package wmuxlog is wmuxd's structured logger: log/slog under the hood,
// JSON to ~/.wmux/wmuxd.log (with simple size-based rotation) plus a text
// mirror to stderr when one is attached to a real console — the manual
// `go run ./cmd/wmuxd` foreground-start path still wants to see log lines
// without tailing the file. Level comes from WMUX_LOG_LEVEL (one-off
// override) or the persisted ~/.wmux/loglevel file, same env-then-file
// resolution order as `wmux theme` — a launched-detached or Task-Scheduler
// wmuxd never inherits an env var set in some other shell.
package wmuxlog

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

// maxLogBytes is the rotation threshold: past this, the previous file is
// renamed to ".1" (clobbering any older ".1") and a fresh file started.
// Kept small on purpose — this is a debugging aid, not an audit trail.
const maxLogBytes = 10 * 1024 * 1024

// LogPath is where structured log entries are written. Same directory and
// name spawn_windows.go's startDaemonDetached has always redirected
// wmuxd's stdout/stderr to, so both paths converge on one file.
func LogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "wmuxd.log" // last resort: cwd
	}
	return filepath.Join(home, ".wmux", "wmuxd.log")
}

// levelPath is where `wmux log level <name>` persists a non-default level.
func levelPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".wmux", "loglevel")
}

// CurrentLevel resolves WMUX_LOG_LEVEL first, then the persisted file,
// then "info". An unrecognized value falls back to "info" rather than
// erroring — a typo in a log level shouldn't stop the daemon from starting.
func CurrentLevel() slog.Level {
	if lvl, ok := parseLevel(os.Getenv("WMUX_LOG_LEVEL")); ok {
		return lvl
	}
	if path := levelPath(); path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if lvl, ok := parseLevel(string(b)); ok {
				return lvl
			}
		}
	}
	return slog.LevelInfo
}

func parseLevel(s string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return 0, false
	}
}

// SetPersistedLevel validates name and writes it to levelPath, taking
// effect on the next wmuxd start (the running daemon's level, like the
// sidebar's theme, is resolved once at Init and doesn't hot-reload).
func SetPersistedLevel(name string) error {
	if _, ok := parseLevel(name); !ok {
		return fmt.Errorf("unknown log level %q — want debug, info, warn, or error", name)
	}
	path := levelPath()
	if path == "" {
		return fmt.Errorf("could not resolve home directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.ToLower(name)+"\n"), 0o644)
}

// rotate renames an oversized log file out of the way before it's reopened
// for append, so LogPath never grows unbounded across a long-running
// daemon's restarts.
func rotate(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < maxLogBytes {
		return
	}
	old := path + ".1"
	os.Remove(old) // best-effort; a failed remove just means Rename below fails too, which we also ignore
	os.Rename(path, old)
}

// Init opens LogPath (rotating it first if oversized) and installs it as
// slog's default logger, tagged with component. The returned close func
// should run via defer in main(); logging continues to work if it's
// forgotten (only buffered writes past a hard crash would be lost, and
// os.File writes here are unbuffered).
func Init(component string) (close func(), err error) {
	path := LogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	rotate(path)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}

	level := CurrentLevel()
	handler := slog.Handler(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: level}))

	// A real console attached to stderr (manual foreground start) gets a
	// human-readable mirror; a detached/Task-Scheduler wmuxd has no
	// console to write to, so skip the second handler entirely there.
	if term.IsTerminal(int(os.Stderr.Fd())) {
		handler = multiHandler{handler, slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})}
	}

	slog.SetDefault(slog.New(handler).With("component", component))
	return func() { f.Close() }, nil
}

// multiHandler fans a record out to every wrapped handler, each still
// filtering by its own configured level.
type multiHandler []slog.Handler

func (m multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil {
			return err
		}
	}
	return nil
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make(multiHandler, len(m))
	for i, h := range m {
		next[i] = h.WithAttrs(attrs)
	}
	return next
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	next := make(multiHandler, len(m))
	for i, h := range m {
		next[i] = h.WithGroup(name)
	}
	return next
}
