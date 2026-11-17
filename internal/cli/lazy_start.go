package cli

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/peterkure3/wmux/internal/authtoken"
	"github.com/peterkure3/wmux/internal/client"
	"github.com/peterkure3/wmux/internal/proto"
)

// daemonLaunchArgs keeps development daemons isolated: the CLI's URL and token
// file must be supplied together, and automatic startup never binds remotely.
func daemonLaunchArgs(addr, tokenFile string) ([]string, error) {
	if addr == "" && tokenFile == "" {
		return nil, nil
	}
	if addr == "" || tokenFile == "" {
		return nil, fmt.Errorf("automatic isolated startup requires WMUX_ADDR and WMUX_TOKEN_FILE together")
	}
	u, err := url.Parse(addr)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("WMUX_ADDR must be a local http://host:port URL")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("automatic daemon startup is restricted to loopback")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("WMUX_ADDR needs a valid port")
	}
	return []string{"-addr", net.JoinHostPort(host, u.Port()), "-state", filepath.Join(filepath.Dir(tokenFile), "state.json")}, nil
}

// detachedDaemonCommand only prepares a direct wmuxd process; no cmd.exe,
// PowerShell wrapper, scheduled task or startup-folder entry is involved.
func detachedDaemonCommand(path string) (*exec.Cmd, io.Closer, error) {
	args, err := daemonLaunchArgs(os.Getenv("WMUX_ADDR"), os.Getenv(authtoken.EnvVar))
	if err != nil {
		return nil, nil, err
	}
	logPath := os.Getenv("WMUX_LOG_FILE")
	if logPath == "" {
		logPath = filepath.Join(filepath.Dir(authtoken.Path()), "wmuxd.log")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, nil, err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = log, log
	return cmd, log, nil
}

func refreshDaemonClient() error {
	token, err := authtoken.Load()
	if err != nil {
		return fmt.Errorf("could not read daemon token: %w", err)
	}
	c := client.New(daemonAddr, token)
	identity, err := c.Identify()
	if err != nil {
		return fmt.Errorf("could not identify wmuxd: %w", err)
	}
	if identity.App != "wmuxd" || identity.Protocol != proto.ProtocolVersion {
		return fmt.Errorf("incompatible service at %s; refusing to use it", daemonAddr)
	}
	dcState.Store(c)
	return nil
}

// ensureDaemonStarted is invoked by explicit terminal launch commands, never by
// notification hooks, package initialization, Windows logon or an AC-power event.
func ensureDaemonStarted() (bool, error) {
	if daemonRunning() {
		return false, refreshDaemonClient()
	}
	if _, err := daemonLaunchArgs(os.Getenv("WMUX_ADDR"), os.Getenv(authtoken.EnvVar)); err != nil {
		return false, err
	}
	path, err := siblingWmuxd()
	if err != nil {
		return false, err
	}
	if _, err := authtoken.LoadOrCreate(); err != nil {
		return false, err
	}
	if err := startDaemonDetached(path); err != nil {
		return false, err
	}
	if !pollHealthz(30*time.Second, true) {
		return false, fmt.Errorf("wmuxd did not become ready; inspect its background log")
	}
	if err := refreshDaemonClient(); err != nil {
		return false, err
	}
	return true, nil
}

func startForTerminal() {
	started, err := ensureDaemonStarted()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux: %v\n", err)
		os.Exit(1)
	}
	if started {
		fmt.Fprintln(os.Stderr, "wmux: wmuxd started in background")
	}
}

type daemonStartCmd struct{}

func (*daemonStartCmd) Run() error {
	started, err := ensureDaemonStarted()
	if err != nil {
		return err
	}
	if started {
		fmt.Println("wmux: wmuxd started in background")
	} else {
		fmt.Println("wmux: wmuxd already running")
	}
	return nil
}
