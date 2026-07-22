// wmux debug is a runtime inspector for wmuxd itself — not a source-level
// Go debugger (delve already owns that job well), but a window into the
// daemon's own live state: session table, goroutine count, recovered
// panics, recent event history, and stdlib pprof profiles. Built to close
// the biggest reliability gap the codebase had: before internal/daemon's
// safeGo/recoverHandler (see debug.go there), a single panic anywhere in
// wmuxd took the whole process down with nothing left behind to diagnose
// it. `wmux debug dump` is the single most useful command here — a
// support-bundle snapshot for a bug report.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/peterkure/wmux/internal/proto"
	"github.com/peterkure/wmux/internal/wmuxlog"
)

func cmdDebug(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "wmux debug: want a subcommand — state, panics, events, dump, or pprof")
		os.Exit(1)
	}

	switch args[0] {
	case "state":
		cmdDebugState()
	case "panics":
		cmdDebugPanics()
	case "events":
		cmdDebugEvents()
	case "dump":
		cmdDebugDump()
	case "pprof":
		cmdDebugPprof(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "wmux debug: unknown subcommand %q — want state, panics, events, dump, or pprof\n", args[0])
		os.Exit(1)
	}
}

func fetchDebugJSON(path string, v any) error {
	resp, err := http.Get(daemonAddr + path)
	if err != nil {
		return fmt.Errorf("could not reach wmuxd: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("wmuxd returned %s: %s", resp.Status, body)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func cmdDebugState() {
	var state proto.DebugState
	if err := fetchDebugJSON("/debug/state", &state); err != nil {
		fmt.Fprintf(os.Stderr, "wmux debug state: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("version:   %s\n", state.Version)
	fmt.Printf("started:   %s\n", state.StartedAt.Format(time.RFC3339))
	fmt.Printf("uptime:    %s\n", state.Uptime)
	fmt.Printf("goroutines: %d\n", state.NumGoroutine)
	fmt.Printf("sessions:  %d\n", state.NumSessions)
	for _, s := range state.Sessions {
		status := "idle"
		if !s.Running {
			status = "exited"
		}
		fmt.Printf("  %-20s %-8s pid=%-8d native=%v surface=%v branch=%s\n",
			s.ID, status, s.PID, s.Native, s.Surface, s.Branch)
	}
}

func cmdDebugPanics() {
	var panics []proto.PanicEntry
	if err := fetchDebugJSON("/debug/panics", &panics); err != nil {
		fmt.Fprintf(os.Stderr, "wmux debug panics: %v\n", err)
		os.Exit(1)
	}
	if len(panics) == 0 {
		fmt.Println("no panics recovered since wmuxd started")
		return
	}
	for _, p := range panics {
		fmt.Printf("[%s] %s: %s\n%s\n\n", p.Time.Format(time.RFC3339), p.Source, p.Err, p.Stack)
	}
}

func cmdDebugEvents() {
	var events []proto.Event
	if err := fetchDebugJSON("/debug/events/recent", &events); err != nil {
		fmt.Fprintf(os.Stderr, "wmux debug events: %v\n", err)
		os.Exit(1)
	}
	if len(events) == 0 {
		fmt.Println("no recent events")
		return
	}
	for _, e := range events {
		b, _ := json.Marshal(e)
		fmt.Println(string(b))
	}
}

// debugDump is the JSON shape written by `wmux debug dump` — everything
// needed to triage a wmuxd bug report in one file, without needing the
// reporter to also grab the log separately.
type debugDump struct {
	Time    time.Time          `json:"time"`
	State   proto.DebugState   `json:"state"`
	Panics  []proto.PanicEntry `json:"panics"`
	Events  []proto.Event      `json:"events"`
	LogTail []string           `json:"logTail"`
}

func cmdDebugDump() {
	var dump debugDump
	dump.Time = time.Now()

	if err := fetchDebugJSON("/debug/state", &dump.State); err != nil {
		fmt.Fprintf(os.Stderr, "wmux debug dump: %v\n", err)
		os.Exit(1)
	}
	fetchDebugJSON("/debug/panics", &dump.Panics)
	fetchDebugJSON("/debug/events/recent", &dump.Events)
	dump.LogTail = readLogTail(200)

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux debug dump: could not resolve home directory: %v\n", err)
		os.Exit(1)
	}
	dumpDir := filepath.Join(home, ".wmux", "dumps")
	if err := os.MkdirAll(dumpDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "wmux debug dump: %v\n", err)
		os.Exit(1)
	}
	path := filepath.Join(dumpDir, fmt.Sprintf("dump-%s.json", dump.Time.Format("20060102-150405")))

	b, err := json.MarshalIndent(dump, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux debug dump: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "wmux debug dump: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(path)
}

// readLogTail reads wmuxlog's file directly (rather than round-tripping
// through wmuxd) so a dump still captures recent log lines even if the
// daemon is unreachable — the one piece of debugDump that doesn't depend
// on wmuxd responding at all.
func readLogTail(n int) []string {
	b, err := os.ReadFile(wmuxlog.LogPath())
	if err != nil {
		return nil
	}
	lines := splitLines(string(b))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// cmdDebugPprof wraps the stdlib net/http/pprof endpoints wmuxd exposes
// under /debug/pprof/: `wmux debug pprof cpu [seconds]`, `heap`, or
// `goroutine`, writing the raw profile to a timestamped file for `go tool
// pprof <file>` — no dependency beyond what net/http/pprof already needed
// daemon-side.
func cmdDebugPprof(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "wmux debug pprof: want a profile kind — cpu, heap, or goroutine")
		os.Exit(1)
	}

	kind := args[0]
	var url string
	switch kind {
	case "cpu":
		seconds := 5
		if len(args) > 1 {
			if s, err := strconv.Atoi(args[1]); err == nil {
				seconds = s
			}
		}
		url = fmt.Sprintf("%s/debug/pprof/profile?seconds=%d", daemonAddr, seconds)
	case "heap":
		url = daemonAddr + "/debug/pprof/heap"
	case "goroutine":
		url = daemonAddr + "/debug/pprof/goroutine"
	default:
		fmt.Fprintf(os.Stderr, "wmux debug pprof: unknown kind %q — want cpu, heap, or goroutine\n", kind)
		os.Exit(1)
	}

	resp, err := http.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux debug pprof: could not reach wmuxd: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "wmux debug pprof: wmuxd returned %s: %s\n", resp.Status, body)
		os.Exit(1)
	}

	path := fmt.Sprintf("wmux-pprof-%s-%s.prof", kind, time.Now().Format("20060102-150405"))
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux debug pprof: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		fmt.Fprintf(os.Stderr, "wmux debug pprof: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("%s\nrun: go tool pprof %s\n", path, path)
}
