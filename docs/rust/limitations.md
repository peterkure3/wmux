# Remaining production requirements

This implementation must not be labeled production-complete. Remaining gates:

- Broader terminal/program matrix: SSH, WSL, Git Bash, Windows Terminal and
  representative full-screen programs. Native platform results are in verification.md.
- Live provider/version testing of the implemented Claude/Codex hook contracts.
  Gemini/Aider permission bridges and optional desktop notifications remain absent.
- Arbitrary text selection, wrapped-line search, OSC 99, extended keyboard/image
  protocols and broader terminal capability negotiation.
- Rich process-tree/resource monitoring. History search is bounded by retained
  physical rows; previews are repository-confined text capped at 64 KiB.
- Dirty-row IPC deltas and broad load/soak
  profiling. Memory configuration bounds terminal allocations, not total RSS.
- Complete journal/recovery semantics for irreversible process changes after a
  storage failure. SQLite tests cover failed read-only and disk-full commits.
- Optional OS sandbox integration. Per-user IPC, hooks and PTYs are not sandboxes.
- Signed distribution and a public release after all required platform gates pass.

Control connections acquire exclusive ownership of workspaces they modify and
release it on disconnect. Multiple read-only observers remain supported. An
operator must detach the writer before separate CLI input/layout changes; an
explicit force daemon shutdown remains available. Observers use the writer's
terminal size. Shared independent input sizes are not implemented.

Metadata persists across daemon restart. Running processes, terminal screens
and agent conversations are not restored. No automatic merge or dirty worktree
deletion exists. Go sessions remain separate behind wmux legacy.

Windows hosts that forbid process-job breakaway retain control of their child daemon; WMUX reports this detected policy limitation. Ordinary process detachment cannot override that OS restriction. A breakaway-capable terminal host is required for persistence after that host's job closes.
