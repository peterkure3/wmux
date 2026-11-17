// Package integration exercises the real binaries on Windows and Linux.
package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/peterkure3/wmux/internal/client"
	"github.com/peterkure3/wmux/internal/proto"
)

func TestBinaryLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs wmux/wmuxd")
	}
	dir := t.TempDir()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", dir+string(os.PathSeparator), "./cmd/wmux", "./cmd/wmuxd")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	wmux, wmuxd := filepath.Join(dir, "wmux"+ext), filepath.Join(dir, "wmuxd"+ext)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	// Verify a startup failure really exits before releasing this port.
	env := append(os.Environ(), "WMUX_ADDR=http://"+addr, "WMUX_TOKEN_FILE="+filepath.Join(dir, "token"), "WMUX_LOG_FILE="+filepath.Join(dir, "daemon.log"), "WMUX_LOG_LEVEL=info")
	bad := exec.CommandContext(ctx, wmuxd, "-addr", addr, "-state", filepath.Join(dir, "state.json"))
	bad.Env = env
	if err := bad.Run(); err == nil {
		t.Fatal("daemon started on an occupied port")
	}
	listener.Close()
	log, err := os.Create(filepath.Join(dir, "daemon.stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	daemon := exec.CommandContext(ctx, wmuxd, "-addr", addr, "-state", filepath.Join(dir, "state.json"))
	daemon.Env = env
	daemon.Stderr = log
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- daemon.Wait(); close(done) }()
	t.Cleanup(func() { daemon.Process.Kill(); <-done })
	httpClient := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := httpClient.Get("http://" + addr + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not become healthy")
		}
		select {
		case err := <-done:
			t.Fatalf("daemon exited during startup: %v", err)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	token, err := os.ReadFile(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatal(err)
	}
	c := client.New("http://"+addr, strings.TrimSpace(string(token)))
	id, err := c.Identify()
	if err != nil || id.PID != daemon.Process.Pid {
		t.Fatalf("wrong daemon identity: %+v, %v", id, err)
	}
	requestStatus := func(method, path, body, origin, auth string, want int) {
		t.Helper()
		req, err := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("X-Wmux-Token", auth)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s: got %d %s, want %d", path, resp.StatusCode, b, want)
		}
	}
	requestStatus("GET", "/sessions", "", "", "", 401)
	requestStatus("GET", "/sessions", "", "https://example.invalid", c.Token, 403)
	requestStatus("POST", "/surfaces", "{}", "", c.Token, 400)
	command := "bash --noprofile --norc"
	if runtime.GOOS == "windows" {
		command = "cmd.exe /q"
	}
	info, err := c.NewSurface(proto.NewSurfaceRequest{ID: "smoke", Cwd: dir, Command: command, Native: true, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if !info.Running || !info.Surface {
		t.Fatalf("invalid new surface: %+v", info)
	}
	t.Cleanup(func() { c.CloseSession("smoke") })
	requestStatus("POST", "/surfaces", `{"id":"smoke","command":"unused"}`, "", c.Token, 409)
	requestStatus("POST", "/surfaces/resize", `{"id":"smoke","cols":99999,"rows":24}`, "", c.Token, 400)
	requestStatus("POST", "/sessions/close", `{"id":"absent"}`, "", c.Token, 404)
	list := exec.CommandContext(ctx, wmux, "list")
	list.Env = env
	if out, err := list.CombinedOutput(); err != nil || !bytes.Contains(out, []byte("smoke")) {
		t.Fatalf("list: %v %s", err, out)
	}

	// Exercise replay, keyboard input, and replay after resize through the PTY.
	attachCtx, detach := context.WithTimeout(ctx, 10*time.Second)
	defer detach()
	req, err := c.NewStreamRequest(attachCtx, "/surfaces/attach?id=smoke")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.StreamRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	var frame proto.SurfaceFrame
	if err := dec.Decode(&frame); err != nil || frame.Type != proto.FrameReplay {
		t.Fatalf("initial replay: %v %+v", err, frame)
	}
	if err := c.InputSurface("smoke", []byte("echo WMUX_SMOKE_INPUT\r\n")); err != nil {
		t.Fatal(err)
	}
	for {
		if err := dec.Decode(&frame); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(frame.Data, []byte("WMUX_SMOKE_INPUT")) {
			break
		}
	}
	if err := c.ResizeSurface("smoke", 100, 30); err != nil {
		t.Fatal(err)
	}
	for {
		if err := dec.Decode(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type == proto.FrameReplay && frame.Cols == 100 && frame.Rows == 30 {
			break
		}
	}
	detach()
	resp.Body.Close()

	watch := exec.CommandContext(ctx, wmux, "watch")
	watch.Env = env
	var watchErrors bytes.Buffer
	watch.Stderr = &watchErrors
	stdout, err := watch.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := watch.Start(); err != nil {
		t.Fatal(err)
	}
	watchDone := make(chan error, 1)
	lines := make(chan string, 32)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), proto.MaxEventLineBytes)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
		watchDone <- watch.Wait()
		close(watchDone)
	}()
	t.Cleanup(func() {
		watch.Process.Kill()
		for range lines {
		}
		<-watchDone
	})
	// SSE headers arrive with the first event, so publish until the watcher
	// announces its subscription rather than relying on a fixed startup sleep.
	ready := false
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for !ready {
		if err := c.Notify(proto.NotifyEvent{SessionID: "smoke", Body: "probe"}); err != nil {
			t.Fatal(err)
		}
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("watch exited before subscribing")
			}
			ready = strings.Contains(line, "watching for notifications")
		case <-timer.C:
			t.Fatal("watch did not subscribe")
		case <-time.After(20 * time.Millisecond):
		}
	}
	message := strings.Repeat("x", 70000)
	if err := c.Notify(proto.NotifyEvent{SessionID: "smoke", Body: message}); err != nil {
		t.Fatal(err)
	}
	waitLine := func(want string) {
		t.Helper()
		timeout := time.NewTimer(5 * time.Second)
		defer timeout.Stop()
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatal("watch exited unexpectedly")
				}
				if strings.Contains(line, want) {
					return
				}
			case <-timeout.C:
				t.Fatal("notification was not delivered")
			}
		}
	}
	waitLine(message)
	if err := c.Notify(proto.NotifyEvent{SessionID: "smoke", Body: "still watching"}); err != nil {
		t.Fatal(err)
	}
	waitLine("still watching")
	if err := c.CloseSession("smoke"); err != nil {
		t.Fatal(err)
	}
	sessions, err := c.ListSessions()
	if err != nil || len(sessions) != 1 || sessions[0].Running {
		t.Fatalf("close: %+v %v", sessions, err)
	}
	pruned, err := c.Prune()
	if err != nil || len(pruned.Removed) != 1 {
		t.Fatalf("prune: %+v %v", pruned, err)
	}
	shutdown, err := c.Shutdown()
	if err != nil {
		t.Fatal(err)
	}
	shutdown.Body.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop")
	}
	for range lines {
	} // drain lifecycle events before waiting for the process
	err = <-watchDone
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 3 || !strings.Contains(watchErrors.String(), "notification stream") {
		t.Fatalf("watch disconnect: %v %s", err, &watchErrors)
	}
	fmt.Fprintln(os.Stderr, "binary smoke: auth, PTY input/replay/resize, notifications, lifecycle, and shutdown passed")
}

