// Package client is the daemon HTTP client shared by every wmux front
// end. Today that's the CLI; a later phase adds a full-screen TUI that is
// also just a client over this same wire (per the phased refactor plan —
// see wmux-tui-refactor-plan.md's Part 1: "cmux is a server with a
// versioned protocol, and every UI is a client").
//
// Centralizing this buys two things a scattering of ad-hoc net/http calls
// can't:
//
//   - the auth token is attached and a timeout applies on every call by
//     construction, not by remembering to do it at each of the ~20 call
//     sites this replaced (wmuxd executes arbitrary commands; see
//     internal/daemon/auth.go for why that needs a token even on
//     loopback), and
//   - the /identify protocol handshake (see ProtocolError) runs once per
//     Client before its first real request and is reported as a typed
//     error instead of a bare 401 — a CLI can turn that into an exit
//     code, a TUI can show a banner instead. Neither is this package's
//     call to make, which is why it returns an error rather than calling
//     os.Exit itself.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/peterkure3/wmux/internal/authtoken"
	"github.com/peterkure3/wmux/internal/proto"
	"github.com/peterkure3/wmux/internal/version"
)

// DefaultAddr is where wmuxd's local HTTP API lives absent an override
// (WMUX_ADDR in the CLI's case — see cmd/wmux's daemonAddr).
const DefaultAddr = "http://127.0.0.1:47823"

// Client talks to one wmuxd over its local HTTP API.
type Client struct {
	Addr  string
	Token string

	http       *http.Client // ordinary request/response calls
	httpStream *http.Client // long-lived streams (SSE, NDJSON, pprof): no
	// Timeout, since http.Client.Timeout bounds the body read too and
	// would sever a live stream mid-session

	identifyOnce sync.Once
	identifyErr  error // sticky: computed once, returned by every call thereafter
}

// New builds a Client. addr defaults to DefaultAddr when empty.
func New(addr, token string) *Client {
	if addr == "" {
		addr = DefaultAddr
	}
	return &Client{
		Addr:       addr,
		Token:      token,
		http:       &http.Client{Timeout: 15 * time.Second},
		httpStream: &http.Client{},
	}
}

// StatusError is returned when a daemon response is well-formed HTTP but
// not the 2xx a call expected. Code lets a caller branch on a specific
// status — e.g. `wmux pane-exec`'s claim retry loop treats 404 as "not
// filed yet" rather than fatal — without string-matching Error().
type StatusError struct {
	Code   int
	Status string
	Body   string
}

func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("daemon returned %s", e.Status)
	}
	return fmt.Sprintf("daemon returned %s: %s", e.Status, e.Body)
}

// ProtocolError reports that whatever answered at Client.Addr is not a
// version- and identity-compatible wmuxd, per GET /identify (see
// proto.IdentifyResponse). It is the typed form of what used to be a bare
// 401 with no indication that the real problem was a stale binary on one
// side — hit for real when the auth-token patch landed and an old
// wmux.exe talked to a new wmuxd.exe.
type ProtocolError struct {
	ThisVersion    string
	DaemonVersion  string
	ThisProtocol   int
	DaemonProtocol int
	DaemonApp      string // set (and non-"wmuxd") only in the wrong-app case
	Addr           string
}

func (e *ProtocolError) Error() string {
	if e.DaemonApp != "" {
		return fmt.Sprintf("something other than wmuxd is answering on %s (got app=%q) — refusing to talk to it",
			e.Addr, e.DaemonApp)
	}
	return fmt.Sprintf("wmux %s cannot talk to wmuxd %s (protocol %d vs %d) — restart wmuxd",
		e.ThisVersion, e.DaemonVersion, e.ThisProtocol, e.DaemonProtocol)
}

// checkIdentify calls GET /identify once per Client, before its first real
// request, and records a *ProtocolError if the pairing is bad. Any failure
// of the identify call itself — daemon unreachable, or an old wmuxd
// predating this endpoint (404) — is not an error here: the real request
// right behind it will hit the same daemon and report that on its own
// terms.
func (c *Client) checkIdentify() error {
	c.identifyOnce.Do(func() {
		resp, err := c.rawRequest(http.MethodGet, "/identify", "", nil)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return
		}
		var id proto.IdentifyResponse
		if json.NewDecoder(resp.Body).Decode(&id) != nil {
			return
		}
		if id.App != "wmuxd" {
			c.identifyErr = &ProtocolError{DaemonApp: id.App, Addr: c.Addr}
			return
		}
		if id.Protocol != proto.ProtocolVersion {
			c.identifyErr = &ProtocolError{
				ThisVersion: version.String(), DaemonVersion: id.Version,
				ThisProtocol: proto.ProtocolVersion, DaemonProtocol: id.Protocol,
			}
		}
	})
	return c.identifyErr
}

// rawRequest is the actual HTTP call, with no identify check — checkIdentify
// itself goes through this to avoid recursing into itself.
func (c *Client) rawRequest(method, path, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, c.Addr+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set(authtoken.HeaderName, c.Token)
	return c.http.Do(req)
}

// Get issues an authenticated GET, after the identify check.
func (c *Client) Get(path string) (*http.Response, error) {
	if path != "/identify" {
		if err := c.checkIdentify(); err != nil {
			return nil, err
		}
	}
	return c.rawRequest(http.MethodGet, path, "", nil)
}

// Post issues an authenticated POST, after the identify check.
func (c *Client) Post(path, contentType string, body io.Reader) (*http.Response, error) {
	if path != "/identify" {
		if err := c.checkIdentify(); err != nil {
			return nil, err
		}
	}
	return c.rawRequest(http.MethodPost, path, contentType, body)
}

