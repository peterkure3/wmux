package daemon

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/peterkure3/wmux/internal/proto"
)

// authHeader is the request header carrying the shared token. Kept as a
// local const so package daemon doesn't import the CLI-facing authtoken
// package just for a string.
const authHeader = "X-Wmux-Token"

// Serve starts the local HTTP API.
//
// Bound to 127.0.0.1, but note that loopback is NOT a trust boundary: any
// local process can reach it, and so can any web page the user visits
// (a cross-origin POST with a simple Content-Type needs no preflight, and
// the handlers below decode the body regardless of Content-Type). Since
// POST /sessions executes its Command field, every route except /healthz
// goes through d.guard — see auth.go.
func (d *Daemon) Serve(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           d.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// WriteTimeout is deliberately 0: /events (SSE) and
		// /surfaces/attach (NDJSON) are long-lived streams that a write
		// deadline would sever mid-session. The read-side timeouts above
		// still bound a client that opens a connection and then stalls.
	}

	slog.Info("wmuxd listening", "addr", addr)
	return srv.ListenAndServe()
}

// handler builds the API's routing table. Separate from Serve so tests can
// exercise the real mux — in particular, that every route is behind guard.
func (d *Daemon) handler() http.Handler {
	mux := http.NewServeMux()

	// Method patterns (Go 1.22+) mean each handler no longer has to
	// reject the wrong verb itself; the mux answers 405 on its own.
	route := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc(pattern, d.recoverHandler(pattern, d.guard(h)))
	}

	route("GET /sessions", d.handleListSessions)
	route("POST /sessions", d.handleSpawnSession)
	route("POST /sessions/register", d.handleRegister)
	route("POST /sessions/deregister", d.handleDeregister)
	route("POST /sessions/close", d.handleClose)
	route("POST /sessions/prune", d.handlePrune)
	route("POST /surfaces", d.handleSurfaces)
	route("GET /surfaces/attach", d.handleSurfaceAttach)
	route("POST /surfaces/input", d.handleSurfaceInput)
	route("POST /surfaces/resize", d.handleSurfaceResize)
	route("POST /panes/pending", d.handlePanePending)
	route("POST /panes/claim", d.handlePaneClaim)
	route("POST /notify", d.handleNotify)
	route("GET /events", d.handleEvents)
	route("GET /debug/state", d.handleDebugState)
	route("GET /debug/panics", d.handleDebugPanics)
	route("GET /debug/events/recent", d.handleDebugEvents)
	route("POST /shutdown", handleShutdown)

	// pprof is guarded too: /debug/pprof/cmdline leaks this process's argv
	// and /debug/pprof/profile is a free 30-second CPU burn for anyone who
	// can reach it. Not wrapped by recoverHandler — they're diagnostic
	// tools in their own right.
	registerPprof(mux, d.guard)

	// /healthz is the sole exemption: it returns no data and mutates
	// nothing, and `wmux update` depends on probing it across a version
	// skew where the CLI may not have a token yet.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	return mux
}

// handleShutdown exits the daemon cleanly on request — `wmux update` uses
// it to release wmuxd.exe's file lock before swapping the binary. State is
// persisted after every mutation, so a hard exit loses nothing;
// http.Server.Shutdown is deliberately not used because /events SSE
// subscribers hold their connections open indefinitely.
func handleShutdown(w http.ResponseWriter, r *http.Request) {
	slog.Info("shutdown requested via /shutdown")
	// Content-Length lets the client complete its read before os.Exit
	// tears the socket down — without it the response is delimited by
	// connection close, which the abrupt exit turns into a reset.
	body := []byte("shutting down")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.Write(body)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	// Give the response a beat to reach the client before the process dies.
	go func() {
		time.Sleep(300 * time.Millisecond)
		os.Exit(0)
	}()
}

func (d *Daemon) handleListSessions(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(d.List())
}

func (d *Daemon) handleSpawnSession(w http.ResponseWriter, r *http.Request) {
	var req proto.NewSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sess, err := d.Spawn(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	json.NewEncoder(w).Encode(sess.Info())
}

// handleRegister lets `wmux attach` register a session it owns the TTY
// for, without the daemon spawning or piping the process itself.
func (d *Daemon) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req proto.RegisterSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sess, err := d.Register(req.ID, req.Cwd, req.Distro, req.PID, req.Native)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	json.NewEncoder(w).Encode(sess.Info())
}

