// Daemon HTTP plumbing for the CLI: a thin adapter from the free-standing
// functions every command in this package already calls (daemonGet,
// daemonPost, ...) onto internal/client.Client, which owns the actual
// HTTP mechanics — auth token, timeouts, and the /identify protocol
// handshake (see internal/client's doc comment for why that lives there
// and not here: a later TUI phase is a client over the same wire).
//
// Kept as function wrappers rather than switching every one of the ~20
// call sites across this package to method calls on a shared *client
// value, so this refactor is a pure swap of what's underneath — no other
// file changes.
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/peterkure3/wmux/internal/authtoken"
	"github.com/peterkure3/wmux/internal/client"
)

// daemonToken is read once at startup. A missing file yields "" — the
// daemon then answers 401 with a message naming the actual problem, which
// is more useful than this process dying before it can say which daemon
// it was talking to.
var daemonToken = func() string {
	tok, err := authtoken.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux: warning: could not read %s: %v\n", authtoken.Path(), err)
	}
	return tok
}()

// dc is the shared client every daemon-talking command in this package
// goes through.
var dc = client.New(daemonAddr, daemonToken)

// exitOnProtocolError turns a *client.ProtocolError into the CLI's own
// policy for it — print the precise message and exit 3 — so callers of
// daemonGet/daemonPost/daemonStream/daemonStreamRequest never see it as
// just another "could not reach wmuxd" error. internal/client itself
// only returns the typed error and never calls os.Exit: a future TUI
// client needs to handle the same error by showing a banner, not dying.
func exitOnProtocolError(err error) {
	if pe, ok := err.(*client.ProtocolError); ok {
		fmt.Fprintln(os.Stderr, "wmux: "+pe.Error())
		os.Exit(3)
	}
}

// daemonPost replaces http.Post for daemon endpoints. path is the leading
// "/..." only; daemonAddr is prepended.
func daemonPost(path, contentType string, body io.Reader) (*http.Response, error) {
	resp, err := dc.Post(path, contentType, body)
	exitOnProtocolError(err)
	return resp, err
}

// daemonGet replaces http.Get for daemon endpoints.
func daemonGet(path string) (*http.Response, error) {
	resp, err := dc.Get(path)
	exitOnProtocolError(err)
	return resp, err
}

// daemonStream is daemonGet for endpoints that stay open indefinitely.
func daemonStream(path string) (*http.Response, error) {
	resp, err := dc.Stream(path)
	exitOnProtocolError(err)
	return resp, err
}

// daemonStreamRequest builds an authenticated streaming request the caller
// drives itself — used where a cancellable context has to own the
// connection's lifetime (wmux connect's detach path).
func daemonStreamRequest(req *http.Request) (*http.Response, error) {
	resp, err := dc.StreamRequest(req)
	exitOnProtocolError(err)
	return resp, err
}

// describeStatus turns a non-2xx daemon response into a message that names
// the actual problem. 401/403 are singled out because they are the two new
// failure modes introduced by authentication, and "daemon returned 401
// Unauthorized" on its own sends people looking in the wrong place.
func describeStatus(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(body))
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Sprintf("not authorized to talk to wmuxd (%s). "+
			"This usually means wmux and wmuxd are different versions, or %s "+
			"is missing/unreadable. Restart wmuxd, then retry.", msg, authtoken.Path())
	case http.StatusForbidden:
		return fmt.Sprintf("wmuxd rejected this request as browser-originated (%s) — "+
			"this should not happen from the CLI; please file a bug", msg)
	default:
		return fmt.Sprintf("daemon returned %s: %s", resp.Status, msg)
	}
}
