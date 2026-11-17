# Build, install and run

Use Rust 1.99 with Cargo, rustfmt and Clippy. Windows needs MSVC C++ build tools;
Linux needs a C compiler/linker. SQLite is bundled. Git and agents are optional.

```sh
cargo build --workspace --release --locked
cargo test --workspace --locked
cargo clippy --workspace --all-targets --locked -- -D warnings
cargo fmt --all -- --check
```

Keep wmux and wmuxd together (Windows .exe suffix). Checksummed native archives
include installers; verify the archive's SHA-256 before extraction.

For a source installation on Windows:

```powershell
go build -o bin/migration-go/ ./cmd/wmux ./cmd/wmuxd
./scripts/install-rust.ps1 -BinDir ./target/release -LegacyBinDir ./bin/migration-go
wmux --version
wmux
```

From an extracted Windows archive use ./install.ps1; pass -LegacyBinDir pointing
to the existing Go pair when migrating. The default install directory is
LOCALAPPDATA/Programs/wmux. Existing executables are backed up; running Go sessions
are not stopped. A new Windows configuration uses a four-pane grid. Existing
configuration is preserved. The installer does not change PATH; retain the
existing WMUX directory on PATH or add it for a fresh install.

Linux source/archive installation:

```sh
sh scripts/install-rust.sh --bin-dir target/release --dir "$HOME/.local/bin"
# Extracted archive: sh install.sh --dir "$HOME/.local/bin"
# Existing Go installation: additionally pass --legacy-bin-dir /path/to/go-pair
wmux doctor
wmux new project --cwd /path/to/repository --detach
wmux attach project
```

No installer registers a logon task/service or starts a daemon. Run wmux in
your terminal to start it hidden in the background; readiness appears there.
Ctrl+B, D detaches while sessions run. The next invocation reuses the daemon.

Rust state is ~/.wmux-rust; WMUX_HOME or --home selects an isolated directory.
The preserved Go pair lives in legacy/ and uses its original ~/.wmux state.
Use wmux legacy list/attach and other Go commands for old sessions. Notification
hook aliases retain Go behavior. No live process or terminal history is migrated.
Use wmux daemon stop --force only when intentionally stopping all Rust processes.

IPC protocol 2 requires matching Rust client/daemon binaries. Restart an older
Rust daemon before upgrading; this terminates its panes, so finish those sessions
first. Installers preserve files and do not silently kill running daemons.

See verification.md for native test platforms, screenshots and measured limits.
The original install/ scripts continue to install published Go releases.
