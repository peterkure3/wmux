// wmuxd is the background daemon: it owns agent sessions, watches their
// output for notification escape sequences, and serves session state over
// a local HTTP API for the wmux CLI and any UI client to consume.
package main

import (
	"flag"
	"log"

	"github.com/peterkure/wmux/internal/daemon"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:47823", "address for the local HTTP API")
	statePath := flag.String("state", daemon.DefaultStatePath(), "path to persist session state across restarts (empty to disable)")
	flag.Parse()

	d := daemon.New(*statePath)
	if err := d.Serve(*addr); err != nil {
		log.Fatalf("wmuxd: %v", err)
	}
}
