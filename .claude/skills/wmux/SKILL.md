---
name: wmux
description: >
  Operate wmux, the notification/session daemon and terminal multiplexer for
  AI coding agents (Claude Code, Codex) — either running natively on Windows
  or inside WSL2. Covers starting wmuxd, the multiplexer TUI (plain `wmux`,
  `wmux grid N --claude`, its mode/split/focus keys), spawning detachable
  sessions, wiring Claude Code/Codex notification hooks, and diagnosing
  WSL2 networking/distro issues. Use when the user wants to start/stop
  wmuxd, run wmux/grid/new/attach/surface/connect/close/list/watch, or
  configure agent hooks for wmux.
---

# wmux

Local HTTP+SSE daemon (`wmuxd`) that tracks agent coding sessions (Claude
Code, Codex, Kimi, Kiro) — watches for OSC 9/99/777 notify escape
sequences, polls git branch + listening ports, and lets `wmux hook run
<agent>` push notifications into it from agent hooks (profile-driven;
`wmux hook list` shows known agents, `~/.wmux/agents/<name>.toml`
overrides/adds profiles; `hook-claude`/`hook-codex` are legacy aliases).
`wmux` is the CLI.

Two binaries, one Go module. Source: `cmd/wmuxd`, `cmd/wmux` (a shim),
`internal/cli` (subcommands), `internal/tui` (the multiplexer),
`internal/layout` (split/grid geometry), `internal/client`,
`internal/daemon`, `internal/proto`. Full background/rationale in
`NOTES.md` (dev handoff notes) and `README.md` (user-facing reference) at
the repo root — read those for anything not covered here.

## Step 0: find out where the agent actually runs — this determines everything else

Don't assume from the README's "common case" — **check**:
```powershell
where claude
where codex
```
If either resolves to a `.exe`/native path (e.g.
`C:\Users\<you>\.local\bin\claude.exe`), that agent runs **natively on
Windows**, full stop — even if WSL distros exist on the machine for other
things. Only treat an agent as WSL-based if it's actually installed and
invoked from inside a distro (`wsl -d <distro> -- which claude`).

**Native Windows agent** (this is what running `where claude` above will
usually show — don't default to assuming WSL just because a distro is
installed):
- Windows-native `wmuxd`/`wmux` only. No WSL involved anywhere.
- `wmux attach` execs the command directly — no `wsl.exe` wrapping.
- Pass `--native` to `wmux surface`/`wmux grid`/`wmux tui` so panes run
  the command directly on Windows (via `cmd.exe /c`). Without it they
  route through `wsl.exe`, which cannot launch a native Windows binary.

**WSL-based agent** (agent is actually installed inside the distro):
- Run `wmuxd`/`wmux` from the **Linux build** (`bin/linux-amd64/`),
  resident *inside* the distro, not the Windows build. Verified on real
  hardware: WSL → Windows over `127.0.0.1` does **not** work without WSL2
  mirrored networking mode (off by default, no `.wslconfig`), so a hook
  firing from inside WSL cannot reach a Windows-native `wmuxd.exe`.
  Windows → WSL over `127.0.0.1` *does* work (built-in localhost
  forwarding), so the Windows-native build is still fine for
  **orchestration** (`wmux grid`, `wmux new --distro ...` from
  PowerShell) even when the daemon itself lives in WSL.
- The multiplexer's panes go through `wsl.exe` by default, which is
  exactly the "run a WSL agent" case — no flag needed.

A machine can need both at once if the user runs some agents natively and
others in WSL — nothing stops running one `wmuxd` on each side
simultaneously (they're independent daemons on their own loopback
namespaces).

## Starting the daemon

Windows-native:
```
wmuxd.exe
```

WSL-resident (WSL-based agents only):
```bash
nohup wmuxd > /tmp/wmuxd.log 2>&1 &
```

Health check either way: `curl -s http://127.0.0.1:47823/healthz` → `ok`.

Sessions persist across a daemon restart (`~/.wmux/state.json`, override
with `--state`; each restored session's PID is re-checked for liveness,
so a session that died while the daemon was down comes back correctly
marked exited). `wmux list`'s `ports` column is scoped to a session's own
process tree, not every listening port on the machine — except a
WSL-targeted `wmux new`/`wmux surface` session on a Windows-native
daemon, where the tracked PID (the `wsl.exe` frontend) has no
correlation to PIDs inside WSL, so that one case still falls back to
listing every port inside the distro.

