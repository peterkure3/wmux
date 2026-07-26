// Daemon access for the TUI.
//
// The TUI is a client over exactly the same wire the CLI uses (see
// internal/client): it never imports internal/cli, and internal/cli never
// reaches into it beyond Run. The one thing it needs from its embedder is
// *which* daemon to talk to, which SetClient supplies once at startup.
package tui

import (
	"net/http"
	"sync/atomic"

	"github.com/peterkure3/wmux/internal/client"
)

// dcState holds the daemon client behind an atomic pointer rather than a
// plain package var: production sets it once at startup, but tests swap it
// for a mock server while background attach goroutines started by an
// earlier test may still be reading it — a real data race -race catches.
var dcState atomic.Pointer[client.Client]

func init() { dcState.Store(client.New("", "")) }

// SetClient points the TUI at a daemon. Callers (internal/cli, and tests)
// must call this before Run.
func SetClient(c *client.Client) { dcState.Store(c) }

// dc returns the daemon client every TUI request goes through.
func dc() *client.Client { return dcState.Load() }

func daemonGet(path string) (*http.Response, error)    { return dc().Get(path) }
func daemonStream(path string) (*http.Response, error) { return dc().Stream(path) }
