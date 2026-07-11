# wmux

A cmux-equivalent notification/session daemon for Windows agent workflows.
`wmuxd` spawns and watches agent sessions (Claude Code, Codex, etc.) for
OSC 9/99/777 notification escape sequences, tracks git branch and listening
ports per session, and serves it all over a local HTTP API. `wmux` is the
CLI you wire into agent hooks and use to inspect state.

Status: daemon + CLI are working end-to-end (tested: spawn → OSC-9 parse →
live SSE push → `list`/`watch` output). Tray/sidebar UI is not built yet —
see "Next steps" below.

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

Then from any shell, spawn an agent session. On Windows this shells out to
`wsl.exe -d <distro>`; if you're running the daemon inside WSL2 itself, it
just execs the command directly:

```
wmux new --id my-project --cwd /home/you/my-project --cmd "claude" --distro Ubuntu
wmux list
wmux watch
```

Wire the notify CLI into your agent's hook config, e.g. for Claude Code's
`on-idle`/notification hook, point it at:

```
wmux notify "Claude is waiting for your input" --session my-project
```

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
(current default on recent Windows/WSL builds) so `127.0.0.1` on the Windows
host and inside WSL refer to the same loopback — otherwise you'd need to
target the WSL virtual adapter's IP from the Windows side instead of
`127.0.0.1`.

## Next steps

1. ~~**Real hook wiring**~~ — done: `wmux hook-claude` (stdin JSON) and
   `wmux hook-codex` (JSON as final arg) are implemented and tested against
   both agents' actual current payload formats. See "Wiring real agent
   hooks" above.
2. **`wt.exe` orchestration** — add a `wmux split`/`wmux new-tab` that
   shells out to `wt.exe` with the right `wsl.exe -d <distro> -- wmux
   attach <id>` args, so spawning a session also opens the visible pane.
3. **Tray/sidebar UI** — a small Wails or Tauri app subscribing to
   `GET /events` (SSE) and `GET /sessions`, showing a notification badge
   and the sidebar metadata (branch/ports/last note) per session.
4. **Port scoping** — `listeningPorts` currently lists all system-wide
   listening ports rather than scoping to the session's process tree;
   fine for one-project-at-a-time dev setups, worth tightening later.
5. **Session persistence** — daemon currently loses all session state on
   restart; cmux persists a snapshot to disk for session restore, worth
   doing the same here once the core loop is solid.
