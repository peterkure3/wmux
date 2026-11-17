package daemon

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
)

// The emulator's input side is an io.Pipe. Device queries must be drained
// concurrently or Emulator.Write blocks while holding the surface mutex.
func TestTerminalDeviceRepliesDoNotBlockOutput(t *testing.T) {
	emu := vt.NewEmulator(80, 24)
	var replies bytes.Buffer
	copied := make(chan error, 1)
	go func() { _, err := io.Copy(&replies, emu); copied <- err }()
	written := make(chan struct{})
	go func() { emu.WriteString("\x1b[6n"); close(written) }()
	select {
	case <-written:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal device query blocked output parsing")
	}
	if err := emu.InputPipe().(io.Closer).Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-copied; err != nil {
		t.Fatal(err)
	}
	if got := replies.String(); got != "\x1b[1;1R" {
		t.Fatalf("cursor reply = %q", got)
	}
}
