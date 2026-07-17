---
name: wmux
description: >
  Operate wmux, the Windows notification/session daemon for AI coding agents
  (Claude Code, Codex) — either running natively on Windows or inside WSL2.
  Covers starting wmuxd, spawning or attaching sessions, opening
  self-closing wt.exe panes (WSL or native), switching pane focus, wiring
  Claude Code/Codex notification hooks, and diagnosing WSL2
  networking/distro issues. Use when the user wants to start/stop wmuxd,
  run wmux new/attach/pane/focus/close/list/watch, or configure agent
  hooks for wmux.
---

# wmux

Local HTTP+SSE daemon (`wmuxd`) that tracks agent coding sessions (Claude
Code, Codex, Kimi, Kiro) — watches for OSC 9/99/777 notify escape
sequences, polls git branch + listening ports, and lets `wmux hook run
<agent>` push notifications into it from agent hooks (profile-driven;
`wmux hook list` shows known agents, `~/.wmux/agents/<name>.toml`
overrides/adds profiles; `hook-claude`/`hook-codex` are legacy aliases).
`wmux` is the CLI.

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

## How `wmux pane` works — the profile flow

`wmux pane` does not pass a commandline through `wt.exe`. It (1)
auto-installs a `wmux` Windows Terminal profile as a settings fragment
(`%LOCALAPPDATA%\Microsoft\Windows Terminal\Fragments\wmux\wmux.json` —
never edits the user's settings.json content; a running WT imports it
live after wmux touches settings.json's mtime), (2) files the session
spec with the daemon (`POST /panes/pending`), and (3) opens the pane
with `--profile wmux --title <id>` and **no commandline**. The profile's
fixed commandline `wmux pane-exec` runs inside the pane, reads the
session ID from its own console title, claims the spec
(`POST /panes/claim`), and runs `wmux attach` as before.

Why: a wt.exe pane only honors its profile's `closeOnExit` when running
the profile's own commandline (verified empirically — a CLI-passed
commandline always leaves an inert dead pane on exit, any exit code).
With `closeOnExit: "always"` on the profile, the pane **removes itself**
from the layout when its process chain dies. The pane keeps the session
ID as its fixed title (`--suppressApplicationTitle`) — that's also how
`wmux focus --id` finds it. Consequence: `wmux pane` now requires the
daemon reachable from the Windows side before it will open anything.

## Whole workspace at once — `wmux grid`, `wmux sidebar --grid`

```powershell
wmux grid --native --ids a,b,c,d --cwd D:\proj --cmd C:\...\claude.exe
wmux sidebar --grid a,b,c,d --native --cwd D:\proj --with C:\...\claude.exe
```

`grid` opens 2-4 equal-split panes in one new tab, each running `--cmd`
as its own session (2: side by side; 3: full-height left + two stacked
right; 4: 2x2 — first ID top-left, then clockwise). `sidebar --grid` is
the same layout squeezed into the right ~80% of a tab whose leftmost pane
is the live sidebar — sidebar plus whole workspace in one shot. Grid
panes all share one cwd/cmd; for per-pane commands open individual
`wmux pane` splits instead.

## Switching focus — `wmux focus`

```powershell
wmux focus --id my-project      # focus that session's pane/tab, any WT window
wmux focus --dir right          # move focus one pane right in the current window
```

Windows-side command like `wmux pane` (not from inside WSL). `--id` uses
UI Automation to foreground the right WT window, select the tab, and put
keyboard focus on the exact pane (works for split halves too). `--dir`
(`left`/`right`/`up`/`down`) is relative `wt move-focus` in the most
recently used window — for an agent calling it from inside a pane that's
its own window, so "focus the pane I just opened to my right" is
`wmux focus --dir right`.

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

For a `wmux pane` session, killing the process chain also removes the
pane from Windows Terminal's layout (the `wmux` profile's
`closeOnExit: "always"` — see the profile flow above). Panes opened by
**pre-profile wmux versions** still linger as inert panes after close —
that's unfixable from outside (`wt.exe` has no API to remove an existing
pane); close those by hand (close button, or Ctrl+Shift+W focused), and
don't attempt to fake it by broadly killing
`wt.exe`/`conhost.exe`/`OpenConsole.exe` processes by image name — a
real machine accumulates many of these across unrelated windows/tabs,
and there's no reliable way to tell which belongs to which pane.

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
- `wmux pane` opens a plain shell instead of the agent → the `wmux` WT
  profile fragment wasn't imported yet (first-ever run on the machine;
  normally WT imports it live after wmux touches settings.json). Retry
  once; if it persists, restart Windows Terminal.
- `wmux focus --id X` says not found → the pane's title doesn't match
  the session ID (session not opened via current `wmux pane`, or opened
  by an old wmux without `--suppressApplicationTitle`). Use
  `wmux focus --dir` instead, or reopen with current `wmux pane`.
