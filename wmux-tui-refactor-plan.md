# wmux: CLI + TUI refactor plan

Findings from reading `manaflow-ai/cmux` at HEAD alongside `peterkure3/wmux`,
and a phased plan.

---

## Part 1 — What cmux actually reuses

Your premise was that cmux "uses something that already exists." That's right,
but it's worth being precise about *which* things, because cmux reuses four
separate wheels and the interesting one isn't the CLI.

`cmux-tui/` is a **standalone Rust workspace** — a separate program from the
Swift macOS app — described in its own manifest as *"tmux-like terminal
multiplexer TUI backed by libghostty-vt."* Its dependency list:

| Concern | cmux reuses | wmux's Go equivalent | Already a dependency? |
| --- | --- | --- | --- |
| Terminal emulation | `ghostty-vt` (libghostty via bindgen FFI) | `charmbracelet/x/vt` | **Yes** — `Surface.emu` |
| TUI rendering | `ratatui` + `crossterm` | `bubbletea` + `lipgloss` + `bubbles` | **Yes** — sidebar |
| PTY | `portable-pty` | `aymanbagabas/go-pty` | **Yes** — `Surface.pty` |
| CLI | verbs generated from a spec | — | **No** |

**You already have Go equivalents of everything cmux reuses, and three of the
four are already in `go.mod` and already wired up.** The gap is not library
choice. It's structural.

### The structural idea worth stealing

cmux-tui is a **server with a versioned protocol, and every UI is a client**:

- Transport: Unix socket, JSON-lines (one JSON object per `\n`), plus an
  opt-in WebSocket listener for browser frontends.
- Every client sends `identify` first and must verify
  `app == "cmux-tui"` and `protocol == 10` before enabling v10 behaviour.
  The spec documents exactly which features degrade at v9, v8, v7, v6.
- `spec/cli.md` and `spec/commands.md` are separate documents, and the CLI
  verbs map **1:1** onto protocol commands. The CLI is not a special
  citizen — it's a thin client over the same wire the TUI uses.
- `bindings/` ships generated clients for Go, TypeScript, Python, Java, and
  Rust, with a conformance suite (`fixtures.json` + `runner.py` + `e2e.sh`).

And the single most useful line in the whole repo, from `spec/frontends.md`:

> Rich frontends should consume the server's authoritative render state: draw
> runs, place the cursor, and send keys. Byte attach remains the
> terminal-piping path for clients that intentionally run a terminal emulator
> or forward raw PTY state elsewhere.

That is the technical crux of Part 3 below. Hold onto it.

### What NOT to copy

The multi-language bindings and the conformance harness exist because cmux has
a Swift app, a browser frontend, and a Node SDK all talking to one server. You
have one Go binary talking to another Go binary in the same module. Copying
that machinery would be pure cost.

**Should you just use cmux-tui itself?** There is a Go binding, and
`uds_windows` in the dependency list means Windows was at least contemplated.
But it would mean shipping a Rust binary next to two Go binaries, on Windows,
to get a TUI you can build from crates you already have in Go form. There's
also a licensing question to resolve first — the root `package.json` says
GPL-3.0-or-later while the `cmux-tui` workspace declares MIT. Not worth
untangling. Build your own; borrow the architecture.

---

## Part 2 — Where wmux actually stands

### The CLI is the smaller problem

```
30   subcommands dispatched from a switch on os.Args[1]
13   hand-rolled flag.FlagSet instances across 8 files
106  os.Exit(1) sites — every failure is exit 1
1    --json flag in the entire CLI (wmux list)
```

Every error is indistinguishable to a caller: a typo in a flag, an unreachable
daemon, and a session that genuinely failed to start all exit 1. There's one
giant `usage()` banner and no per-subcommand help. Every command re-implements
"marshal, POST, check status, print, exit."

This is mechanical to fix and worth fixing, but it isn't what's holding wmux
back.

### The real problem: Windows Terminal owns the layout, and it shouldn't anymore

```
1,628  lines across grid/sidebar*/panes*/sendkeys/title + daemon/panes.go
   50  wt.exe references across 8 files
```

That subsystem exists entirely to make Windows Terminal act as a multiplexer
it was never designed to be:

- a **JSON fragment profile** installed into `%LOCALAPPDATA%` so panes honour
  `closeOnExit:"always"` and actually disappear
- the **console title** used as the only available channel to tell a new pane
  which session it is, plus a claim/retry handshake against the daemon
