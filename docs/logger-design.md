# wmux logger — design

Status: implemented (see `internal/wmuxlog/wmuxlog.go`).
This doc is the design of record; where code and doc disagree, trust code
and fix the doc.

## Goal

One structured logger for `wmuxd`, replacing scattered `log.Printf` calls
across `internal/daemon`. Foundation for the debugger project
(`docs/debugger-design.md`): panic recovery and the recent-events ring
both write through this same logger.

## Decision: log/slog, not a third-party library

`go.mod` was already at Go 1.25 with zero logging dependencies imported
anywhere (no zerolog/zap/logrus). `log/slog` is stdlib, structured, and
levelled out of the box — no new dependency needed to get any of that.

## What existed before

- Plain `log.Printf`/`log.Fatalf` in `internal/daemon/{server,session,notify,persist,surface}.go`
  and `cmd/wmuxd/main.go` — unstructured, unlevelled, no rotation.
- `wmux hook run --log FILE` (`cmd/wmux/hookrun.go`) — a separate, unrelated
  mechanism that appends raw hook payloads to a debug file. Not touched by
  this project; it solves a different problem (what an agent hook actually
  sent), not daemon logging.
- `startDaemonDetached` (`cmd/wmux/spawn_windows.go`) already redirected a
  detached wmuxd's stdout/stderr to `~/.wmux/wmuxd.log` — but the Task
  Scheduler autostart path (`installAutostart`, `cmd/wmux/autostart_windows.go`)
  never redirected anything, so a Task-Scheduler-launched wmuxd's log output
  went nowhere. Moving log-file ownership into `wmuxlog.Init` (wmuxd opens
  its own log file at startup, regardless of how it was launched) fixes
  this for free — no schtasks command-line changes needed.

## Architecture

```
wmuxlog.Init(component string) (close func(), err error)
  ├─ rotate(LogPath())        renames an oversized file to .1 first
  ├─ opens LogPath() for append
  ├─ builds a JSON slog.Handler at CurrentLevel()
  ├─ if os.Stderr is a real terminal, fans out to a second text handler
  │  too (multiHandler) — manual `go run ./cmd/wmuxd` foreground use
  │  still sees log lines without tailing the file
  └─ slog.SetDefault(...)     every call site just uses log/slog directly
```

`cmd/wmuxd/main.go` calls `wmuxlog.Init("wmuxd")` once at startup, before
`daemon.New`/`Serve`. Every `internal/daemon/*.go` call site uses
`slog.Info/Warn/Error` directly — no wrapper type, no passed-around logger
handle; `slog.SetDefault` makes that unnecessary.

## Log path and rotation

`wmuxlog.LogPath()` — `~/.wmux/wmuxd.log`, same path
`startDaemonDetached` already redirected stdout/stderr to, so both
converge on one file regardless of launch method (detached, Task
Scheduler, or manual foreground).

Rotation is size-based and manual (no third-party lib): past 10MB
(`maxLogBytes`), the file is renamed to `.1` (clobbering any older `.1`)
before being reopened. This is a debugging aid, not an audit trail — one
generation of backlog is enough.

## Level resolution

`WMUX_LOG_LEVEL` env var first, then the persisted `~/.wmux/loglevel`
file, then `info` — same env-then-file priority as `wmux theme`
(`docs/sidebar-design.md`), for the same reason: a detached or
Task-Scheduler-launched wmuxd never inherits an env var set in some other
shell. An unrecognized value at either layer falls back to `info` rather
than failing wmuxd's startup.

`wmux log level <name>` persists a level for the *next* wmuxd start (the
running daemon's level, like the sidebar's theme, is resolved once at
`Init` — no hot-reload of an already-running process).

## CLI

| command | does |
|---|---|
| `wmux log` | prints the log file path and current level |
| `wmux log path` | prints just the path |
| `wmux log tail [-n N]` | prints the last N lines (default 50) |
| `wmux log level [NAME]` | prints current level, or persists NAME |

## Future / explicitly out of scope

- Structured query/filter over the log file (`wmux log grep kind=notify`) —
  the debugger's `/debug/events/recent` ring already covers the common
  case (recent notify/session events) without parsing the file.
- Shipping logs off-machine — this is a single-machine debugging aid, not
  a telemetry pipeline.
