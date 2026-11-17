# Keyboard shortcuts

Normal input goes to the focused terminal. Ctrl+B enters one-command mode.
Press the prefix twice to send Ctrl+B to the process. Change prefix in TOML.
Printable command bindings are configurable; digits and navigation keys have
fixed meanings. Command-mode defaults:

| Key | Action |
| --- | --- |
| Tab | Next pane |
| S / V | Split rows / columns |
| G | Grid |
| Z | Zoom focused pane |
| X | Close pane after Y confirmation |
| R / W | Rename / swap with next pane |
| Left / Right | Shrink/grow nearest split divider |
| N | New workspace prompt |
| [ / ] or 1–9 | Previous/next or indexed workspace |
| B | Sidebar |
| L / T / + / - | Lower panel toggle/view/height |
| I | Git inspector / return from file preview |
| F | Repository-relative file preview prompt |
| PageUp / PageDown | Scroll inspector (after prefix) |
| A | Inspect oldest pending approval; Y approve, R reject |
| C | Copy mode |
| P | Selectable command palette (Up/Down, Enter) |
| ? | Help |
| D / Q | Detach UI |

In copy mode, Up/Down and PageUp/PageDown move through retained scrollback. / searches physical history rows; n finds an older match, N a newer match. Y copies the visible buffer via OSC 52; Esc/Q returns to live input. Arbitrary mouse text selection and matching across soft-wrapped rows are not implemented.

Mouse clicks focus panes; SGR mouse reports are forwarded when enabled by the
program. Scroll wheel enters scrollback when no application mouse mode is
active. Unsupported mouse encodings are not translated. Bracketed paste is
forwarded when requested. Paste is capped at 60 KiB per event.

Some terminals cannot distinguish certain Ctrl/Shift combinations or modified
function keys. These depend on the host's input encoding. Extended Kitty key
protocol and terminal image protocols are not implemented.
