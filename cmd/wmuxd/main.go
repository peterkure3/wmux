// wmuxd is the background daemon: it owns agent sessions, watches their
// output for notification escape sequences, and serves session state over
// a local HTTP API for the wmux CLI and any UI client to consume.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/peterkure/wmux/internal/daemon"
	"github.com/peterkure/wmux/internal/version"
	"github.com/peterkure/wmux/internal/wmuxlog"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:47823", "address for the local HTTP API")
	statePath := flag.String("state", daemon.DefaultStatePath(), "path to persist session state across restarts (empty to disable)")
	showVersion := flag.Bool("version", false, "print the wmuxd version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.String())
		return
	}

	closeLog, err := wmuxlog.Init("wmuxd")
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmuxd: could not open log file: %v\n", err)
		os.Exit(1)
	}
	defer closeLog()

	d := daemon.New(*statePath)
	if err := d.Serve(*addr); err != nil {
		slog.Error("wmuxd exiting", "err", err)
		os.Exit(1)
	}
}
