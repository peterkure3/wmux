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

### Example: native Windows Claude Code, in your current terminal

```powershell
wmux attach --id my-project --cwd D:\path\to\project -- "C:\Users\you\.local\bin\claude.exe"
```

Runs `claude.exe` right there with a real TTY (colors, readline, prompts
all work normally) while registering the session with the daemon under
the id `my-project`. When you exit Claude, it deregisters automatically.

### Example: native Windows Claude Code, in a new Windows Terminal pane

```powershell
wmux pane --native --id my-project --cwd D:\path\to\project --cmd "C:\Users\you\.local\bin\claude.exe" --split right
```

Opens a new side-by-side split running the same thing. `--split` also
takes `down` (stacked split) or `tab` (new tab, the default if you omit
`--split`).

(`wt.exe`'s own `-V`/`-H` flags name a split after the orientation of the
*dividing line*, which is backwards from what most people mean by
"vertical"/"horizontal" — verified by screenshot that `-V` actually
produces left/right, not top/bottom. `wmux pane` uses `right`/`down`
instead to sidestep that confusion entirely.)

**Gotcha:** if the exe path has a space in it (a common case — usernames
like `Peter Kure` do this), don't wrap it in embedded double quotes
inside `--cmd`. PowerShell 5.1 has a known bug marshalling arguments with
literal embedded `"` to native programs, and it can silently swallow
trailing flags like `--split`. Instead, use the path's 8.3 short form,
which has no spaces and needs no quoting:
```powershell
(New-Object -ComObject Scripting.FileSystemObject).GetFile("C:\Users\you\.local\bin\claude.exe").ShortPath
# -> C:\Users\PETERK~1\LOCAL~1\bin\claude.exe
```
```powershell
wmux pane --native --id my-project --cwd D:\path\to\project --cmd "C:\Users\PETERK~1\LOCAL~1\bin\claude.exe" --split right
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
wmux attach --id my-project --cwd /home/you/my-project -- claude
```

### Example: WSL-based Claude Code, new Windows Terminal pane

```powershell
wmux pane --id my-project --cwd /home/you/my-project --cmd claude --split right
```

Note: no `--native` here — that flag is specifically for the Windows
path. Plain `wmux pane` always launches inside WSL via `wsl.exe`.

`--cmd` can be a compound command (semicolons, pipes) and it'll still
work correctly — the underlying quoting through `wt.exe` is handled for
you:
```powershell
wmux pane --id build-and-run --cwd /home/you/my-project --cmd "npm install; npm run dev" --split down
```

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
wmux close --id my-project
```
Kills the session's tracked process — the daemon-owned process for
`wmux new`, or the registered PID for `wmux attach`/`wmux pane`.
Verified: this kills the real OS process (confirmed via process list,
not just daemon state) and deregisters the session immediately.

**If you opened it via `wmux pane`:** ending the process (whether by
exiting the agent yourself or via `wmux close`) also removes the pane
itself from Windows Terminal's layout — `wmux pane` opens every pane on
an auto-installed `wmux` WT profile with `closeOnExit: "always"`, whose
fixed commandline (`wmux pane-exec`) owns the pane's whole process
chain. (Panes opened by pre-profile wmux versions passed the commandline
straight through `wt.exe`, which never honors `closeOnExit` — those
still linger as inert panes and can only be closed by hand.)

## Switching focus

```
wmux focus --id my-project     # focus that session's pane/tab, wherever it is
wmux focus --dir right         # move focus one pane right in the current window
```

Run from the Windows side, like `wmux pane`. `--id` finds the pane by
its title — every `wmux pane` keeps the session ID as its fixed pane
title — via UI Automation: it foregrounds the right Windows Terminal
window, selects the tab, and puts keyboard focus on the exact pane,
including one half of a split. `--dir` is relative movement
(`left`/`right`/`up`/`down`) within the most recently used WT window —
handy for an agent that just opened a pane next to itself.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Session exits instantly, no output (WSL commands) | Bad or missing `--distro` | `wsl -l -v` to see real distro names; omit `--distro` to use your default, or pass the correct name |
| `wmux hook-claude`/`hook-codex` says "could not reach wmuxd" | Hook wired to the wrong side | A native Windows agent's hook needs a native `wmuxd`; a WSL agent's hook needs a WSL-resident `wmuxd` — they're on separate network namespaces unless WSL2 mirrored networking is on |
| `wmux pane` opens a window but the session never shows in `wmux list` | `wmux` not on PATH inside the target WSL distro, or `wmuxd` isn't running there | `wsl -d <distro> -- which wmux`; start `wmuxd` inside the distro |
| `wmux pane --native --cmd "..."` seems to drop a trailing flag like `--split` | PowerShell 5.1 embedded-quote bug (see the gotcha above) | Use the 8.3 short path instead of a quoted long path |
| `wmux notify ... ` doesn't show up against the right session in `wmux list` | Forgot `--session` | Add `--session <id>` |
| `wmux close --id X` succeeds but the pane/tab is still visible | Pane opened by a pre-profile wmux version (commandline passed through `wt.exe` never honors `closeOnExit`) | Close that pane by hand; panes opened by current `wmux pane` close themselves |
| `wmux pane` opens a plain shell instead of the agent | The `wmux` WT profile fragment wasn't imported yet (first-ever run; WT normally imports it live after `wmux pane` touches settings.json) | Retry `wmux pane`; if it persists, restart Windows Terminal once |
| `wmux focus --id X` says not found | Pane title no longer matches the session ID (pane opened by an old wmux version without `--suppressApplicationTitle`, or session not opened via `wmux pane`) | Use `wmux focus --dir` instead, or reopen the pane with current `wmux pane` |
| Complex `--cmd` garbled or exits with a bash syntax error | Quoting mangled crossing Git Bash / PowerShell / `wsl.exe` (`$()`, quotes, `;`) | Pipe it instead: `printf '%s' "$CMD" \| wmux surface ... --cmd -` (stdin bypasses every quoting layer); or put it in a script and `--cmd 'bash script.sh'` |
