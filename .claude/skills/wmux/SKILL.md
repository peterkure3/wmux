---
name: wmux
description: >
  Operate wmux, the Windows notification/session daemon for AI coding agents
  (Claude Code, Codex) — either running natively on Windows or inside WSL2.
  Covers starting wmuxd, spawning or attaching sessions, opening wt.exe
  panes (WSL only), wiring Claude Code/Codex notification hooks, and
  diagnosing WSL2 networking/distro issues. Use when the user wants to
  start/stop wmuxd, run wmux new/attach/pane/list/watch, or configure agent
  hooks for wmux.
---

# wmux

Local HTTP+SSE daemon (`wmuxd`) that tracks agent coding sessions (Claude
Code, Codex) — watches for OSC 9/99/777 notify escape sequences, polls git
branch + listening ports, and lets `wmux hook-claude`/`wmux hook-codex`
push notifications into it from agent hooks. `wmux` is the CLI.

Two binaries, one Go module. Source: `cmd/wmuxd`, `cmd/wmux`,
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
- `wmux pane --native` opens a Windows Terminal pane running the command
  directly on Windows (via `powershell.exe -EncodedCommand`, no WSL). Plain
  `wmux pane` (no `--native`) always routes through `wsl.exe`, so it can't
  launch a native Windows binary — you need the flag.

**WSL-based agent** (agent is actually installed inside the distro):
- Run `wmuxd`/`wmux` from the **Linux build** (`bin/linux-amd64/`),
  resident *inside* the distro, not the Windows build. Verified on real
  hardware: WSL → Windows over `127.0.0.1` does **not** work without WSL2
  mirrored networking mode (off by default, no `.wslconfig`), so a hook
  firing from inside WSL cannot reach a Windows-native `wmuxd.exe`.
  Windows → WSL over `127.0.0.1` *does* work (built-in localhost
  forwarding), so the Windows-native build is still fine for
  **orchestration** (`wmux pane`, `wmux new --distro ...` from
  PowerShell) even when the daemon itself lives in WSL.
- `wmux pane` applies here — it's specifically the "open a WSL agent in a
  new Windows Terminal pane" tool.

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
WSL-targeted `wmux new`/plain `wmux pane` session on a Windows-native
daemon, where the tracked PID (the `wsl.exe` frontend) has no
correlation to PIDs inside WSL, so that one case still falls back to
listing every port inside the distro.

## Spawning/attaching sessions

`--distro` is **optional** on `wmux new`/`wmux pane` (WSL path only) — if
omitted, `wsl.exe` falls back to the user's actual configured default
distro (`wsl.exe --status`), so don't guess or hardcode a distro name
(older versions of this tool hardcoded `"Ubuntu"`, which broke on any
machine whose default distro was named something else — fixed, don't
reintroduce it).

**Native Windows agent, in your current terminal — `wmux attach`, no WSL:**
```powershell
wmux attach --id my-project --cwd D:\path\to\project -- "C:\Users\you\.local\bin\claude.exe"
```
Real TTY passthrough, registers with the daemon, deregisters on exit
(even non-zero exit codes).

**Native Windows agent, in a new Windows Terminal pane — `wmux pane --native`:**
```powershell
wmux.exe pane --native --id my-project --cwd D:\path\to\project --cmd "C:\Users\you\.local\bin\claude.exe" --split right
```
Same TTY/daemon guarantees as `wmux attach`, just opened in a fresh
tab/split instead of your current terminal. `--split` takes `tab`
(default, new tab), `right` (side-by-side), or `down` (stacked) — not
`v`/`h`: `wt.exe`'s own `-V`/`-H` name a split after the orientation of
the *dividing line*, backwards from what most people mean by
"vertical"/"horizontal" (verified by screenshot that `-V` produces
left/right, not top/bottom), so `wmux pane` uses unambiguous names
instead.

