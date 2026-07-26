# wmux user manual

A practical, example-driven guide to using `wmux`. For architecture and
setup rationale, see `README.md`. For development history and known
gotchas discovered during testing, see `NOTES.md`.

## What it does

`wmuxd` is a small background daemon that watches your AI coding agent
sessions (Claude Code, Codex) for notification events — "I need your
input", "permission needed", etc — and gives you one place to see and
react to them, instead of alt-tabbing between a dozen terminal windows to
find out which one is actually waiting on you.

`wmux` is the CLI you use to talk to it: spawn sessions, open terminal
panes, wire up hooks, and watch for notifications live.

## Before you start: where does your agent actually run?

This is the single most important thing to get right, and it's not
always obvious. Check:

```powershell
where claude
where codex
```

If either prints a `.exe` path (e.g. `C:\Users\you\.local\bin\claude.exe`),
that agent is a **native Windows install** — even if you also happen to
have WSL distros on the machine for other things. Only treat an agent as
**WSL-based** if it's actually installed inside a distro:

```powershell
wsl -d <distro> -- which claude
```

Everything below is split into "native Windows" and "WSL" examples —
use the one that matches what you just found. Don't assume; check.

## Installation

**Windows, quick path:** run the installer script — no admin rights
needed. It downloads the latest release (verified against `SHA256SUMS`,
same as `wmux update --release`), installs to
`%LOCALAPPDATA%\Programs\wmux`, adds that folder to your user PATH, and
registers wmuxd to start at logon:

```powershell
iwr https://raw.githubusercontent.com/peterkure3/wmux/main/install/install.ps1 | iex
```

Open a **new** terminal afterwards (PATH changes don't reach terminals
already open), then confirm with `wmux version`. To uninstall:
`install/uninstall.ps1` (add `-Purge` to also remove `~/.wmux`'s session
state/logs/settings). See `install/install.ps1 -?` for `-Version`/
`-InstallDir`/`-NoAutostart` options.

**Manual path** (any OS, or if you'd rather not run a script from the
internet):

1. Build or grab the binaries — `wmuxd.exe`/`wmux.exe` for native Windows
   use, `bin/linux-amd64/wmuxd`/`wmux` if you also need a WSL-resident
   daemon.
