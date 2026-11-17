# Configuration reference

Copy config/default.toml to ~/.wmux-rust/config.toml, then edit it. Missing files
use defaults; invalid TOML and unknown keys fail startup with an error.
wmux config prints the path; --print-default prints all platform defaults;
--check validates. Restart the daemon after changing daemon-side settings.

| Setting | Meaning |
| --- | --- |
| shell | Argument array; executable first, no implicit shell interpolation |
| default_workspace | Name created by bare wmux when there are no workspaces |
| default_layout | single, columns, rows, grid, main-stack; initializes 1/2/4 panes |
| sidebar | Initial visibility; automatically hidden below 90 columns |
| scrollback | Requested maximum lines, 0–100000 |
| terminal_memory_mb | 16–2048 MiB terminal allocation budget; conservatively caps scrollback |
| max_panes | Maximum connected terminal buffers, 1–32; close exited buffers to reclaim slots |
| notifications | Capture bell and OSC 9/777 notifications |
| log_level | tracing filter, normally warn; RUST_LOG overrides |
| prefix | Lowercase letter used with Ctrl to enter command mode |
| forward_env | Optional list of names to inherit; omitted inherits daemon environment |
| agents | Map from custom/built-in name to executable/argument array |
| theme | RGB triples for background, foreground, purple and cyan |
| keybindings | Action-to-printable-character map; missing actions retain defaults |

When limiting forward_env, include what your platform and commands actually
need, such as PATH, HOME or USERPROFILE and SystemRoot. TERM=xterm-256color,
COLORTERM=truecolor and WMUX_PANE_ID are set by WMUX. Tokens are not added.
Environment filtering is not filesystem/network sandboxing.

The memory budget reserves half for parser/screens/snapshots and caps configured
scrollback using the worst-case 400-column width and max_panes. It is not a hard
RSS limit on the daemon or agents. Pane sizes are limited to 400x150; combined
visible screen sizes cannot exceed 120000 cells.

Theme values affect layout-independent UI colors. Agent program ANSI colors
remain the program's own colors.
