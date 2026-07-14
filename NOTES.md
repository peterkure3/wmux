
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

**Deployment topology — as originally recommended vs. what's actually
running.** This section originally recommended running `wmuxd`/`wmux`
from the **Linux build**, resident *inside* the WSL2 distro where agents
actually run — not the Windows-native build shelling into WSL via
`wsl.exe` for every operation. Rationale: Claude Code/Codex hooks fire
from inside WSL, and reaching a Windows-hosted daemon's port from inside
WSL depends on WSL2's mirrored networking mode being active, which isn't
guaranteed. Same-namespace `127.0.0.1` sidesteps the question entirely.

That recommendation was never adopted. The real deployment (verified
2026-07-14, `C:\wmux\wmuxd.exe` + `wmux.exe`, both Windows-native builds)
took the other fork instead: `claude.exe` itself turned out to be a
**native Windows install** on this machine (see the `--native` follow-up
below), so every session actually run through this daemon is
`wmux pane --native`/`wmux attach --native` — same OS namespace as
`wmuxd`, no WSL hop, no loopback question to answer. Confirmed via
`state.json`: all 13 sessions on record have `"native": true,
"distro": ""`; zero WSL-targeted sessions have ever run against this
daemon. `wmuxd` has no autostart entry (no Task Scheduler task, no
Startup-folder shortcut) — it's started manually or via `wmux update`'s
restart, per session.

Practical upshot: the WSL→Windows loopback failure described below under
"What's tested vs. not" is real in the code but currently a **latent, not
live**, issue — plain (non-`--native`) `wmux pane`/`wmux attach` sessions
would hit it the moment someone runs one against this Windows-native
daemon, since the inner `wmux attach` execing inside WSL has no route
back to `127.0.0.1:47823` on the host. Nothing currently exercises that
path. If a WSL-targeted (non-native) agent enters the picture, either
adopt the original Linux-build-resident recommendation above, or give the
Windows-native daemon a WSL-reachable bind address instead of pure
`127.0.0.1`.

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
  the Linux build resident inside the distro — **a recommendation the
  actual deployment doesn't follow**; see "Deployment topology" above for
  how that fork was resolved differently in practice, and why it's fine
  for now.

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