**PowerShell 5.1 quoting gotcha for `--cmd` on the native path:** if the
agent's path contains a space (e.g. a username like `Peter Kure`), do
**not** wrap it in embedded double quotes inside `--cmd`
(`--cmd '"C:\Users\Peter Kure\...\claude.exe"'`) — PowerShell 5.1's
native-argv passing mangles arguments containing literal embedded `"`
characters (verified: it silently ate the trailing `--split` flag in
testing), and this happens regardless of whether the quotes come from an
inline string or a variable. Two working alternatives:
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
wmux new --id my-project --cwd /home/you/my-project --cmd "codex exec ..."
wmux list
wmux watch
```

**WSL-based agent, interactive** (real TTY passthrough — what
`claude`/`codex` actually need):
```
wmux attach --id my-project --cwd /home/you/my-project -- claude
```

**WSL-based agent, open a new Windows Terminal tab/pane running it** (run
from PowerShell, not from inside WSL — `wt.exe` isn't reachable from
within a distro):
```powershell
wmux.exe pane --id my-project --cwd /home/you/my-project --cmd claude --split right
```
`--split` accepts `tab` (default), `right`, or `down`. If `--cmd` needs to be a
compound shell command (semicolons, pipes, etc.), that's fine — `wmux
pane` base64-encodes the inner command specifically to survive `wt.exe`'s
own command-line tokenizer, which otherwise splits on unescaped `;` and
mangles embedded quote characters (see NOTES.md for the debugging trail
if this ever regresses).

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

## Stopping a session — `wmux close`

```
wmux close --id my-project
```

Kills the session's tracked process — the daemon-owned process for
`wmux new`, or the registered PID for `wmux attach`/`wmux pane` (the
daemon learns the real PID at register time, added specifically to make
this command possible). Ends the agent and deregisters the session
(`running` flips to `false` in `wmux list`) immediately. Verified against
both a daemon-owned (`wmux new`) and a registered (`wmux attach`) session:
confirmed via actual process-list checks (not just daemon state) that the
real OS process dies, not just the wmux-side bookkeeping.

Exiting the agent yourself (Ctrl+D, `/exit`) works the same way for a
`wmux attach` session — `wmux close` is for ending one *remotely*,
without a terminal attached to it.

**`wmux close` does not close the `wt.exe` pane/tab itself** if the
session was opened via `wmux pane` — Windows Terminal leaves an inert,
already-closed pane in its layout after the hosted process exits,
confirmed even on a clean zero exit code (so it's not an exit-code or
timing thing), and there's no `wt.exe` command-line API to remove an
existing pane from outside. That part still needs closing by hand (its
own close button, or Ctrl+Shift+W with it focused) — don't attempt to
fake this by broadly killing `wt.exe`/`conhost.exe`/`OpenConsole.exe`
processes by image name; a real machine accumulates many of these across
unrelated windows/tabs (including ones the user is actively working in),
and there's no reliable way to tell which belongs to which pane without
already knowing the specific PID `wmux close` targets.

## Diagnosing problems

- Session exits instantly with no output (WSL path) → almost always a
  bad/missing WSL distro. Check `wsl -l -v` and either omit `--distro` or
  pass the right name.
- `wmux hook-claude`/`hook-codex` returns "could not reach wmuxd" → the
  daemon isn't running in the same namespace the hook runs in (see Step 0
  — most common cause is a hook wired to the wrong side, e.g. pointing a
  native Windows agent's hook at a WSL-resident daemon or vice versa).
- `wmux pane` opens a window but the session never registers → check
  `wmux` is actually on PATH inside the target WSL distro (`which wmux`),
  and that `wmuxd` is running there too. (And confirm the agent is
  actually WSL-based in the first place — plain `wmux pane` can't launch a
  native Windows binary; use `--native` for that.)
- `wmux pane --native` opens a pane but a flag after `--cmd` seems to get
  silently dropped (e.g. `--split` reverting to the `tab` default) → the
  PowerShell 5.1 embedded-double-quote quirk above. Switch `--cmd` to an
  8.3 short path with no spaces/quotes needed.
