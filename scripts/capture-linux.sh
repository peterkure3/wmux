#!/bin/sh
# Runs inside an ephemeral Debian container with xterm, Xvfb and imagemagick.
set -eu
mkdir -p /workspace/docs/screenshots /workspace/build/verification
export WMUX_HOME=/tmp/wmux-capture-$$
export PATH="/workspace/build/linux-target/release:$PATH"
mkdir -p "$WMUX_HOME"
printf '%s\n' 'shell = ["/bin/bash", "--noprofile", "--norc"]' > "$WMUX_HOME/config.toml"
display_number=${WMUX_CAPTURE_DISPLAY:-91}
export LANG=C.UTF-8 LC_ALL=C.UTF-8
Xvfb ":${display_number}" -screen 0 1600x1000x24 > /tmp/wmux-xvfb.log 2>&1 &
xvfb_pid=$!
export DISPLAY=":${display_number}"
# Trust only this explicitly supplied bind-mounted checkout in the test container.
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0=safe.directory
export GIT_CONFIG_VALUE_0=/workspace
trap 'wmux daemon stop --force >/dev/null 2>&1 || true; kill "$xvfb_pid" 2>/dev/null || true' EXIT INT TERM
wmux new wmux-source --cwd /workspace --detach
wmux pane split-vertical -w wmux-source
wmux pane split-horizontal -w wmux-source
wmux pane split-horizontal -w wmux-source
wmux pane layout -w wmux-source grid
# These are ordinary shell commands running in actual daemon-owned Unix PTYs.
# No agent state or output is simulated.
wmux --json pane list -w wmux-source > /tmp/panes.json
python3 - <<'PY'
import json,subprocess
panes=json.load(open('/tmp/panes.json'))
commands=[
"printf '\\033[35mNative Bash / real PTY\\033[0m\\n'; uname -srm; pwd",
"printf '\\033[36mGit workspace\\033[0m\\n'; git branch --show-current; git status --short | head -n 8",
"printf '\\033[32mRust workspace\\033[0m\\n'; rustc --version; ls crates",
"printf '\\033[33mDaemon session metadata\\033[0m\\n'; wmux list; printf '\\nSession persists when this UI detaches.\\n'"
]
for pane,command in zip(panes,commands):
    subprocess.run(['wmux','pane','send','--pane',pane,'-w','wmux-source','--enter',command],check=True)
PY
# Force a deterministic 120x40 terminal. The application receives real resize events.
xterm -u8 -xrm "XTerm*allowSendEvents: true" -geometry 120x40 -fa "DejaVu Sans Mono" -fs 12 -bg '#080d16' -fg '#eff4fc' -title WMUX-Rust-Verification -e wmux attach wmux-source &
xterm_pid=$!
sleep 4
window=$(xdotool search --name '^WMUX-Rust-Verification$' | head -n 1)
xdotool windowfocus --sync "$window"
import -window "$window" /workspace/docs/screenshots/wmux-rust-linux-real.png
# Verify keyboard-only pane focus, panel expansion, workspace switching and detach.
xdotool key ctrl+b Tab
xdotool key ctrl+b l
sleep 1
import -window "$window" /workspace/docs/screenshots/wmux-rust-linux-panels.png
xdotool key ctrl+b d
wait "$xterm_pid"
wmux --json list > /workspace/build/verification/detached-workspaces.json
python3 - <<'PY'
import json, pathlib
state=json.load(open('/workspace/build/verification/detached-workspaces.json'))
workspace=next(w for w in state['workspaces'].values() if w['name']=='wmux-source')
assert len(workspace['panes'])==4, workspace
assert all(p['state']=='Running' and p['pid'] for p in workspace['panes'].values())
pathlib.Path('/workspace/build/verification/ui-linux-status.txt').write_text('real xterm captured; keyboard detach preserved four live Bash PTYs\n')
PY
wmux doctor > /workspace/build/verification/doctor-linux.txt
