package main

import (
	"bytes"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestKeyMsgToBytes(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyMsg
		want []byte
	}{
		{"rune", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}, []byte("a")},
		{"multi-rune (IME)", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("你")}, []byte("你")},
		{"space", tea.KeyMsg{Type: tea.KeySpace}, []byte(" ")},
		{"enter", tea.KeyMsg{Type: tea.KeyEnter}, []byte{'\r'}},
		{"backspace", tea.KeyMsg{Type: tea.KeyBackspace}, []byte{127}},
		{"tab", tea.KeyMsg{Type: tea.KeyTab}, []byte{'\t'}},
		{"esc", tea.KeyMsg{Type: tea.KeyEsc}, []byte{27}},
		{"ctrl+c", tea.KeyMsg{Type: tea.KeyCtrlC}, []byte{3}},
		{"ctrl+a", tea.KeyMsg{Type: tea.KeyCtrlA}, []byte{1}},
		{"ctrl+z", tea.KeyMsg{Type: tea.KeyCtrlZ}, []byte{26}},
		{"up", tea.KeyMsg{Type: tea.KeyUp}, []byte("\x1b[A")},
		{"down", tea.KeyMsg{Type: tea.KeyDown}, []byte("\x1b[B")},
		{"left", tea.KeyMsg{Type: tea.KeyLeft}, []byte("\x1b[D")},
		{"right", tea.KeyMsg{Type: tea.KeyRight}, []byte("\x1b[C")},
		{"home", tea.KeyMsg{Type: tea.KeyHome}, []byte("\x1b[H")},
		{"end", tea.KeyMsg{Type: tea.KeyEnd}, []byte("\x1b[F")},
		{"delete", tea.KeyMsg{Type: tea.KeyDelete}, []byte("\x1b[3~")},
		{"alt+a", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a"), Alt: true}, []byte{0x1b, 'a'}},
		{"alt+enter", tea.KeyMsg{Type: tea.KeyEnter, Alt: true}, []byte{0x1b, '\r'}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := keyMsgToBytes(c.key)
			if !bytes.Equal(got, c.want) {
				t.Errorf("keyMsgToBytes(%s) = %v, want %v", c.key.String(), got, c.want)
			}
		})
	}
}

// TestKeyMsgToBytesUnmappedKeysReturnNil documents the deliberate gap:
// unmapped keys are dropped, not guessed at.
func TestKeyMsgToBytesUnmappedKeysReturnNil(t *testing.T) {
	for _, k := range []tea.KeyType{tea.KeyF1, tea.KeyCtrlUp, tea.KeyShiftLeft} {
		if got := keyMsgToBytes(tea.KeyMsg{Type: k}); got != nil {
			t.Errorf("keyMsgToBytes(%v) = %v, want nil (unmapped)", k, got)
		}
	}
}
