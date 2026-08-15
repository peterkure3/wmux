#!/bin/sh
# Uninstalls wmux on Linux: removes the systemd --user autostart entry,
# stops wmuxd, deletes the installed binaries, and (with --purge) the
# ~/.wmux data dir.
#
# POSIX sh: runs under dash, bash, zsh, busybox ash. No bashisms.

set -eu

DIR="${HOME}/.local/bin"
PURGE=0

HEALTHZ_URL="http://127.0.0.1:47823/healthz"
WMUXD_UNIT="wmux-wmuxd.service"

usage() {
    cat <<EOF
Usage: uninstall.sh [options]

Removes wmux from DIR (default: ~/.local/bin).

Options:
  --dir DIR       install directory wmux was placed in
                  (default: ~/.local/bin)
  --purge         also delete ~/.wmux (session state, logs, theme and
                  log-level settings, debug dumps)
  -h, --help      show this help
EOF
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --dir)
            [ "$#" -ge 2 ] || { usage >&2; exit 1; }
            DIR="$2"
            shift 2
            ;;
        --purge)
            PURGE=1
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            usage >&2
            exit 1
            ;;
    esac
done

# healthz_up probes the running daemon's /healthz (the one endpoint that
# is deliberately unauthenticated — see internal/daemon/auth.go).
healthz_up() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsS -o /dev/null "$HEALTHZ_URL" >/dev/null 2>&1
    else
        wget -q -O /dev/null "$HEALTHZ_URL" >/dev/null 2>&1
    fi
}

if [ -x "$DIR/wmux" ]; then
    echo "Removing autostart entry..."
    "$DIR/wmux" autostart uninstall || true
else
    echo "warning: $DIR/wmux not found -- removing the systemd unit directly."
    if command -v systemctl >/dev/null 2>&1; then
        systemctl --user disable --now "$WMUXD_UNIT" >/dev/null 2>&1 || true
    fi
fi

if healthz_up; then
    echo "Stopping running wmuxd..."
    if command -v systemctl >/dev/null 2>&1 &&
        systemctl --user is-active --quiet "$WMUXD_UNIT" 2>/dev/null; then
        systemctl --user stop "$WMUXD_UNIT" || true
    fi
    if healthz_up; then
        pkill -f wmuxd || true
        sleep 1
    fi
fi

if [ -d "$DIR" ]; then
    echo "Removing $DIR..."
    rm -rf "$DIR"
fi

if [ "$PURGE" -eq 1 ]; then
    if [ -d "${HOME}/.wmux" ]; then
        echo "Purging ${HOME}/.wmux (session state, logs, settings)..."
        rm -rf "${HOME}/.wmux"
    fi
fi

echo ""
echo "wmux uninstalled."
if [ "$PURGE" -eq 0 ]; then
    echo "Session state, logs, and settings were left at ${HOME}/.wmux -- rerun with --purge to remove them too."
fi
