package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/peterkure/wmux/internal/wmuxlog"
)

// cmdLog inspects/configures wmuxd's structured log: `wmux log` (path +
// current level), `wmux log tail [-n N]`, `wmux log level <name>`. Mirrors
// `wmux theme`'s shape — a persisted-file setting read fresh by wmuxd at
// its own startup, since a detached/Task-Scheduler wmuxd never inherits an
// env var set in some other shell.
func cmdLog(args []string) {
	if len(args) == 0 {
		fmt.Printf("log file: %s\n", wmuxlog.LogPath())
		fmt.Printf("level: %s\n", wmuxlog.CurrentLevel())
		return
	}

	switch args[0] {
	case "tail":
		cmdLogTail(args[1:])
	case "level":
		cmdLogLevel(args[1:])
	case "path":
		fmt.Println(wmuxlog.LogPath())
	default:
		fmt.Fprintf(os.Stderr, "wmux log: unknown subcommand %q — want tail, level, or path\n", args[0])
		os.Exit(1)
	}
}

func cmdLogTail(args []string) {
	n := 50
	for i := 0; i < len(args); i++ {
		if args[i] == "-n" && i+1 < len(args) {
			fmt.Sscanf(args[i+1], "%d", &n)
			i++
		}
	}

	f, err := os.Open(wmuxlog.LogPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux log tail: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	// The log is a debugging aid capped at 10MB by wmuxlog's own rotation,
	// not an append-only audit trail — reading it whole to grab the tail is
	// simpler than seeking, and cheap at that size.
	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "wmux log tail: %v\n", err)
		os.Exit(1)
	}

	start := 0
	if len(lines) > n {
		start = len(lines) - n
	}
	for _, line := range lines[start:] {
		fmt.Println(line)
	}
}

func cmdLogLevel(args []string) {
	if len(args) == 0 {
		fmt.Println(wmuxlog.CurrentLevel())
		return
	}
	if err := wmuxlog.SetPersistedLevel(args[0]); err != nil {
		fmt.Fprintf(os.Stderr, "wmux log level: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("log level set to %s — takes effect next wmuxd start\n", args[0])
}
