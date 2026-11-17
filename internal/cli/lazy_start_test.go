package cli

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDaemonLaunchArgsRejectRemoteAndUnpairedIsolation(t *testing.T) {
	for _, tc := range []struct{ addr, token string }{
		{"http://127.0.0.1:49000", ""},
		{"", "/tmp/token"},
		{"http://example.com:49000", "/tmp/token"},
		{"http://0.0.0.0:49000", "/tmp/token"},
		{"http://user:password@127.0.0.1:49000", "/tmp/token"},
		{"https://127.0.0.1:49000", "/tmp/token"},
		{"http://127.0.0.1:49000/path", "/tmp/token"},
		{"http://127.0.0.1", "/tmp/token"},
	} {
		if _, err := daemonLaunchArgs(tc.addr, tc.token); err == nil {
			t.Errorf("accepted unsafe/unpaired address %q", tc.addr)
		}
	}
	if args, err := daemonLaunchArgs("", ""); err != nil || len(args) != 0 {
		t.Fatalf("default startup: %v %v", args, err)
	}
	token := filepath.Join(t.TempDir(), "token")
	args, err := daemonLaunchArgs("http://127.0.0.1:49000", token)
	want := []string{"-addr", "127.0.0.1:49000", "-state", filepath.Join(filepath.Dir(token), "state.json")}
	if err != nil || !reflect.DeepEqual(args, want) {
		t.Fatalf("isolated startup: %v %v", args, err)
	}
}

func TestDetachedDaemonIsDirectAndUsesPrivateLog(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WMUX_ADDR", "http://127.0.0.1:49001")
	t.Setenv("WMUX_TOKEN_FILE", filepath.Join(dir, "token"))
	t.Setenv("WMUX_LOG_FILE", filepath.Join(dir, "daemon.log"))
	executable := filepath.Join(dir, "wmuxd")
	cmd, closer, err := detachedDaemonCommand(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	if cmd.Path != executable || len(cmd.Args) < 2 || strings.Contains(cmd.Path, "cmd.exe") {
		t.Fatalf("not a direct daemon launch: %+v", cmd.Args)
	}
	if cmd.Stdin != nil || cmd.Stdout == nil || cmd.Stderr == nil {
		t.Fatal("daemon streams must be detached from terminal")
	}
}
