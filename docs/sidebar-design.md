# wmux sidebar — design

Status: implemented (see `cmd/wmux/sidebar.go`, `cmd/wmux/sidebarui.go`).
This doc is the design of record; where code and doc disagree, trust code
and fix the doc.

## Goal

A left-hand sidebar inside Windows Terminal showing every open wmux
session/pane live: running state, git branch, cwd, ports, unread
notifications — with keyboard/mouse actions to focus, close, and open
panes. The cmux sidebar experience, on Windows, without building a
terminal emulator.

## Decision: TUI pane inside WT, in the wmux binary itself

Chosen over a Wails/Tauri native window because:

- Single Go binary, no webview toolchain, no second process to manage.
- Lives inside the WT layout the panes themselves live in — no separate
  window fighting for screen space or z-order.
- Reuses everything that already exists: the self-closing `wmux` WT
  profile, the daemon HTTP+SSE API, the UIA focus path, the
  `findWindowForPID` liveness probe.

Native-window UI stays on the table as a later upgrade if the TUI feels
cramped; nothing here precludes it (it would consume the same daemon
API).

## Architecture

```
wmux sidebar        launcher — opens a WT pane via the "wmux" profile with
                    --title wmux-sidebar. pane-exec sees that reserved title
                    and runs the TUI directly (no pane spec, no wmux attach,
                    so the sidebar never appears as a session itself).

wmux sidebar-ui     the TUI (Bubble Tea), runs inside that pane
  ├─ GET /sessions        initial snapshot
  ├─ GET /events (SSE)    live push: notify + session lifecycle (typed envelope)
  ├─ poll every 2s        fallback refresh + window-liveness probe
  └─ actions
       focus  → same UIA path as `wmux focus --id` (shared focusSessionByID)
       close  → POST /sessions/close (with y/n confirm)
       new    → same pane-spec flow as `wmux pane` (prompts cwd + cmd)
```

## Daemon change: typed SSE envelope

`GET /events` used to stream bare `NotifyEvent`s. It now streams:

```json
{"type":"notify","notify":{"sessionId":"api","body":"...","time":"..."}}
{"type":"sessions","sessions":[ ...SessionInfo... ]}
```

`sessions` events fire on every lifecycle transition (spawn, register,
deregister, close, reaped exit, liveness-detected exit) and whenever
`pollMetadata` sees a branch/ports diff. The sidebar therefore reacts
instantly to lifecycle changes; its own 2s poll only covers window
liveness and daemon restarts.

## Layout (~22% width)

```
 wmux
─────────────────────────────
▸ ● api       main
    D:\dev\api   :3000
    ✉ tests passed       2m
  ○ web       feat/login
    D:\dev\web
  ● scraper   main      wsl
─────────────────────────────
 3 sessions · 1 unread
 ↑↓ move  ⏎ focus  x close
 n new  r refresh  q quit
```

Row anatomy:

- Dot: `●` green = running, `○` dim = exited, `!` yellow = running but no
  visible console window found for its PID (native sessions only; WSL
  sessions show `wsl` instead — their PID means nothing to the Windows
  window table, same rule as `wmux panes`).
- Line 1: session ID + git branch.
- Line 2: cwd tail + listening ports.
- Line 3 only when there's an unread notification: `✉` + snippet + age.
  Unread is sidebar-local state, set by a notify event, cleared when the
  session is focused (Enter/click).

## Keys / mouse

| key            | action                                        |
|----------------|-----------------------------------------------|
| `↑/k` `↓/j`    | move selection                                |
| `Enter`/click  | focus that session's pane (UIA), clear unread |
| `x`            | close session (y/n confirm)                   |
| `n`            | new pane: prompts cwd then cmd, opens split   |
| `r`            | force refresh                                 |
| `q`/`Ctrl-C`   | quit (pane self-closes via closeOnExit)       |

## Placement constraint (why "sidebar first")

wt.exe's CLI can only split right (`-V`) or down (`-H`) of the focused
pane — no split-left, no swap-pane from the command line. So the sidebar
guarantees "leftmost" only by being the tab's first pane:

- `wmux sidebar` — new tab, sidebar fills it; agent panes split right
  from it.
- `wmux sidebar --with "<cmd>" --cwd PATH [--id ID] [--native] [--distro D]`
  — one shot: sidebar tab + first agent pane in a single chained wt.exe
  invocation (`new-tab ... ; split-pane -V -s 0.78 ...`), leaving the
  sidebar 22% of the width.
- A sidebar opened when panes already exist lands right of the focused
  pane; WT offers no CLI fix — swap manually in-terminal if it matters.

## Sidebar pane lifecycle

Reuses the `wmux` WT profile (closeOnExit "always", fixed commandline
`wmux pane-exec`). The launcher passes `--title wmux-sidebar`;
`pane-exec` treats that reserved title as "run the sidebar TUI in-process"
instead of claiming a pane spec. Quit (`q`) exits the process chain and
WT removes the pane. The reserved title also means `wmux pane --id
wmux-sidebar` is rejected.

## Dependency

First external dep in go.mod: `github.com/charmbracelet/bubbletea`
(raw-mode input, resize, mouse tracking, frame repaint — hand-rolling
those on Windows conhost/WT is weeks of edge cases). Styling is raw ANSI,
no lipgloss.

## Future / explicitly out of scope for v1

- Native-window sidebar (Wails/Tauri) consuming the same API.
- VT-replay pane previews (cmux-tui `attach-surface` style) — needs the
  daemon to own ConPTY, a different project tier.
- Filesystem-watcher git polling to replace the 3s timer.
- One-sidebar-per-window guard (duplicates are harmless; skipped).
