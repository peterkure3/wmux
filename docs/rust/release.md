# Release procedure

Rust 0.3.0 is an implementation milestone, not a declared production release.
Do not publish merely because binaries compile.

1. Run native Windows x86_64 and Linux x86_64 tests, Clippy, formatting, doctor,
   interactive shell checks, failure tests and screenshot verification.
2. Run the native Linux ARM64 CI job before advertising ARM64 support.
3. Review docs/rust/limitations.md and resolve release-blocking terminal,
   security, provider integration and performance gaps.
4. Build cargo build --workspace --release --locked on each target.
5. Package with scripts/package-rust.py --platform PLATFORM --bin-dir PATH
   --output build/rust-artifacts. Verify each .sha256 file.
6. Review archives and install in a clean PATH. Keep the two binaries together.
7. Publish only after explicit release authorization and all platform gates pass.

The Rust CI workflow uploads reviewable artifacts and never publishes a release.
Existing v* tag automation remains the legacy Go release workflow. Do not use
a Go release tag to imply Rust release acceptance. Windows signing and Linux
distribution packages are not configured.
