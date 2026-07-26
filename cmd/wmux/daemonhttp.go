// Daemon HTTP plumbing for the CLI.
//
// Every request to wmuxd goes through here so three things are guaranteed
// by construction rather than by remembering to do them at ~18 call sites:
//
//   - the auth token is attached (wmuxd rejects unauthenticated requests;
//     see internal/daemon/auth.go for why an executes-arbitrary-commands
//     API on loopback needs one),
//   - a timeout applies, so a wedged daemon fails a command instead of
//     hanging it forever on http.DefaultClient's absent deadline, and
//   - the daemon on the other end is actually wmuxd, speaking a protocol
//     this wmux understands (see checkIdentify below) — checked once per
//     process, before the first real request.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/peterkure3/wmux/internal/authtoken"
	"github.com/peterkure3/wmux/internal/proto"
	"github.com/peterkure3/wmux/internal/version"
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

// daemonClient covers ordinary request/response calls. The timeout spans
// the whole exchange including the body read, which is correct here: every
// such response is a small JSON document.
var daemonClient = &http.Client{Timeout: 15 * time.Second}

// daemonStreamClient is for the long-lived streams — GET /events (SSE) and
// GET /surfaces/attach (NDJSON). It has no Timeout, because
// http.Client.Timeout bounds the body read too and would sever a live
// stream mid-session. Callers bound these with a context instead.
var daemonStreamClient = &http.Client{}

func daemonRequest(client *http.Client, method, path string, contentType string, body io.Reader) (*http.Response, error) {
	if path != "/identify" {
		checkIdentify()
	}
	req, err := http.NewRequest(method, daemonAddr+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set(authtoken.HeaderName, daemonToken)
	return client.Do(req)
}

// daemonPost replaces http.Post for daemon endpoints. path is the leading
// "/..." only; daemonAddr is prepended.
func daemonPost(path, contentType string, body io.Reader) (*http.Response, error) {
	return daemonRequest(daemonClient, http.MethodPost, path, contentType, body)
}

// daemonGet replaces http.Get for daemon endpoints.
func daemonGet(path string) (*http.Response, error) {
	return daemonRequest(daemonClient, http.MethodGet, path, "", nil)
}

// daemonStream is daemonGet for endpoints that stay open indefinitely.
func daemonStream(path string) (*http.Response, error) {
	return daemonRequest(daemonStreamClient, http.MethodGet, path, "", nil)
}

// daemonStreamRequest builds an authenticated streaming request the caller
// drives itself — used where a cancellable context has to own the
// connection's lifetime (wmux connect's detach path).
func daemonStreamRequest(req *http.Request) (*http.Response, error) {
	req.Header.Set(authtoken.HeaderName, daemonToken)
	return daemonStreamClient.Do(req)
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

var identifyOnce sync.Once

// checkIdentify calls GET /identify once per process, before this CLI's
// first real request to wmuxd, and exits immediately with a precise
// message if the two are protocol-incompatible or something other than
// wmuxd answered — rather than let the real request fail with a bare 401
// or an opaque decode error that gives no hint why.
//
// Any failure of the identify call itself — daemon unreachable, or an old
// wmuxd predating this endpoint (404) — is not an error here: the actual
// request this call is guarding will hit the same daemon right after and
// report that failure on its own terms.
func checkIdentify() {
	identifyOnce.Do(func() {
		resp, err := daemonRequest(daemonClient, http.MethodGet, "/identify", "", nil)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return
		}
		var id proto.IdentifyResponse
		if err := json.NewDecoder(resp.Body).Decode(&id); err != nil {
			return
		}
		if msg := identifyProblem(id); msg != "" {
			fmt.Fprintln(os.Stderr, "wmux: "+msg)
			os.Exit(3)
		}
	})
}

// identifyProblem compares a daemon's /identify response against this
// process's own expectations, returning the message to print and exit on,
// or "" if the pairing is fine. Split from checkIdentify so the decision
// itself is unit-testable without a live daemon or an os.Exit in the way.
func identifyProblem(id proto.IdentifyResponse) string {
	if id.App != "wmuxd" {
		return fmt.Sprintf("something other than wmuxd is answering on %s (got app=%q) — refusing to talk to it",
			daemonAddr, id.App)
	}
	if id.Protocol != proto.ProtocolVersion {
		return fmt.Sprintf("wmux %s cannot talk to wmuxd %s (protocol %d vs %d) — restart wmuxd",
			version.String(), id.Version, proto.ProtocolVersion, id.Protocol)
	}
	return ""
}
