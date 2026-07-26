// Argument defaults shared by the session-creating commands.
//
// The policy, per Plan_B: nothing is required. Typing `wmux surface
// claude` should work, because every value the daemon needs has a
// defensible default — the current directory, a session ID derived from
// it, WSL or native chosen from the daemon's platform. Flags stay, and
// still win, for the cases where the default is wrong.
//
// Where a command takes a command line to run, the positional arguments
// *are* that command (`wmux surface claude --dangerously-skip`); where it
// doesn't, the positional argument is the session ID (`wmux connect
// review`). That split is what keeps both forms unambiguous without a
// `--` separator.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/peterkure3/wmux/internal/proto"
)

// positionalCommand joins a command's trailing arguments back into one
// command line, preferring an explicit --cmd/--with when given. Quoting
// that mattered to the caller's own shell is already gone by the time
// argv reaches here, which is exactly why `--cmd -` (read from stdin)
// still exists for the hard cases.
func positionalCommand(flagValue string, args []string) string {
	if rest := strings.TrimSpace(strings.Join(args, " ")); rest != "" {
		return rest
	}
	return flagValue
}

// resolveCwd expands an omitted working directory to the caller's own.
func resolveCwd(cwd string) string {
	if cwd != "" {
		return cwd
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// resolveSessionID returns id, or derives one from cwd's base name and
// makes it unique against the sessions the daemon already has running.
// Daemon lookup failures are not fatal: a name that turns out to collide
// gets a clear error from the daemon itself a moment later, which beats
// refusing to start over an unreachable-daemon check the real request is
// about to make anyway.
func resolveSessionID(id, cwd string) string {
	if id != "" {
		return id
	}
	base := filepath.Base(strings.TrimRight(resolveCwd(cwd), `\/`))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "session"
	}
	running := map[string]bool{}
	for _, s := range trySessions() {
		if s.Running {
			running[s.ID] = true
		}
	}
	unique := base
	for n := 2; running[unique]; n++ {
		unique = fmt.Sprintf("%s-%d", base, n)
	}
	return unique
}

// trySessions is fetchSessions without the exit-on-failure: used where a
// missing session list only costs a better default, not correctness.
func trySessions() []proto.SessionInfo {
	sessions, err := dc().ListSessions()
	if err != nil {
		return nil
	}
	return sessions
}

// resolveSurfaceID picks which surface a command like `wmux connect`
// acts on: the argument if given, otherwise the only running surface.
func resolveSurfaceID(cmdName string, args []string) string {
	return resolveTargetID(cmdName, "surface", args, func(s proto.SessionInfo) bool {
		return s.Running && s.Surface
	})
}

// resolveRunningID is resolveSurfaceID over every running session, not
// just surfaces — `wmux close` can end any of them.
func resolveRunningID(cmdName string, args []string) string {
	return resolveTargetID(cmdName, "session", args, func(s proto.SessionInfo) bool {
		return s.Running
	})
}

// resolveTargetID implements "the argument, or the only candidate".
// Refusing to guess between several is deliberate: these commands detach
// or kill something, and picking the wrong one is not recoverable by
// running the command again.
func resolveTargetID(cmdName, noun string, args []string, match func(proto.SessionInfo) bool) string {
	if len(args) > 0 && args[0] != "" {
		return args[0]
	}
	var live []string
	for _, s := range trySessions() {
		if match(s) {
			live = append(live, s.ID)
		}
	}
	switch len(live) {
	case 1:
		return live[0]
	case 0:
		fmt.Fprintf(os.Stderr, "wmux %s: no running %s to act on\n", cmdName, noun)
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "wmux %s: several %ss are running — name one: %s\n",
			cmdName, noun, strings.Join(live, ", "))
		os.Exit(2)
	}
	return ""
}

// defaultShellCommand is what a pane runs when no command was named, so
// that `wmux surface` on its own opens a usable shell instead of failing.
func defaultShellCommand(native bool) string {
	if !native {
		// WSL panes get the distro's login shell, whatever it is.
		return "$SHELL -l"
	}
	if runtime.GOOS == "windows" {
		if sh := os.Getenv("COMSPEC"); sh != "" {
			return sh
		}
		return "powershell.exe"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

// resolveCmd expands a --cmd value of "-" by reading the command from
// stdin. Between the caller's shell and the daemon a command can cross
// Git Bash, PowerShell, wsl.exe, and JSON — each mangles quoting and
// metacharacters ($(), quotes, semicolons) differently; stdin passes the
// bytes through untouched.
func resolveCmd(cmd string) string {
	if cmd != "-" {
		return cmd
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux: could not read --cmd from stdin: %v\n", err)
		os.Exit(1)
	}
	c := strings.TrimSpace(string(b))
	if c == "" {
		fmt.Fprintln(os.Stderr, "wmux: --cmd - given but stdin was empty")
		os.Exit(1)
	}
	return c
}

// multiFlag collects a repeatable string flag's values in order.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, " ") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}