2. Put `wmux.exe`/`wmuxd.exe` somewhere permanent (e.g. `C:\wmux\`) and
   add that folder to your PATH so you can just type `wmux` from any
   terminal:
   ```powershell
   $current = [Environment]::GetEnvironmentVariable("Path", "User")
   [Environment]::SetEnvironmentVariable("Path", "$current;C:\wmux", "User")
   ```
   (Open a **new** terminal afterwards — existing ones won't see the
   updated PATH.)
3. If you also need the WSL-resident daemon, copy the Linux binaries
   somewhere on the distro's PATH:
   ```bash
   sudo cp bin/linux-amd64/wmux bin/linux-amd64/wmuxd /usr/local/bin/
   ```

## Updating

After the initial install, `wmux update` replaces the whole manual dance
(stop daemon, rebuild, copy, restart):

```powershell
wmux update --repo D:\path\to\wmux
```

It pulls the source repo (`git pull --ff-only`; skip with `--no-pull`),
builds fresh `wmux.exe`/`wmuxd.exe`, stops `wmuxd` if it was running,
swaps the binaries next to the running `wmux.exe`, and restarts the
daemon detached (its log goes to `~/.wmux/wmuxd.log`). If the daemon
wasn't running, the binaries are still swapped and it stays stopped.

Where it finds the source repo, in order: `--repo`, the `WMUX_REPO`
environment variable, then the repo path stamped into the binary by the
update run that built it — so after one bootstrap run with `--repo`,
plain `wmux update` works.

Notes:

- **Running sessions are fine.** Live panes/attaches keep executing the
  old (renamed) binary and keep working; they pick up the new version
  when reopened. `update` lists them as a warning and proceeds.
- A `wmux.exe.old` file lingers in the install folder until the next
  update collects it — the running updater can't delete itself.
- The **WSL-resident Linux binaries are not touched** — update those
  manually as in Installation step 3.
- `wmux version` / `wmuxd --version` print the installed version.

## Starting the daemon

Native Windows:
```powershell
wmuxd.exe
```

WSL-resident:
```bash
nohup wmuxd > /tmp/wmuxd.log 2>&1 &
```

Either way, confirm it's up:
```powershell
curl.exe -s http://127.0.0.1:47823/healthz
# -> ok
```

You need `wmuxd` running before anything else in this manual will work.

**Sessions survive a daemon restart.** `wmuxd` snapshots session state to
`~/.wmux/state.json` (override with `wmuxd --state <path>`, or
`--state ""` to disable) after every change, and restores it on startup —
each restored session's process is re-checked so a session that actually
died while the daemon was down comes back correctly marked exited, not
stuck showing running.

## Wiring the notification hook

This is what makes Claude Code push a message into wmux whenever it's
waiting on you, instead of you having to notice a quiet terminal.

**Native Windows agent** — edit `C:\Users\<you>\.claude\settings.json`:
```json
{
  "hooks": {
    "Notification": [
      {
        "matcher": "",
        "hooks": [{ "type": "command", "command": "C:\\wmux\\wmux.exe hook-claude" }]
      }
    ]
  }
}
```
(Use the full path to `wmux.exe` here rather than relying on PATH — the
process that invokes hooks doesn't always inherit a freshly updated one.)

**WSL-based agent** — edit the *WSL-side* `~/.claude/settings.json`
(inside the distro):
```json
{
  "hooks": {
    "Notification": [
      { "matcher": "", "hooks": [{ "type": "command", "command": "wmux hook-claude" }] }
    ]
  }
}
```

**Codex**, either side — `~/.codex/config.toml` (root keys must come
before any `[tables]`):
```toml
notify = ["wmux", "hook-codex", "--session", "my-project"]
```

## Running an agent session

The fastest way in is the multiplexer — see the next section. These are
the one-shot forms for when you want a single session, or a scripted one.

### Example: native Windows Claude Code, in your current terminal

```powershell
wmux attach "C:\Users\you\.local\bin\claude.exe"
```

Runs `claude.exe` right there with a real TTY (colors, readline, prompts
all work normally) while registering the session with the daemon. The id
defaults to the current directory's name; pass `--id my-project` to
choose it. When you exit Claude, it deregisters automatically.

**Gotcha:** if the exe path has a space in it (a common case — usernames
like `Peter Kure` do this), PowerShell 5.1 has a known bug marshalling
arguments with literal embedded `"` to native programs, and it can
silently swallow trailing flags. Use the path's 8.3 short form, which has
no spaces and needs no quoting:
```powershell
(New-Object -ComObject Scripting.FileSystemObject).GetFile("C:\Users\you\.local\bin\claude.exe").ShortPath
# -> C:\Users\PETERK~1\LOCAL~1\bin\claude.exe
```

### Example: WSL-based Codex, headless (batch run, no TTY)

For scripted/background runs where you don't need to type anything:

```bash
wmux new --id nightly-refactor --cwd /home/you/my-project --cmd "codex exec 'run the migration'"
```

`--distro` is optional here — if omitted, `wsl.exe` uses your system's
actual default distro. Only pass it if you want a non-default one:
```bash
wmux new --id nightly-refactor --cwd /home/you/my-project --cmd "codex exec '...'" --distro Ubuntu-22.04
```

### Example: WSL-based Claude Code, interactive

```bash
wmux attach claude
```

### Example: a session that outlives its terminal

```bash
wmux surface claude     # spawn it into a daemon-owned ConPTY, headless
wmux connect            # view and control it here; Ctrl-] detaches
```

The agent keeps running when you close the terminal. Reconnect any time
with `wmux connect` (it picks the only running surface, or name one).

## The multiplexer

```
wmux                      # session list; open panes from there with n
wmux claude               # open it with one pane already running claude
wmux grid 4               # four panes in a 2x2 grid
wmux grid 4 --claude      # the same grid, every pane running claude
wmux grid 3 --codex       # three panes: two over one, all running codex
```

Panes are `wmux surface` sessions, drawn by wmux itself — no Windows
Terminal splits, so this behaves the same on Windows, in WSL, and on
Linux. `wmux grid N` takes any N from 1 to 16. The agent flags are the
same set `wmux hook run` knows (`--claude`, `--codex`, `--kimi`,
`--kiro`, `--mimo`, `--agy`); `--agent NAME` and `--cmd CMD` also work,
and with none of them the panes are shells.

**Keys.** `ctrl+o` cycles panes and clicking focuses the pane under the
cursor, both without changing mode. `ctrl+b` switches to COMMAND mode,
which is sticky (unlike tmux's one-shot prefix) — the footer shows which
mode you're in.

| COMMAND key | does |
| --- | --- |
| `\|` / `v` | split the focused pane side by side |
| `-` / `s` | split the focused pane stacked |
| `n` | new pane, same axis as last time |
| `x` | close the focused pane |
| `tab`, arrows / `hjkl` | cycle / move focus |
| `b` | show or hide the sidebar |
| `g` | snap every pane into a balanced grid |
| `esc` / `i` | back to INSERT (typing at the pane) |
| `q` | quit |

Splitting hands off to the sidebar's own cwd/command prompt, so `ctrl+b`
`-`, a directory, and a command is the whole flow for a new pane.

`wmux theme midnight|frost|gradient` picks the color scheme; it applies
on the next launch.

## Watching for notifications

Two ways to check in on things:

**Snapshot** — current state of every known session (git branch,
listening ports, last notification, running/exited):
```
wmux list
```
Example output:
```
my-project            running    /home/you/my-project   branch=main   ports=[3000] note="Claude is waiting for your input"
nightly-refactor       exited     /home/you/my-project   branch=main   ports=[]     note=""
```

`ports` is scoped to the session's own process tree, not every listening
port on the machine — with one exception: a WSL-targeted `wmux new`/plain
`wmux pane` session on a **Windows-native** daemon falls back to listing
every port inside the distro, since that session's tracked PID (the
Windows-side `wsl.exe` frontend) has no correlation to PIDs inside WSL's
own namespace.

**Live feed** — streams notifications as they happen, useful to leave
running in a spare terminal:
```
wmux watch
```
Example output as it happens:
```
watching for notifications... (Ctrl+C to stop)
[14:32:07] my-project: Claude is waiting for your input
[14:35:51] nightly-refactor: Codex finished a turn
```

## Manual/test notifications

Useful for testing your setup without waiting for a real agent event:
```
wmux notify "test message" --session my-project
```
**Common mistake:** forgetting `--session`. Without it, the notification
still gets pushed (you'll see it in `wmux watch`), but with an empty
session ID — it won't attach to any session in `wmux list`, so you won't
see it reflected there. Always pass `--session <id>` matching an id
you're tracking with `wmux new`/`wmux attach`.

## Ending a session

Just exit the agent (Ctrl+D, `/exit`, closing the process) — `wmux
attach` notices and deregisters automatically, including on a non-zero
exit code.

To end it remotely instead of from inside the session:
```
wmux close                # the only running session
wmux close my-project     # ...or name one
```
Kills the session's tracked process — the daemon-owned process for
`wmux new`/`wmux surface`, or the registered PID for `wmux attach`.
Verified: this kills the real OS process (confirmed via process list,
not just daemon state) and deregisters the session immediately. With
several sessions running and no ID given, it lists them rather than
guessing.

Inside the multiplexer, `ctrl+b` `x` does the same to the focused pane
and removes it from the layout.

## Switching focus

Focus lives inside the multiplexer, and there are three ways to move it:

- **Click** the pane (or the sidebar) you want.
- **`ctrl+o`** cycles through panes in reading order.
- **`ctrl+b`** then `tab`, an arrow key, or `hjkl` — `tab` cycles, the
  direction keys move to the pane geometrically that way.

The sidebar lists every session the daemon knows about; `enter` on one
focuses its pane if it is open in this multiplexer.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Session exits instantly, no output (WSL commands) | Bad or missing `--distro` | `wsl -l -v` to see real distro names; omit `--distro` to use your default, or pass the correct name |
| `wmux hook-claude`/`hook-codex` says "could not reach wmuxd" | Hook wired to the wrong side | A native Windows agent's hook needs a native `wmuxd`; a WSL agent's hook needs a WSL-resident `wmuxd` — they're on separate network namespaces unless WSL2 mirrored networking is on |
| A pane opens but the session never shows in `wmux list` | `wmux` not on PATH inside the target WSL distro, or `wmuxd` isn't running there | `wsl -d <distro> -- which wmux`; start `wmuxd` inside the distro |
| A quoted native path seems to drop a trailing flag | PowerShell 5.1 embedded-quote bug (see the gotcha above) | Use the 8.3 short path instead of a quoted long path |
| `wmux notify ... ` doesn't show up against the right session in `wmux list` | Forgot `--session` | Add `--session <id>` |
| `wmux grid 4 --claude` opens shells, not claude | An old wmux build (the count used to swallow the flags after it) | `wmux update`; `wmux version` to confirm |
| Panes render but keys do nothing | You are in COMMAND mode | Look at the footer; press `esc` or `i` to get back to INSERT |
| Complex `--cmd` garbled or exits with a bash syntax error | Quoting mangled crossing Git Bash / PowerShell / `wsl.exe` (`$()`, quotes, `;`) | Pipe it instead: `printf '%s' "$CMD" \| wmux surface --cmd -` (stdin bypasses every quoting layer); or put it in a script and `--cmd 'bash script.sh'` |
