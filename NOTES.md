
# wmux — development notes / handoff summary

Context for picking this up in another session (e.g. Claude Code). Written
after the initial build-and-test pass; see git history / README.md for
anything that's changed since.

## What this is

A Windows-side notification/session daemon for AI coding agents (Claude
Code, Codex), inspired by [cmux](https://github.com/manaflow-ai/cmux) (a
Ghostty-based macOS terminal for agents). cmux's actual novel contribution
isn't the terminal chrome (that's libghostty, macOS-only) — it's the
notification routing and sidebar metadata layer. `wmux` ports just that
layer, riding on top of Windows Terminal + WSL2 instead of rebuilding a
terminal renderer from scratch.

Two binaries, one Go module:

- **`wmuxd`** — background daemon. Owns/tracks sessions, watches for OSC
  9/99/777 notify sequences, polls git branch + listening ports, serves a
  local HTTP+SSE API on `127.0.0.1:47823`.
- **`wmux`** — CLI. Talks to the daemon over HTTP. Subcommands: `new`,
  `attach`, `pane`, `list`, `watch`, `notify`, `hook-claude`, `hook-codex`.

## Architecture decisions and why

**Two session modes, not one:**

- `wmux new` — headless. Daemon owns the child process via `os/exec`,
  pipes its stdout, scans for OSC sequences. No TTY at all — fine for
  batch/background runs, **breaks interactive agents** (no readline,
  colors, or stdin).
- `wmux attach` — interactive. Runs the command with full
  stdin/stdout/stderr passthrough (inherits whatever real TTY `wmux`
  itself is running in), and only *registers* metadata with the daemon
  rather than the daemon owning the process. This is what should run
  inside a real terminal pane.

**`wmux pane`** shells out to `wt.exe -w 0 new-tab`/`split-pane`, which
runs `wsl.exe -d <distro> -- bash -lc "wmux attach ..."` inside the new
pane. `wmux pane` itself never talks to the daemon — it's a pure "build
and launch a wt.exe command" utility, meant to run from PowerShell.

**Deployment topology:** recommend running `wmuxd`/`wmux` from the
**Linux build**, resident *inside* the WSL2 distro where agents actually
run — not the Windows-native build shelling into WSL via `wsl.exe` for
every operation. Rationale: Claude Code/Codex hooks fire from inside WSL,
and reaching a Windows-hosted daemon's port from inside WSL depends on
WSL2's mirrored networking mode being active, which isn't guaranteed.
Same-namespace `127.0.0.1` sidesteps the question entirely. The
Windows-native build is still useful for orchestration from PowerShell
(`wmux pane`, `wmux new --distro ...` via `wsl.exe`).

**Hook wire formats differ per agent** (checked against current docs, not
assumed from training data):

- Claude Code: `Notification` hook in `~/.claude/settings.json`, command
  type, payload arrives on **stdin** as JSON (`session_id`, `cwd`,
  `message`). → `wmux hook-claude`.
- Codex CLI: simpler `notify` key in `config.toml`, an argv array — Codex
  appends **one JSON string as the final CLI argument**, not stdin
  (`type`, `last-assistant-message`). Codex's newer `hooks.json` framework
  is explicitly **not available on Windows** yet, so `notify` is the
  right integration point there. → `wmux hook-codex`.

## Bugs found during testing (all fixed, all verified)

1. **Notify buffered until session exit.** Original `watchOutput` used
   `bufio.Scanner` (line-based). An OSC sequence with no trailing newline
   sat in the buffer until the *next* newline arrived — which, in the
   worst case, was the process's own exit output. Fixed by switching to
   raw byte-stream scanning that matches on the regex against a rolling
   buffer after every `Read()`, independent of newlines. Verified: notify
   now fires at the actual moment the OSC sequence is written, confirmed
   against real timing on the user's Windows/WSL machine (notify and exit
   logged 8 seconds apart, matching an intentional `sleep 8`).
2. **CLI silently swallowed HTTP errors.** `wmux new` decoded the response
   body into a struct without checking the status code or the decode
   error, so any daemon-side failure printed as blank fields
   (`spawned session  (cwd=)`) instead of a real error. Fixed: checks
   status code, reads and surfaces the raw body on any non-2xx response
   or decode failure.
3. **`/notify` HTTP endpoint never stamped event time.** Client-constructed
   `NotifyEvent` structs left `Time` as the zero value; the server
   published them as-is. Watch output showed `00:00:00` for hook-driven
   notifications. Fixed: server always sets `evt.Time = time.Now()`
   itself, ignoring whatever the client sent.
4. **`os.Exit()` skips deferred functions.** `wmux attach` originally
   deregistered via `defer`, which silently never ran whenever the
   wrapped command exited non-zero (that path called `os.Exit(code)`
   directly, bypassing all defers). Caught via an explicit `exit 7` test.
   Fixed: deregister explicitly before checking the run error, on every
   exit path, rather than via `defer`.
5. **Session IDs could never be reused.** `Spawn`/`Register` rejected on
   ID existence alone, forever — even after the session had already
   exited cleanly. Would have made restarting an agent under the same
   session ID permanently impossible after its first run. Fixed: only
   reject if the existing entry is genuinely still `running`; otherwise
   replace it.