- a **PowerShell + UI-Automation script** to focus a pane, because `wt.exe`
  can only move focus relative to the current pane or by tab index
- a **`--suppressApplicationTitle`** flag so the agent can't rename the tab and
  break focus lookup for the rest of the session
- `wmux panes` and `wmux send-keys` reaching into Win32 console APIs to
  recover introspection `wt.exe` exposes no API for

Per NOTES.md, the original bet was: don't rebuild a terminal renderer, ride on
Windows Terminal + WSL2. **That was the right call at the time.** But then you
built `Surface`:

```go
type Surface struct {
    pty     pty.Pty              // you own the ConPTY
    emu     *vt.Emulator         // you have a full VT model of the screen
    clients map[chan proto.SurfaceFrame]struct{}  // you fan it out
    cols, rows int
}
```

Daemon-owned ConPTY, VT emulator, attach/detach with replay, resize. That is
a multiplexer core. **You already crossed the line the original bet was
protecting you from — you just never rendered more than one surface at a
time.** `wmux connect` attaches to exactly one, full-screen, and that's the
only consumer.

The whole `wt.exe` layer is a workaround for a constraint you removed and
didn't notice.

---

## Part 3 — The technical crux

Here's the part that determines whether the TUI is pleasant or miserable, and
it's why the cmux quote above matters.

`Surface.replayLocked()` currently serializes the screen as an **ANSI repaint**
— `\x1b[2J`, absolute cursor moves, SGR runs. That's perfect for `wmux connect`,
which dumps it straight to a terminal in raw mode.

It is exactly wrong for a TUI. Bubbletea renders by producing a string, diffing
it against the previous frame, and emitting minimal updates. If you paste a
child's ANSI — with its own absolute cursor positioning and screen clears —
into a bubbletea `View()`, the two will fight: the child's `\x1b[2J` wipes your
chrome, its cursor moves land wherever they like, and bubbletea's diff has no
idea what happened.

**So the daemon needs a second render mode**, exactly the split cmux draws
between "draw runs" and "byte attach". You have the data already — `emu.CellAt(x, y)`
returns content, style, and width per cell; `replayLocked` walks it today only
to turn it back into ANSI.

Sketch:

```go
// proto: a styled run of cells on one row — the unit a rich frontend draws.
type Run struct {
    X     int    `json:"x"`
    Text  string `json:"text"`
    FG    string `json:"fg,omitempty"`   // "" = default
    BG    string `json:"bg,omitempty"`
    Attrs uint8  `json:"attrs,omitempty"` // bold/italic/underline bitfield
}

type RowUpdate struct {
    Y    int   `json:"y"`
    Runs []Run `json:"runs"`
}

// FrameCells replaces FrameOutput for rich clients: only rows that changed
// since the client's last frame, plus the cursor.
type CellsFrame struct {
    Type    string      `json:"type"` // "cells"
    Rows    []RowUpdate `json:"rows"`
    Cursor  Pos         `json:"cursor"`
    Visible bool        `json:"cursorVisible"`
    Cols    int         `json:"cols"`
    Rows_   int         `json:"rows"`
}
```

Selected per-client at attach time — `GET /surfaces/attach?id=X&mode=cells`
vs the current `mode=bytes` — so `wmux connect` keeps working byte-for-byte
while the TUI gets something it can compose with lipgloss.

Two things this buys you beyond correctness:

- **Damage tracking.** Only emit rows whose cells changed since that client's
  last frame. A busy agent redrawing a spinner touches one row, not 30. Cheaper
  than the current "broadcast every chunk" fan-out.
- **Composability.** A run is `lipgloss.NewStyle().Foreground(...).Render(text)`.
  Cropping a pane to its rect is slicing runs. Borders, focus highlight, and
  the sidebar all become ordinary lipgloss around it.

Do this before writing any TUI code. Building the TUI on ANSI replay first and
retrofitting cells later means throwing the TUI away.

---

## Part 4 — The plan

Five phases. Each is independently shippable and leaves the tree working.

### Phase 0 — Protocol identity *(small — half a day)*

Add `GET /identify` returning `{app, version, protocol, pid, session}` and a
`proto.Version` constant. The CLI checks it once on first contact and fails
with a real message on mismatch.

You already have evidence you need this: the auth patch introduced a silent
version skew where an old `wmux.exe` gets a bare 401 from a new `wmuxd.exe`.
`identify` turns that into *"wmux 0.3 cannot talk to wmuxd 0.4 (protocol 2 vs 3)
— restart wmuxd."*

