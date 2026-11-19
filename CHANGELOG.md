# Changelog

## 0.3.1

- Unix PTY reads and writes now use Tokio readiness with nonblocking,
  close-on-exec descriptors. No dedicated reader thread or blocking writer
  worker is allocated per Unix pane.
- Cancelled or closed panes release I/O tasks and descriptors even when the
  output queue or kernel input buffer is full.
- Linux regression coverage exercises sixteen real PTYs on one Tokio thread,
  full-size binary transfers, bounded queues, cancellation and descriptor cleanup.
- Windows ConPTY transport and daemon startup policy are unchanged.
- Keep executable detection compatible with Rust 1.100 Clippy and derive
  archive versions from the Rust workspace manifest.