// handleDeregister marks an attach-mode session as no longer running,
// called by `wmux attach` right before it exits.
func (d *Daemon) handleDeregister(w http.ResponseWriter, r *http.Request) {
	var req proto.DeregisterSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := d.Deregister(req.ID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleClose kills a session's tracked process — called by `wmux close`.
func (d *Daemon) handleClose(w http.ResponseWriter, r *http.Request) {
	var req proto.CloseSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := d.Close(req.ID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handlePrune removes all exited sessions from daemon state — called by
// `wmux prune`.
func (d *Daemon) handlePrune(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(proto.PruneResult{Removed: d.Prune()})
}

// handlePanePending files a pane spec from `wmux pane`, to be claimed by
// the `wmux pane-exec` process that starts inside the new wt.exe pane.
func (d *Daemon) handlePanePending(w http.ResponseWriter, r *http.Request) {
	var spec proto.PaneSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if spec.ID == "" || spec.Command == "" {
		http.Error(w, "pane spec needs id and command", http.StatusBadRequest)
		return
	}
	d.AddPaneSpec(spec)
	w.WriteHeader(http.StatusOK)
}

// handlePaneClaim hands a pending pane spec to the pane that will run it.
func (d *Daemon) handlePaneClaim(w http.ResponseWriter, r *http.Request) {
	var req proto.ClaimPaneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	spec, err := d.ClaimPaneSpec(req.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(spec)
}

// handleNotify lets a CLI push a notification directly over HTTP, as an
// alternative to emitting a raw OSC escape sequence on stdout — useful for
// hooks that can't easily write to the session's own PTY (e.g. a hook
// script running in a different process than the shell wmuxd is tailing).
func (d *Daemon) handleNotify(w http.ResponseWriter, r *http.Request) {
	var evt proto.NotifyEvent
	if err := json.NewDecoder(r.Body).Decode(&evt); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	evt.Time = time.Now() // always stamp server-side; don't trust the client's clock/omission

	d.mu.RLock()
	sess, ok := d.sessions[evt.SessionID]
	d.mu.RUnlock()
	if ok {
		sess.mu.Lock()
		sess.lastNote = clipNote(evt.Display())
		sess.mu.Unlock()
	}

	d.publishNotify(evt)
	// lastNote is part of SessionInfo, so the session list changed too.
	if ok {
		d.publishSessions()
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleEvents streams notifications as Server-Sent Events so a tray/UI
// client gets a live push feed instead of polling.
func (d *Daemon) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := d.Subscribe()
	defer d.Unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			b, _ := json.Marshal(evt)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

// handleSurfaces creates a surface session — a daemon-owned ConPTY the
// agent runs inside, attachable/detachable like a tmux session. Called by
// `wmux surface`.
func (d *Daemon) handleSurfaces(w http.ResponseWriter, r *http.Request) {
	var req proto.NewSurfaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sess, err := d.SpawnSurface(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	json.NewEncoder(w).Encode(sess.Info())
}

// handleSurfaceAttach streams a surface's screen to a client as JSON
// lines: one replay frame (full current screen) up front, then ordered
// output frames, with a fresh replay after any resize and an exit frame
// when the process ends. Called by `wmux connect`.
func (d *Daemon) handleSurfaceAttach(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	id := r.URL.Query().Get("id")

	ch, err := d.AttachSurface(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer d.DetachSurface(id, ch)

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")

	enc := json.NewEncoder(w)
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-ch:
			if err := enc.Encode(frame); err != nil {
				return
			}
			flusher.Flush()
			if frame.Type == proto.FrameExit {
				return
			}
		}
	}
}

// handleSurfaceInput writes client keystrokes to a surface's pty.
func (d *Daemon) handleSurfaceInput(w http.ResponseWriter, r *http.Request) {
	var req proto.SurfaceInputRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := d.InputSurface(req.ID, req.Data); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleSurfaceResize resizes a surface's pty + screen model; every
// attached client then receives a fresh replay at the new size.
func (d *Daemon) handleSurfaceResize(w http.ResponseWriter, r *http.Request) {
	var req proto.SurfaceResizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := d.ResizeSurface(req.ID, req.Cols, req.Rows); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}
