package wmuxlog

import (
	"log/slog"
	"os"
	"strings"
	"testing"
)

// isolateHome points USERPROFILE/HOME at a fresh temp dir, mirroring the
// pattern in cmd/wmux/sidebarui_verify_test.go, so LogPath/levelPath never
// touch this machine's real ~/.wmux.
func isolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
}

func TestCurrentLevelFallback(t *testing.T) {
	isolateHome(t)
	t.Setenv("WMUX_LOG_LEVEL", "")
	if got := CurrentLevel(); got != slog.LevelInfo {
		t.Fatalf("default level = %v, want info", got)
	}

	t.Setenv("WMUX_LOG_LEVEL", "bogus")
	if got := CurrentLevel(); got != slog.LevelInfo {
		t.Fatalf("unknown env level = %v, want info fallback", got)
	}

	t.Setenv("WMUX_LOG_LEVEL", "debug")
	if got := CurrentLevel(); got != slog.LevelDebug {
		t.Fatalf("WMUX_LOG_LEVEL=debug = %v, want debug", got)
	}
}

func TestSetPersistedLevel(t *testing.T) {
	isolateHome(t)
	t.Setenv("WMUX_LOG_LEVEL", "")

	if err := SetPersistedLevel("warn"); err != nil {
		t.Fatalf("SetPersistedLevel: %v", err)
	}
	if got := CurrentLevel(); got != slog.LevelWarn {
		t.Fatalf("after persisting warn: %v, want warn", got)
	}

	if err := SetPersistedLevel("not-a-level"); err == nil {
		t.Fatal("expected an error for an unknown level name")
	}

	// Env var still wins over the persisted file.
	t.Setenv("WMUX_LOG_LEVEL", "error")
	if got := CurrentLevel(); got != slog.LevelError {
		t.Fatalf("WMUX_LOG_LEVEL=error should override persisted warn, got %v", got)
	}
}

func TestRotate(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/wmuxd.log"

	small := []byte("small\n")
	if err := os.WriteFile(path, small, 0o644); err != nil {
		t.Fatal(err)
	}
	rotate(path)
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Fatal("small file should not have been rotated")
	}

	big := make([]byte, maxLogBytes+1)
	if err := os.WriteFile(path, big, 0o644); err != nil {
		t.Fatal(err)
	}
	rotate(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("original path should be gone after rotation, stat err = %v", err)
	}
	rotated, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("rotated file missing: %v", err)
	}
	if len(rotated) != len(big) {
		t.Fatalf("rotated file size = %d, want %d", len(rotated), len(big))
	}
}

func TestInitWritesJSONLog(t *testing.T) {
	isolateHome(t)
	t.Setenv("WMUX_LOG_LEVEL", "debug")

	closeLog, err := Init("test-component")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer closeLog()

	slog.Info("hello from test", "n", 42)
	closeLog()

	b, err := os.ReadFile(LogPath())
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	out := string(b)
	if !strings.Contains(out, `"msg":"hello from test"`) {
		t.Fatalf("log missing expected message:\n%s", out)
	}
	if !strings.Contains(out, `"component":"test-component"`) {
		t.Fatalf("log missing component tag:\n%s", out)
	}
}
