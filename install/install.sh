#!/bin/sh
# Installs wmux on Linux: downloads a published GitHub release, places
# wmux/wmuxd under ~/.local/bin (or --dir), and (by default) registers
# wmuxd to start at logon via a systemd --user unit.
#
# No root needed — everything is per-user: the install directory, the
# PATH it lands on (usually already there: ~/.local/bin is on the XDG
# default PATH), and the systemd --user unit `wmux autostart install`
# writes. Same release archive, same SHA256SUMS verification convention
# as `wmux update --release` (internal/cli/release.go) and
# install/install.ps1 — this script exists for the bootstrap case where
# wmux doesn't exist yet to run that command with.
#
# POSIX sh: runs under dash, bash, zsh, busybox ash. No bashisms.

set -eu

# Matches releaseRepo in internal/cli/release.go: the GitHub repo
# (peterkure3, with a 3) deliberately differs from go.mod's module path
# (peterkure). Keep them in sync.
REPO="peterkure3/wmux"

# Default daemon API address, matching cmd/wmuxd/main.go's -addr.
HEALTHZ_URL="http://127.0.0.1:47823/healthz"
# systemd --user unit name, matching autostartUnitName in
# internal/cli/autostart_linux.go.
WMUXD_UNIT="wmux-wmuxd.service"

VERSION="latest"
DIR="${HOME}/.local/bin"
AUTOSTART=1

usage() {
    cat <<EOF
Usage: install.sh [options]

Installs wmux from a GitHub release into DIR (default: ~/.local/bin) and
registers wmuxd to start at logon.

Options:
  --version TAG   release tag to install (default: latest)
  --dir DIR       install directory (default: ~/.local/bin)
  --no-autostart  don't register wmuxd autostart (run
                  'wmux autostart install' yourself later)
  -h, --help      show this help

Examples:
  curl -fsSL https://raw.githubusercontent.com/peterkure3/wmux/main/install/install.sh | sh
  install.sh --version v0.2.1 --dir ~/bin --no-autostart
EOF
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --version)
            [ "$#" -ge 2 ] || { usage >&2; exit 1; }
            VERSION="$2"
            shift 2
            ;;
        --dir)
            [ "$#" -ge 2 ] || { usage >&2; exit 1; }
            DIR="$2"
            shift 2
            ;;
        --no-autostart)
            AUTOSTART=0
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

# fetch_url URL DEST — curl, falling back to wget.
fetch_url() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$1" -o "$2"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO "$2" "$1"
    else
        echo "error: need curl or wget to download the release" >&2
        exit 1
    fi
}

# healthz_up probes the running daemon's /healthz (the one endpoint that
# is deliberately unauthenticated — see internal/daemon/auth.go).
healthz_up() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsS -o /dev/null "$HEALTHZ_URL" >/dev/null 2>&1
    else
        wget -q -O /dev/null "$HEALTHZ_URL" >/dev/null 2>&1
    fi
}

if [ "$VERSION" = "latest" ]; then
    echo "Resolving latest release..."
    json=$(mktemp)
    fetch_url "https://api.github.com/repos/$REPO/releases/latest" "$json"
    VERSION=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$json" | head -n 1)
    rm -f "$json"
    if [ -z "$VERSION" ]; then
        echo "error: could not resolve the latest release tag from GitHub" >&2
        exit 1
    fi
fi

echo "Installing wmux $VERSION to $DIR"

ASSET="wmux_${VERSION}_linux-amd64.tar.gz"
BASE="https://github.com/$REPO/releases/download/$VERSION/"

STAGING=$(mktemp -d "${TMPDIR:-/tmp}/wmux-install.XXXXXX")
trap 'rm -rf "$STAGING"' EXIT INT TERM

cd "$STAGING"

echo "Downloading $ASSET..."
fetch_url "$BASE$ASSET" "$ASSET"

echo "Verifying SHA256SUMS..."
fetch_url "$BASE"SHA256SUMS "SHA256SUMS"
# grep the asset's own line out of the sums file and let sha256sum -c
# re-verify it against the file right here; a missing entry fails as
# hard as a mismatch (grep exits non-zero under set -e).
grep -E "^[0-9a-f]{64}[[:space:]]+${ASSET}$" SHA256SUMS | sha256sum -c -

echo "Extracting..."
tar -xzf "$ASSET"
if [ ! -x ./wmux ] || [ ! -x ./wmuxd ]; then
    echo "error: release archive is missing wmux/wmuxd" >&2
    exit 1
fi

# An already-running wmuxd holds its own binary in use (same reason
# internal/cli/update.go stops it before swapping) — stop it first if
# this is a reinstall over a live daemon.
if healthz_up; then
    echo "Stopping running wmuxd..."
    # Prefer the clean systemd stop; fall back to a hard kill for a
    # daemon running detached (no systemd session, e.g. WSL without
    # systemd enabled).
    if command -v systemctl >/dev/null 2>&1 &&
        systemctl --user is-active --quiet "$WMUXD_UNIT" 2>/dev/null; then
        systemctl --user stop "$WMUXD_UNIT" || true
    fi
    if healthz_up; then
        pkill -f wmuxd || true
        sleep 1
    fi
fi

echo "Installing binaries..."
install -d "$DIR"
install -m 0755 "$STAGING/wmux" "$DIR/wmux"
install -m 0755 "$STAGING/wmuxd" "$DIR/wmuxd"

case ":$PATH:" in
    *":$DIR:"*) ;;
    *)
        echo "warning: $DIR is not on your PATH."
        echo "  add it to your shell profile, e.g.:"
        echo "    echo 'export PATH=\"$DIR:\$PATH\"' >> ~/.profile"
        ;;
esac

if [ "$AUTOSTART" -eq 1 ]; then
    echo "Registering wmuxd autostart..."
    if "$DIR/wmux" autostart install; then
        :
    else
        echo "warning: could not register autostart (is systemd --user available?)."
        echo "  run '$DIR/wmux autostart install' later, or start wmuxd manually: $DIR/wmuxd"
    fi
else
    echo "Skipped autostart (--no-autostart) -- run '$DIR/wmux autostart install' later to enable it."
fi

echo ""
echo "wmux $VERSION installed to $DIR"
echo "Run 'wmux version' to confirm; 'wmux autostart status' checks the daemon."