## Spawning/attaching sessions

`--distro` is **optional** wherever it appears (WSL path only) — if
omitted, `wsl.exe` falls back to the user's actual configured default
distro (`wsl.exe --status`), so don't guess or hardcode a distro name
(older versions of this tool hardcoded `"Ubuntu"`, which broke on any
machine whose default distro was named something else — fixed, don't
reintroduce it).

**Nothing is a required flag.** `--id` defaults to the working
directory's base name (uniquified against running sessions), `--cwd`
defaults to the current directory, and the command can be trailing
arguments instead of `--cmd`. Pass a flag when the default is wrong, not
by habit.

**The multiplexer — `wmux`, `wmux grid`:** this is the primary interface
and what plain `wmux` opens. Panes are `wmux surface` sessions drawn by
wmux itself (no `wt.exe`), so it works identically on Windows, in WSL,
and on Linux.
```
wmux                      # session list; open panes from there with n
wmux claude               # open with one pane already running claude
wmux grid 4               # four panes in a 2x2 grid
wmux grid 4 --claude      # the same grid, every pane running claude
```
`wmux grid N` takes N from 1 to 16 and balances the arrangement (2 side
by side, 3 two-over-one, 4 a 2x2, 6 a 3x2). The agent flags are the same
names `wmux hook list` shows; `--agent NAME` and `--cmd CMD` also work.

Keys: `ctrl+o` cycles panes, clicking focuses the pane under the cursor,
`ctrl+b` switches to a **sticky** COMMAND mode (`|`/`-` split, `n` new,
`x` close, `tab`/arrows/`hjkl` focus, `b` sidebar, `g` grid, `esc`/`i`
back to INSERT, `q` quit). The footer shows the current mode.

**Detachable session, no TUI — `wmux surface` + `wmux connect`:** the
daemon owns a ConPTY, so the session survives its viewing terminal.
```
wmux surface claude       # spawn headless
wmux connect              # attach here (the only running surface), Ctrl-] detaches
```

**Native Windows agent, in your current terminal — `wmux attach`, no WSL:**
```powershell
wmux attach "C:\Users\you\.local\bin\claude.exe"
```
Real TTY passthrough, registers with the daemon, deregisters on exit
(even non-zero exit codes).

**PowerShell 5.1 quoting gotcha on the native path:** if the agent's path
contains a space (e.g. a username like `Peter Kure`), do **not** wrap it
in embedded double quotes — PowerShell 5.1's native-argv passing mangles
arguments containing literal embedded `"` characters (verified: it
silently ate a trailing flag in testing), and this happens regardless of
whether the quotes come from an inline string or a variable. Two working
alternatives:
1. Use the 8.3 short path instead, which has no spaces and needs no
   quoting at all: get it via
   `(New-Object -ComObject Scripting.FileSystemObject).GetFile("C:\Users\you\...\claude.exe").ShortPath`
   (e.g. `C:\Users\PETERK~1\...\claude.exe`).
2. If the command truly needs embedded quotes, test the exact invocation
   once before relying on it — this is a general PowerShell 5.1
   limitation affecting any native exe, not specific to wmux.

**WSL-based agent, headless** (batch/background, no TTY — breaks anything
needing readline/prompts):
```
wmux new --cwd /home/you/my-project --cmd "codex exec ..."
wmux list
wmux watch
```

**WSL-based agent, interactive** (real TTY passthrough — what
`claude`/`codex` actually need):
```
wmux attach claude
```

If a command needs `$()`, quotes, or semicolons, pass it on stdin rather
than fighting the quoting chain: `printf '%s' "$CMD" | wmux surface --cmd -`.

## Wiring agent hooks

The hook command's location and the settings file it goes in both depend
on where the agent (and its matching daemon) actually runs — see Step 0.

