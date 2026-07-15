package main

import (
	"fmt"
	"os"

	"github.com/peterkure/wmux/internal/proto"
)

// cmdSendKeys resolves --id against the daemon's session list and injects
// the given keys into that session's console. Native sessions only — a
// WSL-registered session's PID means nothing to AttachConsole (same
// namespace boundary pidVisible respects daemon-side).
func cmdSendKeys(args []string) {
	fs := newFlagSet("send-keys")
	id := fs.String("id", "", "session ID")
	fs.Parse(args)

	keys := fs.Args()
	if *id == "" {
		fmt.Fprintln(os.Stderr, "wmux send-keys: --id is required")
		os.Exit(1)
	}
	if len(keys) == 0 {
		fmt.Fprintln(os.Stderr, `wmux send-keys: missing keys, e.g. 'wmux send-keys --id x -- Enter' or -- "Ctrl c"`)
		os.Exit(1)
	}

	sessions := fetchSessions("send-keys")
	var target *proto.SessionInfo
	for i := range sessions {
		if sessions[i].ID == *id {
			target = &sessions[i]
			break
		}
	}
	if target == nil {
		fmt.Fprintf(os.Stderr, "wmux send-keys: session %q not found\n", *id)
		os.Exit(1)
	}
	if !target.Native {
		fmt.Fprintf(os.Stderr, "wmux send-keys: session %q is a WSL-targeted session — no native console to inject into\n", *id)
		os.Exit(1)
	}
	if !target.Running || target.PID == 0 {
		fmt.Fprintf(os.Stderr, "wmux send-keys: session %q is not running\n", *id)
		os.Exit(1)
	}

	if err := sendKeysToPID(target.PID, keys); err != nil {
		fmt.Fprintf(os.Stderr, "wmux send-keys: %v\n", err)
		os.Exit(1)
	}
}
