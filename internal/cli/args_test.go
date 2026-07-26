package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/peterkure3/wmux/internal/client"
	"github.com/peterkure3/wmux/internal/proto"
)

// mockSessions points the shared daemon client at a server returning the
// given session list, so the ID-defaulting helpers can be tested without
// a real wmuxd.
func mockSessions(t *testing.T, sessions []proto.SessionInfo) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/identify", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(proto.IdentifyResponse{App: "wmuxd", Protocol: proto.ProtocolVersion})
	})
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(sessions)
	})
	srv := httptest.NewServer(mux)
	old := dc()
	setDC(client.New(srv.URL, "test-tok"))
	t.Cleanup(func() { setDC(old); srv.Close() })
}

func TestPositionalCommandPrefersTrailingArgs(t *testing.T) {
	if got := positionalCommand("", []string{"claude", "--resume"}); got != "claude --resume" {
		t.Fatalf("positionalCommand = %q, want the joined trailing args", got)
	}
	if got := positionalCommand("codex", nil); got != "codex" {
		t.Fatalf("positionalCommand = %q, want the flag value when there are no args", got)
	}
	// An explicit command as trailing args wins over the flag: it's the
	// form people actually type.
	if got := positionalCommand("codex", []string{"claude"}); got != "claude" {
		t.Fatalf("positionalCommand = %q, want the trailing args to win", got)
	}
	if got := positionalCommand("", []string{"   "}); got != "" {
		t.Fatalf("positionalCommand = %q, want empty for whitespace-only args", got)
	}
}

func TestResolveCwdDefaultsToWorkingDirectory(t *testing.T) {
	if got := resolveCwd("/explicit"); got != "/explicit" {
		t.Fatalf("resolveCwd(/explicit) = %q, want it unchanged", got)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Skip("no working directory")
	}
	if got := resolveCwd(""); got != wd {
		t.Fatalf("resolveCwd(\"\") = %q, want the current directory %q", got, wd)
	}
}

func TestResolveSessionIDDerivesFromCwd(t *testing.T) {
	mockSessions(t, nil)
	dir := filepath.Join(t.TempDir(), "myproject")
	if got := resolveSessionID("", dir); got != "myproject" {
		t.Fatalf("resolveSessionID = %q, want the directory's base name", got)
	}
	if got := resolveSessionID("explicit", dir); got != "explicit" {
		t.Fatalf("resolveSessionID = %q, want an explicit --id to win", got)
	}
}

// TestResolveSessionIDAvoidsRunningCollisions: two `wmux surface` runs in
// the same directory must not both claim the same ID, or the second one
// is rejected by the daemon for a reason the user never asked about.
func TestResolveSessionIDAvoidsRunningCollisions(t *testing.T) {
	mockSessions(t, []proto.SessionInfo{
		{ID: "myproject", Running: true},
		{ID: "myproject-2", Running: true},
		{ID: "myproject-3", Running: false}, // exited IDs are reusable
	})
	dir := filepath.Join(t.TempDir(), "myproject")
	if got := resolveSessionID("", dir); got != "myproject-3" {
		t.Fatalf("resolveSessionID = %q, want myproject-3 (first free name)", got)
	}
}

func TestResolveSurfaceIDPicksTheOnlyRunningOne(t *testing.T) {
	mockSessions(t, []proto.SessionInfo{
		{ID: "gone", Running: false, Surface: true},
		{ID: "piped", Running: true, Surface: false}, // not a surface: can't be connected to
		{ID: "live", Running: true, Surface: true},
	})
	if got := resolveSurfaceID("connect", nil); got != "live" {
		t.Fatalf("resolveSurfaceID = %q, want the single running surface", got)
	}
	if got := resolveSurfaceID("connect", []string{"named"}); got != "named" {
		t.Fatalf("resolveSurfaceID = %q, want an explicit argument to win", got)
	}
}

func TestGridCount(t *testing.T) {
	if n, err := gridCount(nil); err != nil || n != defaultGridPanes {
		t.Fatalf("gridCount(nil) = %d, %v; want the default with no error", n, err)
	}
	for _, arg := range []string{"1", "2", "3", "4", "9"} {
		if _, err := gridCount([]string{arg}); err != nil {
			t.Fatalf("gridCount(%q): %v", arg, err)
		}
	}
	for _, arg := range []string{"0", "-1", "claude", "4.5", ""} {
		if _, err := gridCount([]string{arg}); err == nil {
			t.Fatalf("gridCount(%q) accepted an invalid pane count", arg)
		}
	}
	if _, err := gridCount([]string{"999"}); err == nil {
		t.Fatal("gridCount accepted a pane count past maxGridPanes")
	}
}

// TestSplitGridArgsKeepsFlagsAfterTheCount is the regression guard for
// `wmux grid 4 --claude`: Go's flag package stops at the first non-flag
// argument, so the count has to come out before parsing or --claude is
// silently dropped and every pane opens a bare shell.
func TestSplitGridArgsKeepsFlagsAfterTheCount(t *testing.T) {
	count, rest := splitGridArgs([]string{"4", "--claude"})
	if len(count) != 1 || count[0] != "4" {
		t.Fatalf("count = %v, want [4]", count)
	}
	if len(rest) != 1 || rest[0] != "--claude" {
		t.Fatalf("rest = %v, want [--claude]", rest)
	}

	// Order-independent: the flag may come first.
	count, rest = splitGridArgs([]string{"--codex", "3"})
	if len(count) != 1 || count[0] != "3" {
		t.Fatalf("count = %v, want [3] with the flag first", count)
	}
	if len(rest) != 1 || rest[0] != "--codex" {
		t.Fatalf("rest = %v, want [--codex]", rest)
	}

	// A non-numeric positional stays in rest so gridCount can reject it
	// rather than the mistake passing silently.
	if _, rest = splitGridArgs([]string{"four"}); len(rest) != 1 || rest[0] != "four" {
		t.Fatalf("rest = %v, want the bad token preserved", rest)
	}

	// A flag *value* that happens to be numeric must not be mistaken for
	// the count.
	count, _ = splitGridArgs([]string{"--cwd", "/tmp"})
	if len(count) != 0 {
		t.Fatalf("count = %v, want none", count)
	}
}

func TestPaneIDBaseNamesGridsAfterTheAgent(t *testing.T) {
	if got := paneIDBase("/home/me/work", "claude"); got != "claude" {
		t.Fatalf("paneIDBase = %q, want the agent name", got)
	}
	if got := paneIDBase("/home/me/work", ""); got != "work" {
		t.Fatalf("paneIDBase = %q, want the directory's base name without an agent", got)
	}
}
