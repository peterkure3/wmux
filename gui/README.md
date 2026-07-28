# wmux (Kotlin/Compose Desktop client)

Native Windows GUI multiplexer for `peterkure3/wmux`'s daemon (`wmuxd`).
This is **not** a standalone terminal emulator — it's a client over
wmuxd's local HTTP API: `wmuxd` owns every ConPTY ("surface"), does the VT
parsing, and streams the result as structured cell/row data
(`GET /surfaces/attach?mode=cells`); this app just renders that and
forwards keystrokes back (`POST /surfaces/input`). Session list and
lifecycle (`GET /sessions`, `GET /events` SSE, `POST /surfaces`,
`POST /sessions/close`) come from the same daemon. See wmuxd's own
`docs/sidebar-design.md` and `internal/proto/proto.go` for the wire
contract this app implements against.

Packaged as a native MSI/EXE with jpackage (no separate JVM install
required by the end user).

## Modules

- **core** — `net/WmuxDaemonClient.kt` (the daemon HTTP+SSE+NDJSON client)
  and `net/Proto.kt` (wire types mirroring wmuxd's `internal/proto`),
  `SurfacePane.kt` (per-surface screen state, fed by cells frames), and
  `Session.kt` (the client-local, unpersisted split-pane layout tree —
  wmuxd has no concept of on-screen arrangement, only of sessions). Also
  `win/Elevation.kt` (JNA-based UAC "runas" launch, unrelated to the
  daemon). No UI or CLI deps.
- **cli** — Clikt-based `wmux` command (`new`, `list`, `kill`), a thin
  wrapper over `WmuxDaemonClient`. `wmux attach`-style TTY passthrough
  stays the Go CLI's job; this one only talks to the daemon over HTTP.
- **ui** — Compose Desktop layout: session-list sidebar driven by
  `GET /sessions` + `GET /events`, recursive split-pane renderer over
  attached surfaces, cells-frame-to-`AnnotatedString` rendering, bottom
  status bar showing daemon connection state.
- **app** — final executable. `Main.kt` routes to the CLI when args are
  passed, or launches the Compose window (`ui.WmuxApp`) otherwise. Owns
  the jpackage/MSI packaging config; no UI logic of its own.

## Setup

Requires a running `wmuxd` (see the main `wmux` repo's README) — this app
waits at a "wmuxd isn't reachable" screen with a retry button until one
answers `GET /healthz`, rather than auto-launching it.

```powershell
git clone <repo>
cd wmux
.\gradlew.bat build
.\gradlew.bat :app:run
```

`WMUX_ADDR` and `WMUX_TOKEN_FILE` override the daemon address and auth
token file location, same env vars the Go CLI reads.

## Next steps

- Multi-pane grid parity with the Go TUI's `wmux grid N` (today: sidebar +
  splits opened one at a time via "+ new").
- Agent-profile shortcuts (`--claude`/`--codex` equivalents) — currently
  `new`'s command is a raw string; wmuxd's own agent-profile resolution
  (`internal/agentprofile`) isn't mirrored client-side by design (see the
  migration plan's Phase 2).
- OWASP pass on the "new pane" dialog's cwd/command inputs once real
  end-to-end usage exercises them against a live daemon.