**Follow-up feature (2026-07-11): `wmux pane --native`.** The real machine
this was tested on turned out to run `claude.exe` as a **native Windows
install**, not inside WSL at all (`where claude` → a `.exe` path), even
though a WSL distro (archlinux) is present on the machine for other
things — confirms the skill's Step 0 advice to check rather than assume.
Plain `wmux pane` can't launch a native binary (always routes through
`wsl.exe`), so added `--native`: same `wt.exe` pane, but runs
`wmux attach` via `powershell.exe -EncodedCommand` (UTF-16LE base64,
PowerShell's own documented single-opaque-token mechanism) instead of
`wsl.exe -d ... -- bash -lc`, for the same reason the WSL path avoids raw
quoting through `wt.exe` — verified working, including with a semicolon
in `--cmd` to stress-test it the same way as the WSL fix.

Also verified: real Claude Code hook wiring against a live `claude.exe`
install (`~/.claude/settings.json` → `Notification` hook →
`C:\wmux\wmux.exe hook-claude`, fired correctly) and `wmux attach`
wrapping a real native process end to end (register → run → exit-code
propagation → deregister).

**PowerShell 5.1 quoting gotcha found while testing `--native`:** passing
`--cmd` a value containing embedded literal `"` characters (e.g.
`'"C:\Users\Peter Kure\...\claude.exe" --version'`, needed because the
path contains a space) silently corrupted argument parsing — specifically
it ate the trailing `--split h` flag, which silently fell back to the
`tab` default. Reproduced with both an inline string and a variable
assignment, so it's not a quoting-style issue on the calling side — this
is PowerShell 5.1's documented legacy native-argv marshalling being
unreliable around embedded double quotes (predates the `$PSNativeCommand
ArgumentPassing='Standard'` fix in PS 7.3+). Not fixable from the Go
binary's side since the corruption happens before wmux.exe ever sees the
arguments. Workaround: use the target's 8.3 short path
(`(New-Object -ComObject Scripting.FileSystemObject).GetFile(...).ShortPath`)
to avoid needing embedded quotes at all — verified this sidesteps it
completely.

**Follow-up fix (2026-07-11): `--split v`/`h` renamed to `right`/`down`.**
User reported "we have horizontal pane splitting but no vertical pane
splitting" after using `--split v`. Both directions were actually already
implemented identically in `cmdPane` — verified by screenshotting real
`wt.exe split-pane -V` and `-H` invocations directly: `-V`
(`--vertical`) produces a **left/right** layout, `-H` (`--horizontal`)
produces **top/bottom**. `wt.exe` names a split after the orientation of
the *dividing line*, which is backwards from the much more common
intuition (used by e.g. tmux, VS Code) that "vertical split" means panes
stacked vertically (top/bottom) and "horizontal split" means panes
side-by-side. The user's `--split v` was working exactly as coded
(left/right) but not as expected. Fixed by renaming the flag values from
`v`/`h` to unambiguous `right`/`down` (`wtArgs` still emits `-V`/`-H`
under the hood, unchanged) — removes the ambiguity entirely rather than
just documenting it. Updated `README.md`, `MANUAL.md`, `MANUAL.tex`, and
`SKILL.md` to match, plus the reasoning note about `wt.exe`'s own naming.

**Follow-up feature (2026-07-11): `wmux close`.** After the split-naming
fix, tested whether `wmux pane`-opened panes could actually be closed
(not just have their agent process exit) — found they can't, by design:
a `wt.exe` pane whose hosted process exits is left as an inert,
already-closed pane in the layout (confirmed via screenshot even for a
clean zero exit code, ruling out an exit-code/timing explanation), and
`wt.exe` has no documented command-line API to remove an existing pane
from outside. Deliberately did **not** attempt to fake this via UI
automation or by broadly killing `wt.exe`/`conhost.exe`/`OpenConsole.exe`
processes — a machine accumulates many of these across unrelated
windows/tabs including ones actively in use, and there's no reliable way
to distinguish "the test pane" from "the user's real terminal" by image
name alone (this session had ended up with dozens of leftover
conhost/OpenConsole processes just from testing).

What *is* now closable: the session's actual tracked process, via a new
`wmux close --id ID` command. Required plumbing the real PID through to
the daemon: `RegisterSessionRequest` gained a `PID` field (Register
proper for the mux/HTTP wire type; `Session` gained a `pid` field
populated either from the daemon's own `cmd.Process.Pid` for `wmux new`,
or from the client-supplied PID for `wmux attach`/`wmux pane`). This
required restructuring `cmdAttach` in `cmd/wmux/main.go` to call
`cmd.Start()` before registering (to have a real PID to send) rather
than the previous single `cmd.Run()` call — registration now happens
between `Start()` and `Wait()`. New `Daemon.Close(id)` in
`internal/daemon/session.go` looks up the tracked PID and calls
`os.FindProcess(pid).Kill()`; new `POST /sessions/close` route in
`server.go`; new `wmux close --id ID` CLI command. Verified end-to-end
for both session types — confirmed via actual process-list checks (not
just daemon state) that the real OS process dies: for a `wmux attach`
session, watched the exact process count drop by one; for a `wmux new`
(WSL, daemon-owned) session, confirmed via `pgrep` inside the distro
that the `sleep 60` process was actually gone after `wmux close`, not
just marked exited in `wmux list`. Also verified closing an
already-exited or nonexistent session ID returns a clean 404, not a
crash or hang.

**Follow-up features (2026-07-11): port scoping + session persistence.**
Both from README's original "Next steps" list, implemented together
since port scoping needed the same "is this session native or WSL"
tracking persistence's restore path also benefits from.

- **Native session tracking.** Found a real latent bug while designing
  port scoping: `gitBranch`/`listeningPorts` branched purely on the
  *daemon's own* `runtime.GOOS`, never on whether the specific session
  being polled was itself native-Windows or WSL-targeted. A Windows-native
  daemon therefore always shelled into WSL for metadata polling — correct
  for `wmux new` (always WSL-targeted by design) and for `wmux attach`
  sessions actually run inside WSL, but silently wrong for a *native*
  `wmux attach`/`wmux pane --native` session, whose `cwd` is a real
  Windows path that means nothing inside a WSL distro. Fixed by adding
  `Native bool` to `RegisterSessionRequest`, set automatically by `wmux
  attach` from its own `runtime.GOOS` (no new user-facing flag — nativity
  is inherent to which `wmux` binary is running, not something to ask the
  user to specify). `Spawn`-mode sessions never set it (defaults `false`),
  correctly preserving the always-WSL-targeted `wmux new` behavior.
  Verified: a native session's git branch against a real Windows path
  (`D:\Github\wmux`) now resolves correctly (`main`) via native `git.exe`,
  where it silently failed before.

- **Port scoping.** New `internal/daemon/portscope.go`: `processTree(pid)`
  walks the real process tree in the daemon's own namespace (`ps -eo
  pid,ppid` + BFS on Unix/WSL, `Get-CimInstance Win32_Process` +BFS on
  native Windows), and `listeningPortsForTree` cross-references it against
  the platform's port→PID data (`ss -ltnp` / `Get-NetTCPConnection
  -OwningProcess`). Only attempted when the session's PID is meaningful in
  the daemon's own namespace (`runsDirectly`: true for any session on a
  WSL-resident/Linux daemon, and for *native* sessions on a Windows-native
  daemon) — a WSL-targeted `wmux new`/plain `wmux pane` session on a
  Windows-native daemon has a `pid` that's the Windows-side `wsl.exe`
  frontend process, which has zero correlation to PIDs inside the WSL
  distro's own `/proc`, so scoping is skipped entirely for that specific
  combination and it falls back to the old system-wide `ss -ltn` listing
  rather than either crashing or silently showing nothing.

  Verified on both platforms with a listener bound to one specific,
  distinctive port (18342 native Windows via `TcpListener`, 28471 WSL via
  `python3 -m http.server`): `wmux list` showed exactly that one port in
  both cases, not the 5-7 system ports (`53`, `5355`, the daemon's own
  `47823`, etc.) the old unscoped code always included. Hit one real
  debugging trap while verifying this: after rebuilding, forgot to
  reinstall the updated Linux binary to `/usr/local/bin` inside WSL before
  testing, so the first WSL test ran against a stale build and appeared to
  show the bug still present (full unscoped port list) — always diff the
  installed binary's hash against the fresh build before trusting a
  failed verification.

- **Session persistence.** New `internal/daemon/persist.go`. `Daemon.New`
  now takes a `statePath` (default `~/.wmux/state.json` via
  `DefaultStatePath()`, overridable with `wmuxd --state`, empty disables
  it); `save()` snapshots all sessions to disk (write-to-temp +rename, so
  a crash mid-write can't corrupt the file) after every lifecycle
  transition and once per `pollMetadata` tick; `load()` restores sessions
  at startup and re-checks each one's PID for actual liveness via a new
  `processAlive` helper (`tasklist`/`ps -p`) rather than trusting the
  persisted `running` flag blindly. Verified all three real scenarios:
  (1) daemon killed and restarted while the tracked process is still
  alive — session restores as `running: true`, branch/port polling
  resumes; (2) tracked process killed *independently* of the daemon
  (bypassing `wmux close`, so the on-disk snapshot still says `running:
  true`) before a restart — session correctly restores as `running:
  false` via the liveness re-check, not left incorrectly `running`;
  (3) normal `wmux close` then restart — restores as `exited`, as
  expected. A restored Spawn-mode session loses OSC notify parsing (the
  daemon no longer holds its original stdout pipe after a restart) and
  clean `cmd.Wait()` reaping (Go can only `Wait()` a process it actually
  `Start()`ed itself) — `pollMetadata`'s per-tick liveness check is what
  eventually notices such a session exited, in place of those.

**Follow-up features (2026-07-11): full pane close + `wmux focus`.**
Prompted by the goal "switch focus that the agent can use + close the
pane completely." Key discovery that unlocked pane close, found by
empirical testing (window-count probe with `wt -w -1`, since WT is
single-process and process counting tells you nothing): **a wt.exe pane
only honors its profile's `closeOnExit` setting when it runs the
profile's own commandline.** Any commandline passed on the wt.exe command
line leaves the inert dead pane previously documented as unfixable — on
any exit code, regardless of `closeOnExit`. So the fix is to never pass a
commandline through wt.exe at all:

- `wmux pane` now auto-installs a WT **settings fragment**
  (`%LOCALAPPDATA%\Microsoft\Windows Terminal\Fragments\wmux\wmux.json`,
  never touches the user's settings.json content) defining a `wmux`
  profile: `closeOnExit: "always"`, `suppressApplicationTitle: true`,
  fixed commandline `<wmux.exe> pane-exec`. A running WT imports a new
  fragment during any settings reload — touching settings.json's mtime
  triggers one (verified live on WT 1.24, no restart needed; `wmux pane`
  does this automatically after writing the fragment).
- `wmux pane` files the session spec (id/cwd/cmd/distro/native) with the
  daemon (`POST /panes/pending`), then opens the pane with
  `--profile wmux --title <id>` and **no commandline**.
- `wmux pane-exec` (the profile's commandline, runs inside the new pane)
  reads its own console title — wt.exe's `--title` sets it, verified —
  claims the spec (`POST /panes/claim`, retries briefly, specs expire
  after 2 min so a stale one can't start an agent much later), and runs
  the exact same inner `wmux attach` commands `wmux pane` used to hand
  to wt.exe (powershell `-EncodedCommand` for native, base64|bash pipe
  for WSL). Title is the only id channel wt.exe leaves us, and it's
  race-free (concurrent `wmux pane` calls each claim their own id).
- Net effect: agent exits or `wmux close` kills it → process chain
  unwinds → pane removes itself from the layout. Verified end-to-end
  twice (ping session, split right: registered → `wmux close` → process
  gone, pane gone).

`wmux focus` — two modes, both verified:

- `--id ID`: UI Automation (via embedded PowerShell): find the element
  named ID under each `CASCADIA_HOSTING_WINDOW_CLASS` top-level window,
  `SetForegroundWindow`, select its TabItem, `SetFocus()` its
  TermControl. Verified keyboard focus lands on the exact TermControl
  (checked `AutomationElement.FocusedElement` after), including one half
  of a split. This is why panes keep `--suppressApplicationTitle`: the
  title IS the session id, for the pane's whole lifetime.
- `--dir left|right|up|down`: plain `wt -w 0 move-focus <dir>` —
  relative movement in the most recently used window (which, for an
  agent calling it from inside a pane, is its own window).
- Gotcha found while testing: powershell.exe emits CLIXML progress noise
  on **stderr** ("Preparing modules for first use") — `wmux focus` must
  read the script's ok/not-found verdict from stdout only, not
  CombinedOutput.

Also relevant: cmux's own pane architecture (socket API `pane.*`
`surface.*` commands over a bonsplit split-tree) was reviewed for
comparison — wmux's wt.exe-sidecar approach can't target/resize/inspect
arbitrary panes (that ceiling needs owning ConPTY), but self-closing
panes + focus-by-id cover the agent-workflow essentials.

**Still not tested:**

- Real Codex hook wiring end-to-end with a live Codex invocation (Codex
  isn't installed anywhere on this machine, native or WSL — only tested
  with a hand-constructed payload matching the documented format)
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
