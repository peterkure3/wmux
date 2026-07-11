# wmux

A cmux-equivalent notification/session daemon for Windows agent workflows.
`wmuxd` spawns and watches agent sessions (Claude Code, Codex, etc.) for
OSC 9/99/777 notification escape sequences, tracks git branch and listening
ports per session, and serves it all over a local HTTP API. `wmux` is the
CLI you wire into agent hooks and use to inspect state.

Status: daemon + CLI are working end-to-end, verified on a real Windows 11
+ WSL2 machine (spawn → OSC-9 parse → live SSE push → `list`/`watch`
output, `wmux pane`'s full `wt.exe`/`wsl.exe` quoting chain, both hook
commands). Tray/sidebar UI is not built yet — see "Next steps" below.

**Note:** `--distro` (for `wmux new`/`wmux pane`) is optional — if omitted,
`wsl.exe` uses your system's actual default distro (`wsl.exe --status`),
same as running `wsl.exe` with no `-d` yourself. Pass `--distro <name>`
explicitly only if you want a *non-default* distro (check names with
`wsl -l -v`).

## Layout

```
cmd/wmuxd/       daemon entrypoint
cmd/wmux/        CLI entrypoint
internal/daemon/ session management, OSC watcher, git/port polling, HTTP+SSE server
internal/proto/  shared wire types
bin/             prebuilt binaries (windows-amd64, linux-amd64)
```

## Running it

On Windows, run `wmuxd.exe` once in the background (add it to Startup or
run it from Task Scheduler — no console needed since it's headless):

```
wmuxd.exe
```

**Important:** if your agents (Claude Code / Codex) run inside a WSL2
distro — the common case — run the Linux build of `wmuxd`/`wmux` (in
`bin/linux-amd64/`) *inside that distro* instead of the Windows build on
the host. See "Wiring real agent hooks" below for why.

### Headless sessions (`wmux new`)

Good for background/batch runs where you don't need to type into the
agent — spawns the process with no TTY, piping its output through the
daemon's OSC watcher:

```
wmux new --id my-project --cwd /home/you/my-project --cmd "codex exec ..."
wmux list
wmux watch
```

### Interactive sessions (`wmux attach` + `wmux pane`)

For anything you actually want to type into — `claude`, `codex`, a normal
interactive session — `wmux new` won't work: it has no TTY, so readline,
colors, and prompts all break, and there's no way to send it input at all.

`wmux attach` runs a command with full TTY passthrough (real
stdin/stdout/stderr) while still registering with the daemon for tracking:

```
wmux attach --id my-project --cwd /home/you/my-project -- claude
```

`wmux pane` (run from PowerShell, not from inside WSL) opens a new
Windows Terminal tab or split pane that runs `wmux attach` for you. By
default it runs the command inside a WSL distro:

```powershell
wmux.exe pane --id my-project --cwd /home/you/my-project --cmd claude --split right
```

Pass `--native` to run the command directly on Windows instead — no WSL,
no `wsl.exe` involved — for agents that are native Windows installs
(check with `where claude`/`where codex` first; having a WSL distro on
the machine doesn't mean the agent runs inside it):

```powershell
wmux.exe pane --native --id my-project --cwd D:\path\to\project --cmd claude.exe --split right
```

`--split` accepts `tab` (default, new tab), `right` (side-by-side split),
or `down` (stacked split).

Under the hood, `wmux pane` files a "pane spec" with the daemon and opens
the pane on a dedicated `wmux` Windows Terminal profile (installed
automatically as a [settings fragment](https://learn.microsoft.com/en-us/windows/terminal/json-fragment-extensions),
never touching your own `settings.json` content). The profile's fixed
commandline, `wmux pane-exec`, claims the spec back by session ID (carried
via the pane title) and runs `wmux attach` for you. This indirection is
what makes panes **close themselves**: a `wt.exe` pane only honors its
profile's `closeOnExit` setting when it runs the profile's own
commandline, so the profile can use `closeOnExit: "always"` — the pane
disappears from the layout the moment its process exits or `wmux close`
kills it, instead of lingering as an inert dead pane (which is what
happens with a commandline passed straight through `wt.exe`, and there's
no API to remove such a pane afterwards).

The pane keeps the session ID as its fixed title
(`--suppressApplicationTitle`), which is also what `wmux focus --id`
uses to find it.

(Note: `wt.exe`'s own `-V`/`-H` flags name the split after the
orientation of the *dividing line*, which is the opposite of what most
people mean by "vertical"/"horizontal" split — verified by screenshot
that `-V` actually produces a left/right layout. `wmux pane` uses
`right`/`down` instead specifically to avoid that confusion.)

**PowerShell 5.1 quoting note for `--native --cmd`:** if the agent's path
contains a space (e.g. a username like `C:\Users\Jane Doe\...`), avoid
wrapping it in embedded double quotes inside `--cmd` — PowerShell 5.1's
native-argv passing mangles arguments with literal embedded `"`
characters and can silently drop trailing flags like `--split`. Use the
8.3 short path instead (no spaces, no quoting needed):
`(New-Object -ComObject Scripting.FileSystemObject).GetFile("C:\Users\Jane Doe\...\claude.exe").ShortPath`.

### Switching focus (`wmux focus`)

Two addressing modes, both runnable by an agent (e.g. from a hook or a
tool call) as well as by hand — run from the Windows side, like
`wmux pane`:

```powershell
wmux focus --id my-project      # focus that session's pane/tab, wherever it is
wmux focus --dir right          # move focus one pane right in the current window
```

`--id` finds the pane by its title (every `wmux pane` keeps the session
ID as its fixed title) via UI Automation: it brings the right Windows
Terminal window to the foreground, selects the tab, and puts keyboard
focus on the exact pane — including one half of a split. `--dir`
(`left`/`right`/`up`/`down`) is relative movement within the most
recently used WT window (plain `wt move-focus`), useful for "jump to the
pane I just opened next to myself".

### Closing a session (`wmux close`)

```
wmux close --id my-project
```

Kills the session's tracked process — the daemon-owned process for
`wmux new`, or the registered PID for `wmux attach`/`wmux pane` (the
daemon learns the real PID at register time). This ends the agent and
deregisters the session (`wmux list` shows `running: false`
immediately).

For a session opened via `wmux pane`, killing the agent unwinds the
pane's whole process chain, and the `wmux` profile's
`closeOnExit: "always"` then removes the pane from the Windows Terminal
layout entirely — nothing left to close by hand. (Panes opened by older
wmux versions, which passed the commandline straight through `wt.exe`,
still linger as inert panes — that's unfixable from outside and exactly
why the profile flow exists.)

## Building from source

```
go build -o bin/wmuxd.exe ./cmd/wmuxd   # on Windows, or cross-compile:
GOOS=windows GOARCH=amd64 go build -o bin/wmuxd.exe ./cmd/wmuxd
GOOS=windows GOARCH=amd64 go build -o bin/wmux.exe  ./cmd/wmux
```

## Wiring real agent hooks

`wmux` has two dedicated subcommands that speak each agent's actual wire
format — don't use `wmux notify` directly for these, it's just the manual
testing entry point.

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

### Important: where the daemon needs to run

Whichever of `wmux hook-claude` / `wmux hook-codex` actually gets invoked
runs **wherever the agent process itself runs**. If Claude Code / Codex run
inside a WSL2 distro (the common case), the hook command needs a `wmux`
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
5. **Tray/sidebar UI** — a small Wails or Tauri app subscribing to
   `GET /events` (SSE) and `GET /sessions`, showing a notification badge
   and the sidebar metadata (branch/ports/last note) per session.
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
