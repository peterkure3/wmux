// The generic agent-hook handler behind `wmux hook run <agent>`.
//
// One handler, driven by declarative profiles (internal/agentprofile),
// replaces the old per-agent subcommands: hook-claude and hook-codex are
// now thin aliases for `wmux hook run claude` / `wmux hook run codex`, so
// existing hook configs keep working unchanged. Onboarding a new agent
// whose hooks speak stdin-JSON or argv-JSON means writing a small TOML
// profile, not new Go code — several newer CLIs (Kimi, Kiro) copied
// Claude Code's hook shape outright and work through the claude-shaped
// profiles bundled with the binary.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/peterkure/wmux/internal/agentprofile"
)

// cmdHook dispatches `wmux hook run <agent>` and `wmux hook list`.
func cmdHook(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "wmux hook: expected 'run <agent>' or 'list'")
		os.Exit(1)
	}
	switch args[0] {
	case "run":
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			fmt.Fprintln(os.Stderr, "wmux hook run: missing <agent> (try 'wmux hook list')")
			os.Exit(1)
		}
		runHook("hook run "+args[1], args[1], args[2:])
	case "list":
		for _, name := range agentprofile.List() {
			fmt.Println(name)
		}
	default:
		fmt.Fprintf(os.Stderr, "wmux hook: unknown subcommand %q (expected 'run' or 'list')\n", args[0])
		os.Exit(1)
	}
}

// hookDecision is the pure outcome of applying a profile to a payload:
// what to notify about, or that nothing should be sent (filtered event,
// empty message with no default).
type hookDecision struct {
	Session string
	Body    string
	Notify  bool
}

// evalHook applies a profile's field mappings and filters to a raw JSON
// payload. getwd is injected for testability; it is only consulted when
// the profile's session_fallback is "getwd".
func evalHook(p *agentprofile.Profile, raw []byte, flagSession string, getwd func() (string, error)) (hookDecision, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return hookDecision{}, err
	}

	if !p.EventAllowed(agentprofile.Extract(payload, p.EventField)) {
		return hookDecision{}, nil
	}

	session := flagSession
	if session == "" {
		session = agentprofile.Extract(payload, p.SessionField)
	}
	if session == "" {
		session = agentprofile.Extract(payload, p.CwdField)
	}
	if session == "" && p.SessionFallback == "getwd" {
		if cwd, err := getwd(); err == nil {
			session = cwd
		}
	}

	body := agentprofile.Extract(payload, p.MessageField)
	if body == "" {
		if p.DefaultMessage == "" {
			return hookDecision{}, nil // nothing to notify about
		}
		body = p.DefaultMessage
	}

	return hookDecision{Session: session, Body: body, Notify: true}, nil
}

// runHook is the shared implementation behind `wmux hook run <agent>` and
// the hook-claude/hook-codex aliases. cmdName only prefixes diagnostics so
// each entry point reports under its own name.
//
// Exit-code contract (kept bit-identical to the old per-agent commands):
//   - unknown profile or missing argv payload: exit 1, forward never runs
//   - --forward runs before parsing and unconditionally — the original
//     handler must fire even when the payload is garbage or wmuxd is down;
//     its exit code is propagated
//   - parse error: exit max(forwardExit, 1)
//   - filtered event or empty message with no default: exit forwardExit
//     (0 without --forward) with no notify
//   - with --forward the wmux notify is best-effort (warning only);
//     without it a notify failure exits 1
func runHook(cmdName, agent string, args []string) {
	fs := newFlagSet(cmdName)
	session := fs.String("session", "", "session ID override (profile decides the fallback)")
	logFile := fs.String("log", "", "append every raw payload to this file (debugging aid for verifying what an agent actually sends)")
	var forward multiFlag
	fs.Var(&forward, "forward", "argv token of a pre-existing notify handler to chain (repeat once per token; payload is passed through)")
	fs.Parse(args)

	p, err := agentprofile.Load(agent)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux %s: %v\n", cmdName, err)
		os.Exit(1)
	}

	var raw []byte
	var payloadWord string // wording for parse-error diagnostics, per wire
	switch p.Wire {
	case agentprofile.WireArgvJSON:
		if fs.NArg() < 1 {
			fmt.Fprintf(os.Stderr, "wmux %s: missing JSON payload argument\n", cmdName)
			os.Exit(1)
		}
		raw = []byte(fs.Arg(0))
		payloadWord = "payload argument"
	case agentprofile.WireStdinJSON:
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wmux %s: could not read stdin: %v\n", cmdName, err)
			os.Exit(1)
		}
		raw = b
		payloadWord = "stdin payload"
	}

	// Log the raw payload before any parsing or filtering — the whole
	// point is capturing what the agent really sent when the profile's
	// assumptions turn out wrong (an unexpected event type is filtered
	// silently, so without this there is nothing to debug from). Strictly
	// best-effort: a full disk or bad path must never break the hook chain.
	if *logFile != "" {
		if f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			fmt.Fprintf(f, "%s %s %s\n", time.Now().Format(time.RFC3339), agent, raw)
			f.Close()
		}
	}

	// Forward before anything else — the original handler must fire even
	// if the payload doesn't parse or wmuxd is down. argv-json agents
	// (Codex) append the payload as the final argument, exactly as the
	// agent itself would have invoked the handler; stdin-json agents get
	// it piped to the child's stdin for the same reason.
	forwardExit := 0
	if len(forward) > 0 {
		var fcmd *exec.Cmd
		if p.Wire == agentprofile.WireArgvJSON {
			fcmd = exec.Command(forward[0], append(append([]string{}, forward[1:]...), string(raw))...)
		} else {
			fcmd = exec.Command(forward[0], forward[1:]...)
			fcmd.Stdin = strings.NewReader(string(raw))
		}
		fcmd.Stdout = os.Stdout
		fcmd.Stderr = os.Stderr
		if err := fcmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				forwardExit = exitErr.ExitCode()
			} else {
				fmt.Fprintf(os.Stderr, "wmux %s: forward %q failed: %v\n", cmdName, forward[0], err)
				forwardExit = 1
			}
		}
	}

	dec, err := evalHook(p, raw, *session, os.Getwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wmux %s: could not parse %s: %v\n", cmdName, payloadWord, err)
		os.Exit(max(forwardExit, 1))
	}
	if !dec.Notify {
		os.Exit(forwardExit)
	}

	if len(forward) > 0 {
		// Best-effort when chaining: an unreachable wmuxd must not turn a
		// successfully forwarded event into a nonzero exit for the agent.
		if err := pushNotifyErr(dec.Session, dec.Body); err != nil {
			fmt.Fprintf(os.Stderr, "wmux %s: warning: %v\n", cmdName, err)
		}
		os.Exit(forwardExit)
	}
	pushNotify(dec.Session, dec.Body, cmdName)
}
