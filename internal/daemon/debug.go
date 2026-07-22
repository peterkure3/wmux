package daemon

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"github.com/peterkure/wmux/internal/proto"
	"github.com/peterkure/wmux/internal/version"
)

// ring is a small fixed-capacity FIFO buffer, used for both recovered
// panics and recent events — history a debugger can inspect after the
// fact without needing the file log (which may have rotated away).
type ring[T any] struct {
	mu    sync.Mutex
	items []T
	cap   int
}

func newRing[T any](capacity int) *ring[T] {
	return &ring[T]{cap: capacity}
}

func (r *ring[T]) add(item T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, item)
	if len(r.items) > r.cap {
		r.items = r.items[len(r.items)-r.cap:]
	}
}

func (r *ring[T]) snapshot() []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]T, len(r.items))
	copy(out, r.items)
	return out
}

// recordPanic appends a recovered panic to the bounded ring GET
// /debug/panics serves. 50 entries is plenty — this is a debugging aid,
// not an incident log — and keeps a runaway panicking goroutine from
// growing memory unbounded.
func (d *Daemon) recordPanic(source string, err, stack string) {
	d.panics.add(proto.PanicEntry{Time: time.Now(), Source: source, Err: err, Stack: stack})
}

// safeGo runs fn in a new goroutine with panic recovery. Before this, the
// codebase had zero recover() calls anywhere — a panic in any
// session-reader or metadata-poller goroutine took the entire daemon down,
// killing every session's tracking along with it. Recovering here means
// only that one goroutine's task is lost; the session itself is left as-is
// rather than guessed at, since silently mutating state after an
// unexpected panic risks compounding the original bug.
func (d *Daemon) safeGo(source string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				stack := string(debug.Stack())
				slog.Error("panic recovered in goroutine", "source", source, "panic", fmt.Sprint(r))
				d.recordPanic(source, fmt.Sprint(r), stack)
			}
		}()
		fn()
	}()
}

// recoverHandler wraps an HTTP handler so a panic returns a 500 instead of
// crashing wmuxd (Go's net/http server recovers per-connection panics on
// its own, but only by closing that connection — it neither logs a
// friendly message nor records anything a debugger can later inspect).
func (d *Daemon) recoverHandler(pattern string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				stack := string(debug.Stack())
				slog.Error("panic recovered in handler", "pattern", pattern, "panic", fmt.Sprint(rec))
				d.recordPanic(pattern, fmt.Sprint(rec), stack)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		h(w, r)
	}
}

// handleDebugState reports the daemon's own runtime health — version,
// uptime, goroutine count, and the current session table — for `wmux
// debug state` and as the core of a `wmux debug dump` bug-report bundle.
func (d *Daemon) handleDebugState(w http.ResponseWriter, r *http.Request) {
	sessions := d.List()
	json.NewEncoder(w).Encode(proto.DebugState{
		Version:      version.String(),
		StartedAt:    d.startedAt,
		Uptime:       time.Since(d.startedAt).Round(time.Second).String(),
		NumGoroutine: runtime.NumGoroutine(),
		NumSessions:  len(sessions),
		Sessions:     sessions,
	})
}

// handleDebugPanics returns every panic safeGo/recoverHandler has caught
// since this wmuxd process started (bounded ring, oldest evicted first).
func (d *Daemon) handleDebugPanics(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(d.panics.snapshot())
}

// handleDebugEvents returns the same recent-history ring GET /events
// subscribers stream live — useful for a `wmux debug dump` snapshot when
// no one had /events open at the time something went wrong.
func (d *Daemon) handleDebugEvents(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(d.recentEvents.snapshot())
}

// registerPprof mounts the stdlib's net/http/pprof handlers under
// /debug/pprof/ on d's own mux — pprof's functions default to registering
// on http.DefaultServeMux via import side effect, which doesn't reach a
// custom mux, so each entry point needs an explicit registration here.
func registerPprof(mux *http.ServeMux) {
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}
