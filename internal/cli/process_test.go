package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCLIHelperProcess(t *testing.T) {
	if os.Getenv("WMUX_CLI_TEST_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"wmux"}, os.Args[i+1:]...)
			Main()
			os.Exit(0)
		}
	}
	os.Exit(99)
}

func TestCLIExitCodesAndStreamErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		status int
		body   string
		code   int
		want   string
	}{
		{"version", []string{"version"}, 200, "", 0, ""},
		{"unknown", []string{"does-not-exist"}, 200, "", 2, "unexpected argument"},
		{"notify missing message", []string{"notify"}, 200, "", 2, "missing <message>"},
		{"watch unauthorized", []string{"watch"}, 401, "bad token", 3, "not authorized"},
		{"watch server error", []string{"watch"}, 500, "server failed", 3, "server failed"},
		{"watch disconnect", []string{"watch"}, 200, "", 3, "disconnected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/identify" {
					fmt.Fprint(w, `{"app":"wmuxd","protocol":1}`)
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, append([]string{"-test.run=^TestCLIHelperProcess$", "--"}, tc.args...)...)
			cmd.Env = append(os.Environ(), "WMUX_CLI_TEST_HELPER=1", "WMUX_ADDR="+srv.URL, "WMUX_TOKEN_FILE="+t.TempDir()+"/missing-token")
			b, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				if e, ok := err.(*exec.ExitError); ok {
					code = e.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			if code != tc.code || !strings.Contains(string(b), tc.want) {
				t.Fatalf("exit %d, output %s; want exit %d containing %q", code, b, tc.code, tc.want)
			}
		})
	}
}
