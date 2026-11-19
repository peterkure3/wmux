# Rust architecture

The migration adds eleven crates. The retained Go implementation and its HTTP
protocol remain independently buildable. Rust does not read or migrate Go state.

| Crate | Owns |
| --- | --- |
| wmux-core | Serializable models, validated TOML, pure nested split geometry |
| wmux-terminal | vt100/vte parsing, per-pane cell buffers, scrollback, device replies |
| wmux-pty | portable-pty, ConPTY/Unix PTY, bounded input and output, process cleanup |
| wmux-security | Windows SID/ACLs and Unix owner/permissions checks |
| wmux-ipc | Versioned length-prefixed JSON over Unix sockets or named pipes |
| wmux-storage | SQLite transaction commits and restart policy |
| wmux-agents | Command adapters and executable detection |
| wmux-git | Git CLI metadata, diffs and safe worktree operations |
| wmux-daemon | Process ownership, command serialization, event fanout, persistence |
| wmux-tui | Ratatui cell rendering and Crossterm input; an IPC client |
| wmux-cli | Commands, background startup, diagnostics, custom approval bridge |

The daemon owns every PTY, parser and child. Detaching a UI drops only its
subscription. A serialized actor runs on a blocking worker so native spawn,
SQLite and PTY control calls do not block Tokio's executor. PTY readers feed a
bounded 256-message output queue, with chunks no larger than 16 KiB. Each pane
has a 64-message bounded input queue. Unix reads and writes use a nonblocking,
close-on-exec duplicate of the master registered with Tokio AsyncFd. Two
lightweight tasks handle readiness and bounded-channel backpressure. Pane
destruction aborts both tasks, including pending sends/writes, so descriptors
cannot outlive the pane. Registration is prepared before child creation.
Windows retains its native blocking ConPTY reader and uses blocking writer
workers only while there is data.

Clients receive an initial snapshot and coalesced updates, limited to about
30 Hz per subscriber. Identical cells are run-length encoded per row. Ratatui
diffs successive cell buffers, so unchanged display cells are not repainted.
IPC still sends complete compressed snapshots rather than dirty-row deltas.
The terminal cell budget is bounded to 120000 visible cells across panes.
Scrollback is limited by both line count and a conservative share of the
terminal memory budget. These are terminal allocation budgets, not an OS RSS cap.

Requests use a 32 MiB frame limit, version checks, timeouts, and a 64-client
connection cap. Unix endpoints are inside a private 0700 owned directory, with
a 0600 socket. Windows names include the current SID and canonical state-path
hash; pipe DACLs grant access only to that user and remote clients are rejected.
Windows runtime state paths also receive protected user-only inheritable DACLs.
Same-user processes are trusted IPC clients. PTYs do not sandbox those processes.

SQLite commits metadata synchronously and atomically. Terminal output and raw
input are never written to application logs. Notification history is capped.
No runtime token, session file or private database belongs in Git.

On detach, shells keep running. On clean daemon shutdown, live panes are stopped.
On restart, stored Running/Starting processes become Disconnected and PIDs are
cleared. Pending approvals expire. Screen contents are not recovered after daemon
loss, and processes are never relaunched automatically. Windows Job Objects kill
descendants when the daemon loses its handles. Unix closes its PTYs on process
death; OS signal semantics do not constitute a sandbox or guarantee that a
deliberately daemonized descendant is killed.

Multiple observers can attach with --read-only. Control connections acquire write ownership for each workspace they modify; disconnect releases ownership. Competing input, resize and layout writes are rejected. Observers render the writer's dimensions. IPC protocol 2 requires matching binaries.