// Stream is Get for an endpoint that stays open indefinitely (SSE, NDJSON,
// a pprof profile that blocks for its capture window) — no client-side
// read timeout.
func (c *Client) Stream(path string) (*http.Response, error) {
	if err := c.checkIdentify(); err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, c.Addr+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(authtoken.HeaderName, c.Token)
	return c.httpStream.Do(req)
}

// StreamRequest runs a caller-built streaming request (used where a
// cancellable context has to own the connection's lifetime — `wmux
// connect`'s detach path). The auth token is attached here; the caller
// builds everything else (method, URL, context).
func (c *Client) StreamRequest(req *http.Request) (*http.Response, error) {
	if err := c.checkIdentify(); err != nil {
		return nil, err
	}
	req.Header.Set(authtoken.HeaderName, c.Token)
	return c.httpStream.Do(req)
}

// NewStreamRequest builds (but does not send) a context-bound GET request
// against this client's daemon — the piece `wmux connect` needs so a
// detach can cancel the underlying connection.
func (c *Client) NewStreamRequest(ctx context.Context, path string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodGet, c.Addr+path, nil)
}

// GetJSON GETs path and decodes a 200 response's body into v.
func (c *Client) GetJSON(path string, v any) error {
	resp, err := c.Get(path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// postJSON POSTs reqBody (marshaled, or no body if nil) to path, and on a
// response matching wantStatus decodes the body into respBody (skipped if
// nil). Any other status becomes a *StatusError.
func (c *Client) postJSON(path string, reqBody, respBody any, wantStatus int) error {
	var r io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	resp, err := c.Post(path, "application/json", r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		return &StatusError{Code: resp.StatusCode, Status: resp.Status, Body: strings.TrimSpace(string(body))}
	}
	if respBody != nil {
		return json.Unmarshal(body, respBody)
	}
	return nil
}

func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &StatusError{Code: resp.StatusCode, Status: resp.Status, Body: strings.TrimSpace(string(body))}
}

// Identify calls GET /identify directly, bypassing the sticky check (and
// the sticky error it would otherwise return) — used by callers that want
// the raw answer, e.g. a future `wmux daemon status`.
func (c *Client) Identify() (proto.IdentifyResponse, error) {
	var id proto.IdentifyResponse
	resp, err := c.rawRequest(http.MethodGet, "/identify", "", nil)
	if err != nil {
		return id, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return id, statusError(resp)
	}
	return id, json.NewDecoder(resp.Body).Decode(&id)
}

// Healthz reports whether wmuxd answers GET /healthz — the one endpoint
// that needs no token, so it works across a version skew where this
// process may not have a readable one yet (see `wmux update`).
func (c *Client) Healthz() bool {
	resp, err := c.rawRequest(http.MethodGet, "/healthz", "", nil)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// ListSessions is GET /sessions.
func (c *Client) ListSessions() ([]proto.SessionInfo, error) {
	var sessions []proto.SessionInfo
	if err := c.GetJSON("/sessions", &sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

// Spawn is POST /sessions — a daemon-owned, piped (no TTY) session.
func (c *Client) Spawn(req proto.NewSessionRequest) (proto.SessionInfo, error) {
	var info proto.SessionInfo
	err := c.postJSON("/sessions", req, &info, http.StatusOK)
	return info, err
}

// Register is POST /sessions/register — `wmux attach`'s tracking-only
// registration for a session it owns the TTY for itself.
func (c *Client) Register(req proto.RegisterSessionRequest) (proto.SessionInfo, error) {
	var info proto.SessionInfo
	err := c.postJSON("/sessions/register", req, &info, http.StatusOK)
	return info, err
}

// Deregister is POST /sessions/deregister.
func (c *Client) Deregister(id string) error {
	return c.postJSON("/sessions/deregister", proto.DeregisterSessionRequest{ID: id}, nil, http.StatusOK)
}

// CloseSession is POST /sessions/close.
func (c *Client) CloseSession(id string) error {
	return c.postJSON("/sessions/close", proto.CloseSessionRequest{ID: id}, nil, http.StatusOK)
}

// Prune is POST /sessions/prune.
func (c *Client) Prune() (proto.PruneResult, error) {
	var result proto.PruneResult
	err := c.postJSON("/sessions/prune", nil, &result, http.StatusOK)
	return result, err
}

// Notify is POST /notify. The daemon answers 202 Accepted, not 200 — it's
// a fire-and-forget push, not a create.
func (c *Client) Notify(evt proto.NotifyEvent) error {
	return c.postJSON("/notify", evt, nil, http.StatusAccepted)
}

// NewSurface is POST /surfaces — a daemon-owned ConPTY session.
func (c *Client) NewSurface(req proto.NewSurfaceRequest) (proto.SessionInfo, error) {
	var info proto.SessionInfo
	err := c.postJSON("/surfaces", req, &info, http.StatusOK)
	return info, err
}

// ResizeSurface is POST /surfaces/resize.
func (c *Client) ResizeSurface(id string, cols, rows int) error {
	return c.postJSON("/surfaces/resize", proto.SurfaceResizeRequest{ID: id, Cols: cols, Rows: rows}, nil, http.StatusOK)
}

// InputSurface is POST /surfaces/input.
func (c *Client) InputSurface(id string, data []byte) error {
	return c.postJSON("/surfaces/input", proto.SurfaceInputRequest{ID: id, Data: data}, nil, http.StatusOK)
}

// Shutdown is POST /shutdown. It returns the raw response rather than an
// error on non-200 — `wmux update` treats a 404 specially (a running
// wmuxd predating this endpoint) rather than as failure.
func (c *Client) Shutdown() (*http.Response, error) {
	return c.Post("/shutdown", "application/json", nil)
}
