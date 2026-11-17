# Troubleshooting

Start with wmux doctor. It reports platform, host input mode, shell/agent
availability, configuration, IPC connectivity and an actual PTY spawn/output/
resize/termination check. Absence of TERM on Windows is normal. Run the UI in
an interactive terminal; redirected stdin is appropriate only for CLI commands.

A connection failure usually means wmuxd is not running, the wrong WMUX_HOME is
selected, or the Go binary was invoked instead of Rust. Keep wmux and wmuxd from
the same build together. The Rust transport is not compatible with Go HTTP.

Only one Rust daemon can hold a state directory. A lock conflict does not justify
deleting the lock file; stop the original daemon, or choose an isolated home.
Stale Unix sockets are removed only after acquiring that directory's OS lock.
A non-socket at the endpoint is refused.

Disconnected metadata after daemon failure is expected. Original running
processes and screen contents cannot be restored. Start a new pane. No saved PID
is reused to attach to a different process.

Small terminals automatically hide the sidebar and show the focused pane.
Toggle lower/inspector panels off when space is scarce. Focus changes preserve
split geometry. Read-only clients do not resize or send process input.

Git worktree removal fails on dirty/untracked content. Inspect and preserve it;
WMUX deliberately does not force deletion. git diff currently shows tracked,
unstaged changes; staged/untracked file previews and rich diff navigation remain
unsupported.

If a Windows build cannot replace wmuxd.exe, a daemon from that build is still
running. Stop that specific daemon with the matching --home before rebuilding.
Do not terminate unrelated user sessions.
