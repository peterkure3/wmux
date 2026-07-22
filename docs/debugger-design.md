# wmux debugger — design

Status: implemented (see `internal/daemon/debug.go`, `cmd/wmux/debugcmd.go`).
This doc is the design of record; where code and doc disagree, trust code
and fix the doc.

## Goal

A runtime inspector for `wmuxd` itself — session table, goroutine count,
recovered panics, recent event history, CPU/heap/goroutine profiles — for
diagnosing the daemon when something goes wrong. Built on top of the
logger project (`docs/logger-design.md`): every panic recorded here also
logs through `slog`, and the recent-events ring taps the same `publish`
path the logger's structured entries flow alongside.

## Decision: not a source-level Go debugger

`delve` already does breakpoint/step/variable-inspection debugging of a Go
binary well; reimplementing that would be pure duplication. "Debugger for
wmux" instead means introspecting wmuxd's own live state and history —
closer to a combination of `pprof`, a support-bundle generator, and a
crash reporter than to gdb/delve.

## The gap this closes

Before this project, a `grep -rn "recover()"` across the entire codebase
returned zero matches. Every session-managing goroutine
(`watchOutput`, `pollMetadata`, `waitExit`, `readSurface`, `reapSurface`)
and every HTTP handler ran unprotected — one panic anywhere took the whole
`wmuxd` process down, silently, with nothing left behind to diagnose what
happened. This was the single biggest reliability gap found when planning
this project, and closing it (not the profiling/dump tooling) is the part
that actually matters most.

## Architecture

```
internal/daemon/debug.go
  ├─ ring[T]                    small generic bounded FIFO (mutex-guarded)
  │    ├─ Daemon.panics          ring[proto.PanicEntry], cap 50
  │    └─ Daemon.recentEvents    ring[proto.Event], cap 200 — tapped from
  │                              the existing publish() fan-out, so it's
  │                              populated by the same path GET /events
  │                              subscribers already stream from
  │
  ├─ safeGo(source, fn)         go fn() with deferred recover(); every
  │                              session-goroutine launch site in
  │                              session.go/surface.go/persist.go now goes
  │                              through this instead of a bare `go`
  │
  └─ recoverHandler(pattern, h) wraps an HTTP handler: panic -> 500 + log
                                 + recorded to the panics ring, instead of
                                 net/http's own bare per-connection recover
                                 (which just silently closes the connection)
```

A recovered panic in a goroutine is *not* auto-retried and the affected
session's state is left as-is, deliberately — guessing at recovery after
an unexpected panic risks compounding whatever the original bug was. The
value here is that one broken goroutine no longer takes every other
session down with it, and there's now a record of what happened.

`server.go`'s `Serve()` routes every handler through `recoverHandler` via
a small `route(pattern, h)` closure, plus registers stdlib
`net/http/pprof`'s handlers under `/debug/pprof/` directly on the custom
mux (`pprof`'s functions default to `http.DefaultServeMux` via import side
effect, which a custom mux never sees — each entry point needs explicit
registration).

## Daemon endpoints

| endpoint | returns |
|---|---|
| `GET /debug/state` | `proto.DebugState`: version, uptime, goroutine count, full session table |
| `GET /debug/panics` | `[]proto.PanicEntry`: every panic recovered since this wmuxd started |
| `GET /debug/events/recent` | `[]proto.Event`: last 200 events (notify + session lifecycle) |
| `GET /debug/pprof/*` | stdlib pprof: index, cmdline, profile (cpu), symbol, trace |

## CLI

| command | does |
|---|---|
| `wmux debug state` | session table + uptime + goroutine count |
| `wmux debug panics` | every recovered panic, with stack trace |
| `wmux debug events` | recent event history as JSON lines |
| `wmux debug dump` | bundles state + panics + events + a 200-line log tail into one timestamped JSON file under `~/.wmux/dumps/` — the practically useful one, for attaching to a bug report |
| `wmux debug pprof cpu\|heap\|goroutine [seconds]` | writes the profile to a `.prof` file in the cwd, prints the `go tool pprof` command to read it |

`dump`'s log tail is read directly from `wmuxlog.LogPath()` on disk
(rather than round-tripped through wmuxd), so a dump still captures recent
log lines even if the daemon itself isn't responding to `/debug/state`.

## Known limitation: WSL PID visibility

A WSL-registered session's tracked PID is the Windows-side `wsl.exe`
frontend process, not the real in-distro PID (`internal/daemon/session.go`,
`pidVisible`) — `wmux debug state`'s session table inherits this same
boundary. Deeper in-distro process inspection is explicitly out of scope
for v1.

## Future / explicitly out of scope for v1

- In-distro (WSL) process/goroutine inspection — the Windows-side daemon
  has no visibility past the `wsl.exe` frontend PID.
- `wmux debug attach <session>` — a read-only live view of a session via
  the existing surface-attach NDJSON protocol (`docs/sidebar-design.md`'s
  `wmux connect` machinery). Deliberately not built alongside the rest of
  this project to avoid landing a half-finished feature; the plumbing
  (`AttachSurface`/`DetachSurface`) already exists if this gets picked up
  later.
- Automatic restart/self-healing of a goroutine after a recovered panic —
  `safeGo` only stops the crash from propagating; it doesn't attempt to
  resume whatever the goroutine was doing.