**Native Windows agent** — edit the Windows-side
`C:\Users\<you>\.claude\settings.json` (i.e. plain `~/.claude/settings.json`
from a Windows shell's perspective). Use the full path to the installed
binary rather than relying on PATH having refreshed in whatever process
invokes the hook:
```json
{
  "hooks": {
    "Notification": [
      { "matcher": "", "hooks": [{ "type": "command", "command": "C:\\wmux\\wmux.exe hook-claude" }] }
    ]
  }
}
```

**WSL-based agent** — edit the *WSL-side* `~/.claude/settings.json`
(inside the distro, not the Windows one), pointing at the WSL `wmux`
binary:
```json
{
  "hooks": {
    "Notification": [
      { "matcher": "", "hooks": [{ "type": "command", "command": "wmux hook-claude" }] }
    ]
  }
}
```

Codex (`~/.codex/config.toml` on whichever side codex runs, root keys
before any `[tables]`):
```toml
notify = ["wmux", "hook-codex", "--session", "my-project"]
```

**Codex desktop app gotcha:** the app claims `notify` for its own handler
(`codex-computer-use.exe turn-ended`), and Codex allows only one `notify`
command — don't displace it. Chain it with `--forward` (one occurrence
per argv token; the JSON payload is appended to the forwarded command):
```toml
notify = [ "C:\\wmux\\wmux.exe", "hook-codex", "--session", "codex",
           "--forward", "C:\\...\\codex-computer-use.exe", "--forward", "turn-ended" ]
```
The forward runs first, for every event type, even with wmuxd down; its
exit code is propagated, and the wmux notify is best-effort. Also note:
the desktop app's handler path contains a versioned hash directory and
the app may rewrite config.toml on update — re-check the `notify` line
after app updates.

## Focus, splits, and layout — all inside the multiplexer

There is no `wmux focus`/`wmux pane`/`wmux send-keys` any more: the
`wt.exe` layer (WT profile fragment, console-title handshake, PowerShell
UI-Automation focus script) was retired once wmux drew its own panes.
Layout lives in the TUI:

- **Focus:** click a pane, `ctrl+o` to cycle, or `ctrl+b` then
  `tab`/arrows/`hjkl`.
- **Split:** `ctrl+b` then `|` (side by side) or `-` (stacked). Either
  opens the sidebar's cwd/command prompt for the new pane.
- **Grid:** `wmux grid N` at launch, or `ctrl+b` `g` to snap the current
  panes into one and keep them balanced as panes come and go.
- **Sidebar:** `ctrl+b` `b` shows/hides it.

## Stopping a session — `wmux close`

```
wmux close                # the only running session
wmux close my-project     # ...or name one
```

Kills the session's tracked process — the daemon-owned process for
`wmux new`/`wmux surface`, or the registered PID for `wmux attach` (the
daemon learns the real PID at register time, added specifically to make
this command possible). Ends the agent and deregisters the session
(`running` flips to `false` in `wmux list`) immediately. Verified against
both a daemon-owned (`wmux new`) and a registered (`wmux attach`) session:
confirmed via actual process-list checks (not just daemon state) that the
real OS process dies, not just the wmux-side bookkeeping.

With several sessions running and no ID given it lists them rather than
guessing — these commands are not undoable by re-running them.

Exiting the agent yourself (Ctrl+D, `/exit`) works the same way for a
`wmux attach` session — `wmux close` is for ending one *remotely*,
without a terminal attached to it. Inside the multiplexer, `ctrl+b` `x`
closes the focused pane and removes it from the layout.

## Diagnosing problems

- Session exits instantly with no output (WSL path) → almost always a
  bad/missing WSL distro. Check `wsl -l -v` and either omit `--distro` or
  pass the right name.
- `wmux hook-claude`/`hook-codex` returns "could not reach wmuxd" → the
  daemon isn't running in the same namespace the hook runs in (see Step 0
  — most common cause is a hook wired to the wrong side, e.g. pointing a
  native Windows agent's hook at a WSL-resident daemon or vice versa).
- A pane opens but the session never registers → check `wmux` is
  actually on PATH inside the target WSL distro (`which wmux`), and that
  `wmuxd` is running there too. (And confirm the agent is actually
  WSL-based in the first place — the default path can't launch a native
  Windows binary; use `--native` for that.)
- A flag after a quoted native path seems to get silently dropped → the
  PowerShell 5.1 embedded-double-quote quirk above. Switch to an 8.3
  short path with no spaces/quotes needed.
- `wmux grid 4 --claude` opens shells instead of claude → an old build,
  where the pane count swallowed the flags after it. `wmux update`.
- Panes render but keystrokes do nothing → the TUI is in COMMAND mode
  (the footer says so). `esc` or `i` returns to INSERT.