Do this first because every later phase adds protocol surface, and negotiating
capabilities is only possible if there's a version to negotiate against.

### Phase 1 — Real CLI *(1–2 days, mechanical)*

Land `internal/client` first (the §5 refactor from the code review — it's now
load-bearing for two phases, not just cleanup). Then move dispatch onto a
framework:

- **`spf13/cobra`** — ubiquitous, generates completions and per-command help,
  pairs with your existing Charm stack. Charm also ships a styling wrapper for
  cobra worth a look if you want the help output to match your sidebar theme.
- **`alecthomas/kong`** — struct-tags instead of builder calls, noticeably less
  boilerplate, smaller. Good fit if you like declarative.

Either is fine; cobra if you want completions and ecosystem, kong if you want
the smallest diff. Then adopt cmux's CLI discipline, which is the part that
actually matters:

- `--json` on **every** read command, not just `list`
- distinct exit codes: `0` ok, `1` command failed, `2` usage error,
  `3` connection/transport failure — replacing 106 undifferentiated `os.Exit(1)`
- commands that mutate print nothing on success; create commands print the id
- group the 30 verbs into namespaces: `wmux session {new,list,close,prune}`,
  `wmux pane {open,focus,send}`, `wmux daemon {status,log,debug,update}`,
  with the current flat names kept as hidden aliases so nothing breaks

### Phase 2 — Cell render mode *(2–3 days)*

Part 3 above. `CellsFrame`, `?mode=`, damage tracking, and a
`Surface.dirtyRows` bitset advanced per client. `wmux connect` unchanged.

Test it headlessly before any UI exists: attach in cells mode, drive a known
command, assert the reconstructed grid matches `emu`'s. That test is worth
writing carefully — it's the contract the TUI is built on.

### Phase 3 — `wmux tui` *(the payoff — 1–2 weeks)*

A full-screen bubbletea program. It is a **client**, not part of the daemon —
same as `wmux connect`, just rendering N surfaces instead of one.

Components:

- **Layout tree** — binary split tree (`Split{dir, ratio, a, b}` | `Leaf{sessionID}`),
  computed to rects on resize. This is the thing `wt.exe` was doing for you, and
  it's ~200 lines you fully control.
- **Focus + key routing** — a prefix key (tmux's `Ctrl-B`, or `Ctrl-A`) puts the
  TUI in command mode; everything else forwards to the focused surface via
  `POST /surfaces/input`. Prefix keybindings map onto the *same* protocol
  commands the CLI verbs call, per cmux's 1:1 rule.
- **Sidebar as a widget** — your existing `sidebarui.go` model becomes a pane in
  the layout instead of a separate `wt.exe` pane with a reserved console title.
  The `sidebarTitle` reservation and the fragment-profile branch in `pane-exec`
  both disappear.
- **Resize** — layout rect → `POST /surfaces/resize` per pane. You already
  handle the replay-after-resize path.

The existing themes (`midnight`/`frost`/`gradient`) carry over unchanged;
they're already lipgloss.

### Phase 4 — Retire the wt.exe layer *(cleanup)*

Once `wmux tui` is real, mark `pane`, `grid`, `focus`, `panes`, `send-keys` as
legacy. They stay useful for one genuine case — driving native Windows agents
in real WT panes when you *don't* want a multiplexer — so don't delete them
reflexively. But they stop being the primary path, and the UIA focus script,
the fragment profile installer, the console-title handshake, and
`daemon/panes.go`'s claim/TTL dance can all go with them.

That's roughly 1,600 lines and your two nastiest platform dependencies
(PowerShell UI-Automation, WT settings-file mutation) retired.

---

## Sequencing note

Phases 0–2 are all protocol/plumbing and total maybe a week. Phase 3 is the
real work. Resist starting Phase 3 first — building the TUI against the ANSI
replay path will feel faster for two days and then have to be rewritten, which
is precisely the failure mode the cmux `frontends.md` quote is warning about.

The honest scope check: Phase 3 is a terminal multiplexer UI. tmux and zellij
are large projects. Yours is smaller because the daemon, session model, PTY
ownership, VT emulation, persistence, and notification routing **already
exist and work** — you're writing a layout tree, a key router, and a renderer
over a protocol you control. But budget weeks, not days, and get Phase 2's
conformance test solid first.
