package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/peterkure/wmux/internal/proto"
)

// Serve starts the local HTTP API. Bound to 127.0.0.1 only — this is a
// single-machine daemon, not a network service.
func (d *Daemon) Serve(addr string) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/sessions", d.handleSessions)
	mux.HandleFunc("/sessions/register", d.handleRegister)
	mux.HandleFunc("/sessions/deregister", d.handleDeregister)
	mux.HandleFunc("/sessions/close", d.handleClose)
	mux.HandleFunc("/sessions/prune", d.handlePrune)
	mux.HandleFunc("/surfaces", d.handleSurfaces)
	mux.HandleFunc("/surfaces/attach", d.handleSurfaceAttach)
	mux.HandleFunc("/surfaces/input", d.handleSurfaceInput)
	mux.HandleFunc("/surfaces/resize", d.handleSurfaceResize)
	mux.HandleFunc("/panes/pending", d.handlePanePending)
	mux.HandleFunc("/panes/claim", d.handlePaneClaim)
	mux.HandleFunc("/notify", d.handleNotify)
	mux.HandleFunc("/events", d.handleEvents)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/shutdown", handleShutdown)

	log.Printf("wmuxd listening on http://%s", addr)
	return http.ListenAndServe(addr, mux)
}

// handleShutdown exits the daemon cleanly on request — `wmux update` uses
// it to release wmuxd.exe's file lock before swapping the binary. State is
// persisted after every mutation, so a hard exit loses nothing;
// http.Server.Shutdown is deliberately not used because /events SSE
// subscribers hold their connections open indefinitely.
func handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	log.Printf("shutdown requested via /shutdown")
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

func (d *Daemon) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode(d.List())

	case http.MethodPost:
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

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRegister lets `wmux attach` register a session it owns the TTY
// for, without the daemon spawning or piping the process itself.
func (d *Daemon) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	json.NewEncoder(w).Encode(proto.PruneResult{Removed: d.Prune()})
}

// handlePanePending files a pane spec from `wmux pane`, to be claimed by
// the `wmux pane-exec` process that starts inside the new wt.exe pane.
func (d *Daemon) handlePanePending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
