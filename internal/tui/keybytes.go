// Key-to-bytes translation for `wmux tui`'s focused-pane input forwarding.
//
// bubbletea parses raw terminal input into tea.KeyMsg and does not expose
// the original bytes (verified against its public API) — there is no
// passthrough option. Forwarding a keypress to a remote pty therefore
// means reconstructing the byte sequence a real terminal would have sent,
// the same thing every terminal-in-terminal tool that builds on a parsed
// input layer ends up doing.
package tui

import tea "github.com/charmbracelet/bubbletea"

// keyMsgToBytes reconstructs the byte sequence a directly-attached
// terminal would have sent for k, or nil for a key this doesn't know how
// to reconstruct (most F-keys, ctrl+arrow, ctrl+shift combos — real but
// rare in day-to-day agent-session use; dropped rather than guessed at).
func keyMsgToBytes(k tea.KeyMsg) []byte {
	var b []byte
	switch k.Type {
	case tea.KeyRunes:
		b = []byte(string(k.Runes))
	case tea.KeySpace:
		b = []byte(" ")
	case tea.KeyUp:
		b = []byte("\x1b[A")
	case tea.KeyDown:
		b = []byte("\x1b[B")
	case tea.KeyRight:
		b = []byte("\x1b[C")
	case tea.KeyLeft:
		b = []byte("\x1b[D")
	case tea.KeyHome:
		b = []byte("\x1b[H")
	case tea.KeyEnd:
		b = []byte("\x1b[F")
	case tea.KeyPgUp:
		b = []byte("\x1b[5~")
	case tea.KeyPgDown:
		b = []byte("\x1b[6~")
	case tea.KeyDelete:
		b = []byte("\x1b[3~")
	case tea.KeyInsert:
		b = []byte("\x1b[2~")
	case tea.KeyShiftTab:
		b = []byte("\x1b[Z")
	default:
		// Every C0 control key — KeyCtrlA..KeyCtrlZ, KeyEnter (CR),
		// KeyTab (HT), KeyEsc, KeyBackspace (DEL) — has a KeyType
		// numerically equal to its own control-code byte (0-31, or 127
		// for DEL); bubbletea's "other keys" (arrows, F-keys, ...) are
		// negative, so this never misfires against those.
		if k.Type >= 0 && (k.Type <= 31 || k.Type == 127) {
			b = []byte{byte(k.Type)}
		}
	}
	if b == nil {
		return nil
	}
	if k.Alt {
		// Conventional xterm meta-key encoding: ESC prefix before the
		// key's own bytes.
		return append([]byte{0x1b}, b...)
	}
	return b
}