## What's tested vs. not

**Tested on a real Windows 11 + WSL2 (archlinux distro) machine (2026-07-11):**

- Full daemon HTTP+SSE loop on native Windows: spawn, notify detection and
  timing (confirmed async return from `wmux new`, notify landing ~5s later
  matching an intentional delay), `list`, listening-port polling (via `ss`
  inside WSL)
- `hook-claude` (stdin JSON) and `hook-codex` (argv JSON, including
  correctly ignoring non-`agent-turn-complete` event types) against a
  WSL-resident daemon, invoked from inside WSL
- `wmux pane` end-to-end: PowerShell → `wt.exe -w 0 new-tab`/`split-pane`
  → `wsl.exe -d <distro>` → `bash -lc` → `wmux attach` → daemon
  register/deregister, confirmed via session state and file-based proof
  of execution inside the pane
- WSL2 networking topology: confirmed **WSL → Windows via `127.0.0.1`
  does NOT work** on this machine (no `.wslconfig`, mirrored mode is off
  by default) — a hook running inside WSL cannot reach a Windows-native
  `wmuxd.exe`. Confirmed **Windows → WSL via `127.0.0.1` does work**
  (WSL2's built-in localhost-forwarding, unrelated to mirrored mode).
  This validates the README's recommendation to run `wmuxd`/`wmux` from
  the Linux build resident inside the distro.

**Bug found and fixed during this pass:**

- **`wmux pane`'s quoting chain broke on compound `--cmd` values.**
  Two independent problems, both in `cmd/wmux/main.go` `cmdPane`:
  1. `wt.exe` re-tokenizes its own trailing commandline and splits on any
     unescaped `;`, even one embedded deep inside an already-quoted
     argv token from `wsl.exe -d ... -- bash -lc "<...>;<...>"` — so any
     `--cmd` containing a compound/multi-statement shell command silently
     truncated everything after the first `;` before it ever reached
     `wsl.exe`.
  2. The initial fix attempt (base64-encode the inner command, decode via
     `eval "$(echo B64 | base64 -d)"`) avoided the semicolon problem but
     introduced a new one: a token containing literal `"` characters gets
     mangled somewhere between Go's argv escaping and `wt.exe`'s own
     parser (verified empirically — the exact same payload runs fine via
     direct `wsl.exe`/`bash -lc`, only breaks once `wt.exe` is in the
     chain), even when reproduced via array-splatted PowerShell args
     bypassing Go entirely.
  - Fixed by avoiding embedded quote characters entirely: base64-encode
    the inner command and pipe it through decode+exec with no quoting at
    all — `echo <b64>|base64 -d|bash`. Verified working for both a
    semicolon-heavy compound command and the common single-command case
    (`claude`, `codex`, etc.).

**Follow-up fix (2026-07-11):** `--distro` used to default to the
hardcoded string `"Ubuntu"` whenever omitted, in `buildCommand`,
`gitBranch`, `listeningPorts` (`internal/daemon/session.go`) and
`cmdPane` (`cmd/wmux/main.go`) — which is exactly what caused `test1`/
`test2`/`test3` above to fail silently, since this machine's actual
distro is `archlinux`. Fixed by omitting `-d <name>` from the `wsl.exe`
invocation entirely when `--distro` is empty, letting `wsl.exe` fall back
to the user's real configured default distro instead of a guessed name.
Verified: `wmux new` with no `--distro` flag now correctly reaches
`archlinux` and completes the full spawn → notify → exit cycle.

**Still not tested:**

- Real Claude Code / Codex hook wiring end-to-end with a live agent
  actually invoking the hook (only tested with hand-constructed payloads
  matching documented formats)
- Session persistence / restart behavior beyond a single daemon lifetime

## Not yet built (see README "Next steps" for the live list)

- Tray/sidebar UI (Wails or Tauri, subscribing to `GET /events` SSE and
  `GET /sessions`)
- Port scoping (`listeningPorts` currently lists all system-wide listening
  ports, not scoped to the session's process tree)
- Session persistence across daemon restarts (cmux snapshots to disk for
  this; wmuxd currently loses all state on restart)

## Project layout

```
wmux/
├── go.mod                       module github.com/peterkure/wmux
├── README.md                    setup, hook-wiring, next steps (source of truth, keep in sync)
├── cmd/
│   ├── wmuxd/main.go             daemon entrypoint
│   └── wmux/
│       ├── main.go               all CLI subcommands
│       └── flags.go              flag.FlagSet helper
├── internal/
│   ├── daemon/
│   │   ├── session.go            Spawn/Register/Deregister, OSC watcher, git/port polling
│   │   └── server.go             HTTP + SSE routes
│   └── proto/proto.go            shared wire types
└── bin/                          prebuilt binaries (windows-amd64, linux-amd64)
```

`README.md` is the more detailed, user-facing reference (exact commands,
config snippets for `~/.claude/settings.json` and `~/.codex/config.toml`);
this file is oriented toward "what happened and why," for picking the
project back up with full context.