// TestOnDemandDaemonOutlivesItsInvokingCLI verifies startup without any logon
// registration. All endpoints/files are isolated from the user's real daemon.
func TestOnDemandDaemonOutlivesItsInvokingCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real binaries")
	}
	dir := t.TempDir()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", dir+string(os.PathSeparator), "./cmd/wmux", "./cmd/wmuxd")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	cli := filepath.Join(dir, "wmux"+ext)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	tokenPath := filepath.Join(dir, "isolated", "token")
	env := append(os.Environ(), "WMUX_ADDR=http://"+address, "WMUX_TOKEN_FILE="+tokenPath, "WMUX_LOG_FILE="+filepath.Join(dir, "daemon.log"))
	run := func() ([]byte, error) {
		cmd := exec.CommandContext(ctx, cli, "daemon", "start")
		cmd.Env = env
		return cmd.CombinedOutput()
	}
	out, err := run()
	if err != nil {
		t.Fatalf("cold CLI startup: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "wmuxd started in background") {
		t.Fatalf("startup notice missing: %s", out)
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	c := client.New("http://"+address, strings.TrimSpace(string(token)))
	identity, err := c.Identify()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if response, err := c.Post("/shutdown", "application/json", nil); err == nil {
			response.Body.Close()
		}
	})
	// The invoking CLI has exited, but the authenticated daemon still responds.
	if response, err := c.Get("/sessions"); err != nil {
		t.Fatal(err)
	} else {
		response.Body.Close()
	}
	out, err = run()
	if err != nil || !strings.Contains(string(out), "already running") {
		t.Fatalf("warm startup: %v %s", err, out)
	}
	same, err := c.Identify()
	if err != nil || same.PID != identity.PID {
		t.Fatalf("duplicate daemon was created: %+v %+v %v", identity, same, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(tokenPath), "state.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
