#!/bin/sh
set -eu
mkdir -p build/verification
printf 'module wmux-build-output\ngo 1.26.6\n' > build/go.mod
if [ -d target ]; then printf 'module wmux-rust-build-output\ngo 1.26.6\n' > target/go.mod; fi
rustup component add clippy rustfmt
cargo test --workspace --locked > build/verification/rust-tests-linux.log 2>&1 || { cat build/verification/rust-tests-linux.log; exit 1; }
cargo clippy --workspace --all-targets --locked -- -D warnings
cargo build --workspace --release --bins --examples --locked
export WMUX_HOME=/tmp/wmux-measure
export PATH="/workspace/build/linux-target/release:$PATH"
mkdir -p "$WMUX_HOME"
printf '%s\n' 'shell = ["/bin/bash", "--noprofile", "--norc"]' > "$WMUX_HOME/config.toml"
wmux daemon start
/workspace/build/linux-target/release/examples/measure > build/verification/performance-linux.json

wmux daemon stop --force
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq xvfb xterm xdotool imagemagick fonts-dejavu-core python3
wmux daemon start
python3 scripts/measure-linux.py
wmux daemon stop --force
sh scripts/capture-linux.sh

python3 scripts/package-rust.py --platform linux-x86_64 --bin-dir build/linux-target/release --output build/rust-artifacts
printf 'passed\\n' > build/verification/linux-verification-status.txt
