# wmux

A cmux-equivalent notification/session daemon for AI agent workflows.
`wmuxd` spawns and watches agent sessions (Claude Code, Codex, etc.) for
OSC 9/99/777 notification escape sequences, tracks git branch and listening
ports per session, and serves it all over a local HTTP API. `wmux` is the
CLI you wire into agent hooks and use to inspect state.

Running `wmux` with no arguments opens the multiplexer: a full-screen
multi-pane TUI over daemon-owned sessions, with the live session sidebar
as one of its panes. It draws the panes itself — no Windows Terminal
splits involved — so the same binary behaves the same on Windows, in
WSL, and on Linux.

Status: daemon + CLI + TUI are working end-to-end, verified on a real
Windows 11 + WSL2 machine (spawn → OSC-9 parse → live SSE push →
`list`/`watch` output, both hook commands, and the TUI's attach/render/
input path), and separately verified on native Fedora Linux + KDE Plasma
(systemd-managed daemon, TUI rendering under KWin/Wayland). See
`docs/sidebar-design.md` for the sidebar's design.

**Note:** `--distro` only matters when `wmux` itself runs on Windows and
the session is meant to run inside WSL — it's optional there too: if
omitted, `wsl.exe` uses your system's actual default distro (`wsl.exe
--status`), same as running `wsl.exe` with no `-d` yourself. Pass
`--distro <name>` explicitly only if you want a *non-default* distro
(check names with `wsl -l -v`). On native Linux or native Windows, there's
no WSL involved and this flag is a no-op.

## Layout

```
cmd/wmuxd/        daemon entrypoint
cmd/wmux/         CLI entrypoint (a three-line shim over internal/cli)
internal/cli/     every wmux subcommand, and the kong command tree
internal/tui/     the multiplexer: model, key routing, mouse, panes, sidebar, themes
internal/layout/  the split/grid tree — pure geometry, no I/O
internal/client/  daemon HTTP client shared by the CLI and the TUI
internal/daemon/  session management, OSC watcher, git/port polling, HTTP+SSE server, panic recovery
internal/agentprofile/ per-agent hook profiles (TOML) + launch commands
internal/wmuxlog/ structured logger (log/slog, JSON + rotation) — see docs/logger-design.md
internal/proto/   shared wire types
bin/              prebuilt binaries (windows-amd64, linux-amd64)
install/          Windows installer script (install.ps1/uninstall.ps1)
```

The dependency direction is one-way: `cli` → `tui` → `layout`, and both
`cli` and `tui` talk to the daemon only through `client`. Nothing imports
`cli`, so the TUI and the layout engine are testable without a terminal
or a process.

## Authentication

wmuxd's HTTP API can execute arbitrary commands (`POST /sessions` runs its
`command` field), so binding to `127.0.0.1` is not on its own sufficient —
loopback is reachable by every process on the machine, and by any web page
you visit, since a cross-origin POST with a simple `Content-Type` is sent
without a CORS preflight.

Two mechanisms close that:

- **Browser rejection.** Any request carrying `Origin` or a cross-site
  `Sec-Fetch-Site` is refused with 403. Those headers are set by the
  browser and cannot be forged by page JavaScript.
- **A shared token.** `wmuxd` generates `~/.wmux/token` (0600) on first
  start; `wmux` reads it and sends it as `X-Wmux-Token`. Every route except
  `GET /healthz` requires it, including `/shutdown` and `/debug/pprof/*`.

Nothing to configure — both binaries find the file on their own. Two notes:

- If you talk to the API by hand, send the header:
  `curl -H "X-Wmux-Token: $(cat ~/.wmux/token)" http://127.0.0.1:47823/sessions`
- Set `WMUX_TOKEN_FILE` alongside `WMUX_ADDR` when running isolated
  daemons on one machine.

**Upgrading from a pre-token build:** restart `wmuxd` so it provisions the
token. Panes still running the previous `wmux.exe` will get a 401 on their
deregister call and print a warning — harmless, and it goes away when the
pane is reopened.

## Installing (Windows)

No admin rights needed — installs to `%LOCALAPPDATA%\Programs\wmux`,
adds it to your user PATH, registers wmuxd to start at logon:

```powershell
iwr https://raw.githubusercontent.com/peterkure3/wmux/main/install/install.ps1 | iex
```

Open a **new** terminal afterwards, then `wmux version` to confirm.
Uninstall with `install/uninstall.ps1` (`-Purge` also removes
`~/.wmux`'s session state/logs/settings). See MANUAL.md for the manual
(no-script) install path.

## Installing (Linux)

No installer script yet — build from source (Go 1.25+) or grab a
published release archive:

```bash
git clone https://github.com/peterkure3/wmux.git && cd wmux
go build -o bin/wmuxd ./cmd/wmuxd
go build -o bin/wmux  ./cmd/wmux
```

Put `bin/` on your `PATH` (or symlink both binaries into somewhere that
already is), then register `wmuxd` to start at login via a systemd
`--user` unit:

```bash
wmux autostart install    # writes ~/.config/systemd/user/wmux-wmuxd.service,
                           # then `systemctl --user enable --now`s it
wmux autostart status      # systemctl --user status wmux-wmuxd
wmux autostart uninstall
```

This is a genuinely native install — no WSL layer involved. `wmux
version` confirms it's on your PATH; `wmux` with no arguments opens the
multiplexer.

## Running it

On Windows, run `wmuxd.exe` once in the background (add it to Startup or
run it from Task Scheduler — no console needed since it's headless):

```
wmuxd.exe
```

On Linux, `wmux autostart install` (see "Installing (Linux)" above) is
the equivalent — or run `wmuxd` directly in a terminal for a quick check
before wiring up the systemd unit.

**If your agents run inside WSL2** (Windows host, agents inside a distro
— still a common setup): run the Linux build of `wmuxd`/`wmux` (in
`bin/linux-amd64/`) *inside that distro* instead of the Windows build on
the host. See "Wiring real agent hooks" below for why. This is separate
from a genuinely native Linux install (no Windows host involved at all,
e.g. running wmux directly on a Linux desktop) — both are first-class,
they just answer different questions about where your agents actually
run.

### The multiplexer (`wmux`, `wmux grid`)

Plain `wmux` opens it. Panes are daemon-owned sessions; the sidebar is
one of the panes:

```
wmux                      # sidebar + one shell pane, open more with n
wmux claude               # open it with one pane already running claude
wmux grid 4               # four panes in a 2x2 grid
wmux grid 4 --claude      # the same grid, every pane running claude
wmux grid 3 --codex       # 3 panes: two over one, all running codex
```

`wmux grid N` accepts any N from 1 to 16 and arranges them in a balanced
grid (2 side by side, 3 as two-over-one, 4 as a 2x2, 6 as 3x2...). The
agent flags come from the same profiles `wmux hook run` uses —
`--claude`, `--codex`, `--kimi`, `--kiro`, `--mimo`, `--agy` — or use
`--agent NAME`, or `--cmd` for an arbitrary command. With none of them,
the panes are shells.

Inside the multiplexer:

| key | does |
| --- | --- |
| `ctrl+o` | cycle panes |
| click | focus the pane (or sidebar) under the cursor |
| `ctrl+b` | switch to COMMAND mode |

COMMAND mode is **sticky** — unlike tmux's one-shot prefix, one `ctrl+b`
buys a run of commands, and `esc` (or `i`) goes back to typing at the
pane. The mode is shown in the footer.

| COMMAND key | does |
| --- | --- |
| `v` / `\|` | split the focused pane side by side (vertical divider) |
| `h` / `-` / `s` | split the focused pane stacked (horizontal divider) |
| `n` | new pane, same axis as last time |
| `x` | close the focused pane |
| `tab`, arrows / `jkl` | cycle / move focus |
| `b` | show or hide the sidebar |
| `g` | snap every pane into a balanced grid |
| `esc` / `i` | back to INSERT (typing at the pane) |
| `q` | quit |

Splitting hands off to the sidebar's own cwd/command prompt, so `ctrl+b`
`-` then a directory and a command is the whole flow.

### Headless sessions (`wmux new`)

Good for background/batch runs where you don't need to type into the
agent — spawns the process with no TTY, piping its output through the
daemon's OSC watcher:

```
wmux new codex exec ...            # ID defaults to this directory's name
wmux new --id my-project --cwd /home/you/my-project --cmd "codex exec ..."
wmux list
wmux watch
```

Every session-creating command takes the same optional flags, and none of
them are required: `--id` defaults to the working directory's base name
(uniquified against running sessions), `--cwd` defaults to the current
directory, and the command can be given as trailing arguments instead of
`--cmd`. Use the flags when a default is wrong, not routinely.

### Interactive sessions (`wmux attach`)

For anything you actually want to type into — `claude`, `codex`, a normal
interactive session — `wmux new` won't work: it has no TTY, so readline,
colors, and prompts all break, and there's no way to send it input at all.

`wmux attach` runs a command with full TTY passthrough (real
stdin/stdout/stderr) in *this* terminal, while still registering with the
daemon for tracking:

```
wmux attach claude
```

For a pane inside the multiplexer, or a session that outlives its
terminal, use `wmux grid`/`wmux` or `wmux surface` below instead.

### Detachable sessions (`wmux surface` + `wmux connect`)

tmux-style sessions: the daemon owns a real pseudo-terminal (ConPTY) the
agent runs inside, plus a server-side VT screen model, so the session is
fully interactive **and** survives its viewing terminal closing. Close
Windows Terminal entirely — the agent keeps running; reconnect later and
the current screen repaints exactly (a VT replay, not scrollback).

```
wmux surface claude       # spawn it headless, ID named after this directory
wmux connect              # view/control it here (the only running surface)
wmux connect my-project   # ...or name one
```

`Ctrl-]` detaches (the session keeps running); reconnect any time, from
any terminal, with the same `wmux connect`. Several clients can attach at
once. `wmux surface` with no command opens a shell. Pass `--native` to
run the command directly on Windows instead of inside WSL. Surfaces show
up in `wmux list`/the sidebar like any session, their output is watched
for OSC notify sequences like `wmux new` sessions, and they are exactly
what the multiplexer's panes are.

Caveats: a surface dies with the daemon (the ConPTY can't survive a wmuxd
restart — it comes back as `exited`), and `wmux update` restarts wmuxd,
so finish or close surfaces before updating.

`wmux update` has two sources: the default rebuilds from the source repo
(`--repo`/`WMUX_REPO`/the path stamped into the binary), and
`--release latest` (or `--release vX.Y.Z`) downloads a published GitHub
release instead — no Go toolchain or checkout needed. Release archives
are verified against the release's `SHA256SUMS` before install, and a
machine with no source repo configured falls back to `--release latest`
automatically.

### Diagnosing wmuxd itself (`wmux log` / `wmux debug`)

`wmux log` inspects the daemon's structured log (`~/.wmux/wmuxd.log`,
JSON, size-rotated): `wmux log tail [-n N]`, `wmux log level [NAME]`. See
`docs/logger-design.md`.

`wmux debug` is a runtime inspector for `wmuxd` itself — not a
source-level debugger (delve already covers that), but the daemon's own
live state: `wmux debug state` (session table, uptime, goroutine count),
`wmux debug panics` (every panic a built-in recovery layer has caught —
before this existed, one panic anywhere in wmuxd took the whole daemon
down with nothing left to diagnose it), `wmux debug events` (recent
notify/session history), `wmux debug dump` (bundles all of the above plus
a log tail into one file — the one to attach to a bug report), and
`wmux debug pprof cpu|heap|goroutine` (stdlib profiling). See
`docs/debugger-design.md`.

### Closing a session (`wmux close`)

```
wmux close                # the only running session
wmux close my-project     # ...or name one
```

Kills the session's tracked process — the daemon-owned process for
`wmux new`/`wmux surface`, or the registered PID for `wmux attach` (the
daemon learns the real PID at register time). This ends the agent and
deregisters the session (`wmux list` shows `running: false`
immediately). With no ID and several sessions running, it lists them
rather than guessing which one you meant.

Inside the multiplexer, `ctrl+b` `x` does the same thing to the focused
pane and removes it from the layout.

## Building from source

```
go build -o bin/wmuxd.exe ./cmd/wmuxd   # on Windows, or cross-compile:
GOOS=windows GOARCH=amd64 go build -o bin/wmuxd.exe ./cmd/wmuxd
GOOS=windows GOARCH=amd64 go build -o bin/wmux.exe  ./cmd/wmux

go build -o bin/wmuxd ./cmd/wmuxd       # on Linux, or cross-compile:
GOOS=linux GOARCH=amd64 go build -o bin/wmuxd ./cmd/wmuxd
GOOS=linux GOARCH=amd64 go build -o bin/wmux  ./cmd/wmux
```

`wmux update` automates this for an existing install (rebuild from a
configured `--repo`/`WMUX_REPO`, swap the running binaries, restart the
daemon) — or `wmux update --release latest` to install a published
release instead of building locally (works on both platforms; the
release workflow packages Windows as `.zip` and Linux as `.tar.gz`,
handled transparently).

CI (`.github/workflows/test.yml`) runs vet + build + tests on every push
and PR — Linux with the race detector, plus a Windows runner. Pushing a
`v*` tag triggers `.github/workflows/release.yml`, which re-runs the
tests and attaches version-stamped `windows-amd64` (zip) and
`linux-amd64` (tar.gz) builds of both binaries to a GitHub release.

## Wiring real agent hooks

Agent hooks go through one generic handler, `wmux hook run <agent>`,
driven by a per-agent TOML profile describing that agent's wire format
(stdin JSON vs. JSON-as-final-argument) and field names. Profiles for
**claude, codex, kimi, kiro** ship inside the binary — `wmux hook list`
shows what's known. `wmux hook-claude` and `wmux hook-codex` remain as
aliases for `wmux hook run claude`/`wmux hook run codex`, so existing
configs keep working. Don't use `wmux notify` directly for agent wiring —
it's just the manual testing entry point.

### Claude Code

Claude Code invokes command hooks with the event payload on **stdin** as
JSON (`session_id`, `cwd`, `message`, ...). Add to `~/.claude/settings.json`
(or your project's `.claude/settings.json`):

```json
{
  "hooks": {
    "Notification": [
      {
        "matcher": "",
        "hooks": [{ "type": "command", "command": "wmux hook-claude" }]
      }
    ]
  }
}
```

Claude Code's own `session_id` becomes the wmux session ID directly — you
don't need to have registered the session via `wmux new` first; the daemon
accepts (and publishes) a notify for any session ID.

### Codex CLI

Codex uses a simpler `notify` key in `config.toml` — an argv array that
Codex invokes with **one extra JSON argument appended**, not stdin. Codex's
newer `hooks.json` framework is explicitly not available on Windows yet, so
this is the integration point to use there. Add to `~/.codex/config.toml`
(root keys must appear before any `[tables]`):

```toml
notify = ["wmux", "hook-codex", "--session", "my-project"]
```

Codex currently only emits `agent-turn-complete` through `notify` (not
per-tool events), and `--session` is a fixed label you choose per
`config.toml` rather than something Codex hands you — it falls back to the
current working directory if omitted.

**If `notify` is already taken** — the Codex desktop app claims it for its
own handler (`codex-computer-use.exe turn-ended`), and Codex allows only
one `notify` command. Chain the existing handler through `--forward`
instead of displacing it (one `--forward` per argv token; the JSON payload
is appended to the forwarded invocation exactly as Codex would have done):

```toml
notify = [ "C:\\wmux\\wmux.exe", "hook-codex", "--session", "codex",
           "--forward", "C:\\...\\codex-computer-use.exe", "--forward", "turn-ended" ]
```

The forward runs first and unconditionally — every event type, even when
`wmuxd` is unreachable — and its exit code is what Codex sees; the wmux
notify itself is best-effort in this mode, so a wmux problem can never
break the app's own notification chain.

### Kimi Code, Kiro — and any other agent that copied Claude Code's hooks

Several newer CLI agents (Kimi Code CLI, Kiro CLI) adopted Claude Code's
hook payload shape outright — stdin JSON with the same `session_id` /
`cwd` / `hook_event_name` / `message` field names. Bundled profiles cover
them already: point the agent's hook command at `wmux hook run kimi` /
`wmux hook run kiro` in its own hooks config.

For an agent wmux doesn't know yet, drop a profile at
`~/.wmux/agents/<name>.toml` (a user file with a bundled name replaces
the bundled profile wholesale):

```toml
name = "someagent"
wire = "stdin-json"            # or "argv-json" (payload as final CLI argument)
session_field = "session_id"   # dot-paths into the JSON payload
cwd_field = "cwd"              # session fallback when session_field is empty
message_field = "message"
event_field = "hook_event_name"
event_allow = ["Stop"]         # empty/omitted = notify on every event
# default_message = "turn done"  # used when the message field is empty;
                                 # omitted = empty message sends nothing
# session_fallback = "getwd"     # last-resort session ID = hook's cwd
```

Then run `wmux hook run someagent` from the agent's hook — no new Go code
involved. `--session ID` overrides the payload's session; `--forward`
chains a pre-existing handler exactly as described for Codex above (for
stdin-wire agents the payload is piped to the forwarded command's stdin).

### Notifying without a hook (raw OSC)

Anything running inside a tracked session can notify by printing an OSC
escape sequence — no hook wiring needed. Three forms are recognized:

```
printf '\033]9;build done\007'                                    # plain message
printf '\033]99;title=Agent;message=needs input;type=agent_input\007'  # structured
printf '\033]777;notify;Build;complete\007'                       # rxvt-style title;message
```

OSC 99 takes `key=value` pairs separated by `;` — `title`, `message`,
and `type` (e.g. `agent_input`, `agent_done`, `error`); a body with no
`=` is treated as a plain message. The parsed title/message/kind land as
separate fields on the `/events` notify payload, and `wmux list`/the
sidebar show them as `title: message`.

### Important: where the daemon needs to run

Whichever `wmux hook run <agent>` command (or `hook-claude`/`hook-codex`
alias) actually gets invoked runs **wherever the agent process itself
runs**.

**Genuinely native Linux (no Windows host at all)** — e.g. Claude Code
running directly on a Linux desktop: none of the WSL networking
discussion below applies. `wmuxd` and the hook command both run as
ordinary Linux processes on the same machine, talking over ordinary
`127.0.0.1` loopback, exactly as `localhost` HTTP normally works. Install
per "Installing (Linux)" above and wire the hooks per "Wiring real agent
hooks" above — that's the whole story.

**WSL2 on a Windows host** — if Claude Code / Codex run
inside a WSL2 distro (the common case there), the hook command needs a `wmux`
binary reachable from inside that distro, and it needs to reach a `wmuxd`
listening on `127.0.0.1:47823` from that same network namespace.

The simplest setup: run **both** `wmuxd` and `wmux` from the Linux build
(`bin/linux-amd64/`) inside the WSL distro itself, rather than running
`wmuxd.exe` on the Windows side. This sidesteps the WSL2-to-Windows
networking question entirely, since the daemon and the hook command share
the same localhost. The Windows-native `wmuxd.exe`/`wmux.exe` build is still
useful for orchestration from PowerShell (spawning sessions via
`wsl.exe -d <distro>` — see the "Running it" section above), but for the
hook wiring itself, WSL-resident is the path of least resistance.

If you do want a single Windows-side daemon that both PowerShell and
WSL-resident hooks can reach, you'll need WSL2's mirrored networking mode
so `127.0.0.1` on the Windows host and inside WSL refer to the same
loopback — otherwise you'd need to target the WSL virtual adapter's IP
from the Windows side instead of `127.0.0.1`. **Verified on a real
Windows 11 + WSL2 machine without a `.wslconfig` (mirrored mode off, the
actual default):** WSL → Windows over `127.0.0.1` does **not** work
(connection refused), so a hook running inside WSL cannot reach
`wmuxd.exe` on the Windows side without mirrored mode. Windows → WSL over
`127.0.0.1` **does** work out of the box (WSL2's built-in localhost
forwarding, unrelated to mirrored mode) — so PowerShell-side orchestration
via `wmux pane`/`wmux new --distro ...` can always reach a WSL-resident
daemon, it's only the hook direction that needs mirrored mode.

## Next steps

1. ~~**Real hook wiring**~~ — done: `wmux hook-claude` (stdin JSON) and
   `wmux hook-codex` (JSON as final arg) are implemented and tested against
   both agents' actual current payload formats. See "Wiring real agent
   hooks" above.
2. ~~**`wt.exe` orchestration**~~ — done and verified end-to-end on real
   Windows + WSL2: `wmux attach` (real TTY passthrough + daemon
   registration) and `wmux pane` (shells out to `wt.exe -w 0
   new-tab`/`split-pane` running `wmux attach` inside a WSL distro).
   Fixed a real quoting-chain bug found during that verification: `wt.exe`
   re-tokenizes its trailing commandline and splits on any unescaped `;`
   (even one nested inside an already-quoted argv token), so a `--cmd`
   containing a compound shell command used to silently truncate. Fixed by
   base64-encoding the inner command and piping it through decode+exec
   with no quote characters at all (`echo <b64>|base64 -d|bash`) — see
   NOTES.md for the full debugging trail, including a second failed fix
   attempt (`eval "$(...)"`) that hit a separate embedded-quote mangling
   issue specific to `wt.exe`'s parser.
3. ~~**`wmux pane --native`**~~ — done: runs the command directly on
   Windows via `powershell.exe -EncodedCommand`, no WSL, for agents that
   are native Windows installs. Verified against a real `claude.exe`.
4. ~~**`wmux close`**~~ — done: kills a session's tracked process
   (daemon-owned for `wmux new`, registered PID for `wmux attach`/`wmux
   pane`). Verified end-to-end for both session types via real
   process-list checks. Originally couldn't remove the `wt.exe` pane
   itself; superseded by the profile flow in (8), which makes panes
   close themselves.
5. ~~**Tray/sidebar UI**~~ — done, as a TUI pane instead of a Wails/Tauri
   app (single binary, lives inside the WT layout; see
   `docs/sidebar-design.md` for the reasoning). `wmux sidebar` opens a
   live session sidebar as a new tab's leftmost pane: running state, git
   branch, cwd, ports, unread-notification badges, plus Enter/click to
   focus a session's pane, `x` to close it, and `n` to open a new native
   agent pane. Backed by a new typed `/events` envelope
   (`{"type":"notify"|"sessions",...}`) that pushes session lifecycle and
   branch/port changes, so the sidebar re-renders from SSE push instead
   of polling. `wmux sidebar --with CMD --cwd PATH [--native]` opens the
   sidebar plus a first agent pane (sidebar keeps ~22% width) in one
   shot, and `wmux sidebar --grid A,B[,C[,D]] --with CMD --cwd PATH`
   opens the sidebar plus a 2-4 pane `wmux grid` layout beside it in the
   same tab (every pane running CMD as its own session). A native-window
   UI can still slot in later against the same API.
6. ~~**Port scoping**~~ — done, and fixed a real latent bug found while
   implementing it: a **native** Windows session's git branch/port
   polling was always shelling into WSL regardless (the daemon only ever
   checked its own `runtime.GOOS`, never whether *this particular
   session* was native or WSL-targeted), so branch lookups against a
   Windows path like `D:\...` were silently broken. Fixed by having
   `wmux attach` report its own nativity (from its own `runtime.GOOS`) at
   register time. `wmux list` now shows only the ports
   actually opened by a session's own process tree, not every listening
   port on the machine. Walks the real process tree (via
   `Get-CimInstance Win32_Process` on native Windows sessions, `/proc`
   via `ps -eo pid,ppid` on WSL/Linux sessions) and cross-references it
   against the platform's own port→owning-PID data
   (`Get-NetTCPConnection -OwningProcess` / `ss -ltnp`). Verified on both
   platforms: a session opening exactly one port shows exactly that port,
   not the dozen-plus system-wide ports it used to. One known gap: a
   `wmux new`/plain `wmux pane` session on a **Windows-native** daemon is
   always WSL-targeted via `wsl.exe`, whose Windows-side PID has no
   correlation to PIDs inside the WSL distro's own namespace — scoping
   isn't attempted there and it falls back to listing every port inside
   the distro (the old behavior), same as before this change.
7. ~~**Session persistence**~~ — done: `wmuxd` now snapshots session
   state to `~/.wmux/state.json` (override with `--state`) after every
   lifecycle change, and restores it on startup. Each restored session's
   PID is re-checked for liveness, so a session whose process died while
   the daemon was down comes back correctly marked `exited`, not
   `running`. Verified all three cases: daemon restart with the process
   still alive (restores as running, metadata polling resumes), daemon
   restart after the process died independently (restores as exited,
   with no `wmux close`/deregister call involved), and a normal
   close-then-restart.
8. ~~**Full pane close + focus switching**~~ — done: `wmux pane` now
   opens panes on an auto-installed `wmux` WT profile fragment
   (`closeOnExit: "always"`, fixed commandline `wmux pane-exec` that
   claims the session spec from the daemon by pane title), because a
   pane only honors its profile's `closeOnExit` when running the
   profile's own commandline — verified empirically; a CLI-passed
   commandline always leaves an inert dead pane. Panes now vanish on
   agent exit and on `wmux close`. New `wmux focus --id ID` (UI
   Automation: foreground the right WT window, select the tab, focus the
   exact pane — verified keyboard focus lands on the right TermControl,
   including split halves) and `wmux focus --dir left|right|up|down`
   (relative `wt move-focus`). Both verified end-to-end on real
   Windows 11 + WT 1.24.
9. ~~**Structured logger**~~ — done: `internal/wmuxlog` replaces scattered
   `log.Printf` across `internal/daemon` with `log/slog`, JSON to
   `~/.wmux/wmuxd.log` with size-based rotation, level via
   `WMUX_LOG_LEVEL` or a persisted `~/.wmux/loglevel` file (same
   env-then-file resolution as `wmux theme`, for the same reason: a
   detached/Task-Scheduler wmuxd never inherits an env var set in some
   other shell). See `docs/logger-design.md`.
10. ~~**Runtime debugger**~~ — done: closed the daemon's biggest
    reliability gap found while planning this — zero `recover()` calls
    existed anywhere, so one panic in any session goroutine or HTTP
    handler took the whole process down. `safeGo`/`recoverHandler`
    (`internal/daemon/debug.go`) catch and record panics into a bounded
    ring buffer instead, exposed via `/debug/state`, `/debug/panics`,
    `/debug/events/recent`, stdlib pprof under `/debug/pprof/`, and
    `wmux debug state|panics|events|dump|pprof`. See
    `docs/debugger-design.md`.
11. ~~**Windows installer**~~ — done: `install/install.ps1` (no admin
    needed) downloads the latest GitHub release, installs to
    `%LOCALAPPDATA%\Programs\wmux`, persists the user PATH, and
    registers wmuxd autostart — the bootstrap path `wmux update` can't
    cover (getting `wmux.exe` onto a machine that doesn't have it yet).
    `install/uninstall.ps1` reverses it. Tested end-to-end against a
    real published release; caught a real bug doing so (GitHub serves
    `SHA256SUMS` as `application/octet-stream`, so `Invoke-WebRequest`'s
    `.Content` came back as a raw byte array instead of a string).
