# Development and verification

The repository keeps Go and Rust independently buildable during migration.
Cargo builds do not replace Go binaries or hooks. New Rust features belong in
the smallest owning crate and cross daemon boundaries through wmux-ipc.

Run cargo fmt, cargo clippy --workspace --all-targets -- -D warnings and
cargo test --workspace. Lifecycle integration tests start real isolated wmuxd
processes, real shells and IPC subscribers; they do not require an installed AI
agent. Windows tests use PowerShell; Linux uses Bash. Git tests make a temporary
local repository and test dirty-worktree refusal.

UI tests use Ratatui TestBackend at 80x24, 120x40 and 200x60. They check contiguous
shared border geometry, clean intersections, focus invariance, panel collapse
and narrow/small terminals. Parser tests cover UTF-8 chunk boundaries, wide and
combining characters, alternate screens, SGR attributes, scrollback bounds,
notification callbacks and cursor/device replies.

scripts/verify-windows.ps1 performs native checks, release builds, doctor and
repeatable measurements. scripts/verify-linux.sh does the same in the documented
Linux container, then scripts/capture-linux.sh captures actual xterm windows.
Performance harness: cargo run -p wmux-cli --example measure --release after
starting an isolated daemon and setting WMUX_HOME. Input timing measures request
dispatch until daemon cell state contains shell output, with polling overhead;
it does not measure keyboard hardware, compositor or display response.

The retained Go suite uses go test -count=1 ./... and Linux race checks. Its
Windows ConPTY fixture now sleeps locally rather than relying on ping. The
emulator's device reply pipe is drained concurrently and closed with the surface.

Fallow currently supplies JavaScript analysis only in this repository. Its
results do not replace Rust compiler, Clippy, Cargo dependency checks, Go
verification or manual security review.

Packaging uses Python 3.11+ (stdlib tomllib) to read the workspace version. For Linux verification on Windows, use a native Docker volume for CARGO_TARGET_DIR; bind-mounted Windows debug caches can be slow and consume significant disk space. Disable incremental/debug symbols in disposable verification builds when disk space is constrained.
