#!/bin/sh
# Install a locally verified native pair; never create a service or start it.
set -eu
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
bin_dir=$script_dir
install_dir=${HOME}/.local/bin
legacy_dir=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --bin-dir) bin_dir=$2; shift 2 ;;
        --dir) install_dir=$2; shift 2 ;;
        --legacy-bin-dir) legacy_dir=$2; shift 2 ;;
        *) echo 'Usage: install.sh --bin-dir DIR --dir DIR [--legacy-bin-dir DIR]' >&2; exit 2 ;;
    esac
done
for name in wmux wmuxd; do
    [ -x "$bin_dir/$name" ] || { echo "Missing executable: $bin_dir/$name" >&2; exit 1; }
done
version=$("$bin_dir/wmux" --version)
case "$version" in 'wmux 0.'*) ;; *) echo 'Source is not Rust WMUX' >&2; exit 1 ;; esac
if [ -f "$install_dir/wmux" ] && [ ! -f "$install_dir/legacy/wmux" ] && [ -z "$legacy_dir" ]; then
    echo 'Existing installation: supply --legacy-bin-dir to preserve the Go pair' >&2
    exit 1
fi
if [ -n "$legacy_dir" ]; then
    for name in wmux wmuxd; do [ -x "$legacy_dir/$name" ] || { echo "Missing legacy $name" >&2; exit 1; }; done
fi
mkdir -p "$install_dir"
stage=$(mktemp -d "$install_dir/.wmux-install.XXXXXX")
trap 'rm -f -- "$stage/wmux" "$stage/wmuxd"; rmdir -- "$stage" 2>/dev/null || true' EXIT HUP INT TERM
for name in wmux wmuxd; do cp -- "$bin_dir/$name" "$stage/$name"; chmod 755 "$stage/$name"; done
if [ -n "$legacy_dir" ]; then
    mkdir -p "$install_dir/legacy"
    for name in wmux wmuxd; do
        if [ -f "$install_dir/legacy/$name" ]; then cp -p -- "$install_dir/legacy/$name" "$install_dir/legacy/$name.before-update"; fi
        cp -- "$legacy_dir/$name" "$install_dir/legacy/$name.new"
        chmod 755 "$install_dir/legacy/$name.new"
        mv -- "$install_dir/legacy/$name.new" "$install_dir/legacy/$name"
    done
fi
for name in wmux wmuxd; do
    if [ -f "$install_dir/$name" ]; then cp -p -- "$install_dir/$name" "$install_dir/$name.before-update"; fi
    mv -- "$stage/$name" "$install_dir/$name"
done
echo "Installed $version in $install_dir; add this directory to PATH if needed."
echo 'Run wmux in your terminal; no daemon was started or logon service registered.'
echo 'Use wmux legacy list / attach for preserved Go sessions.'
