# Rust migration audit — 2026-11-17

The starting checkout is commit `f637b8d` on main, with substantial existing
uncommitted Go hardening, tests, documentation, CI changes and GUI deletions.
Those changes are preserved. Remote branches include linux-port,
refactor/plan-b-multiplexer, release-tarball-fix and update-downgrade-guard.

## Existing implementation

This is not an empty repository. `internal/daemon/surface.go` already owns
ConPTY/Unix PTYs and a Go VT emulator; cell clients receive styled terminal
cells. `internal/client` carries authenticated HTTP/SSE; `internal/tui` is
Bubble Tea; `internal/layout` implements nested splits. Existing modules also
provide agent hook wiring, OSC notifications, process identity, Windows Job
Objects, JSON persistence, installers and checksummed Go releases.

The established dependency direction is cli → tui → layout, with daemon access
through client. Platform code is separated using filename suffixes. Existing
tests cover layout, input encoding, authentication, persistence failures,
notification stress, cell rendering, spawn races and binary lifecycle.

Older migration notes incorrectly describe the current daemon as lacking VT
parsing. They are historical proposals, not authoritative implementation facts.
There is no Rust workspace or Rust implementation at the start of this task.

## Sandbox and cmux

The supplied ZIP was found in the user's Downloads and extracted under ignored
`build/reference`. Its README explicitly identifies simulated terminal output
and an uncompiled Ratatui preview. It includes running-workspace and palette
PNGs; `wmux-zero-gap-live-clean.png` is absent. The supplied running-workspace
image has visible pane gaps; the newer explicit zero-gap requirement takes
precedence. Preserve the near-black, purple/cyan system, full-height sidebar,
workspace navigation and optional diagnostics/inspector with real PTY content.

The public wmux and cmux repositories were inspected. A local cmux checkout was
also inspected for license and subsystem organization. cmux separates workspace
navigation, socket automation and agent notifications; it uses Ghostty and native
macOS UI. Its license is GPL/commercial. No cmux implementation is copied.
Reference: https://github.com/manaflow-ai/cmux and https://github.com/peterkure3/wmux.

## Repository intelligence limits

Fallow was not installed on PATH. `npx --yes fallow --summary` ran after approval
for npm cache access. It analyzed only one JavaScript file and found no entry
points; its dead-file finding is not evidence that the Go code is unused.
Fallow does not provide useful Go/Rust dependency coverage here. Source inventory,
the existing CODEBASE_INDEX.md, manifests and package-specific tests are used
instead. No existing files are deleted based on its report.

## Migration boundaries and risks

Add independent Rust crates for model/config/layout, terminal parsing, PTYs,
local IPC, security, SQLite storage, agents, Git, daemon and TUI/CLI. Retain Go
commands, hooks, HTTP and installers until an explicit compatibility replacement
exists. Rust uses a separate `.wmux-rust` state directory; no unsafe automatic
state conversion or port sharing. UI detach must never own process lifetime.
Restored metadata must mark former processes disconnected, never resurrect PIDs.

Highest-risk areas: Windows pipe ACLs, process-tree cleanup, terminal negotiation,
output backpressure, cell width/combining marks, multiclient resizing, and
cross-platform failure cleanup. Verify real PTYs and executable lifecycle, not
only mocked interfaces. Windows and Linux results must be reported separately.
