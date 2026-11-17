package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/peterkure3/wmux/internal/proto"
)

type brokenStream struct{}

func (brokenStream) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestWatchLargeEscapedNotification(t *testing.T) {
	// JSON expands these control characters sixfold, exceeding both the old
	// CLI limit and a naive 1 MiB scanner limit.
	message := strings.Repeat("\x01", 200000)
	data, err := json.Marshal(proto.Event{Type: proto.EventNotify, Notify: &proto.NotifyEvent{SessionID: "test", Body: message, Dropped: 2}})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = watchNotifications(strings.NewReader("data: "+string(data)+"\n\n"), &out)
	if err == nil || !strings.Contains(err.Error(), "disconnected") {
		t.Fatalf("EOF must be reported: %v", err)
	}
	if !strings.Contains(out.String(), message) || !strings.Contains(out.String(), "(+2 earlier missed)") {
		t.Fatal("large notification was lost or truncated")
	}
}

func TestWatchReadFailure(t *testing.T) {
	err := watchNotifications(brokenStream{}, io.Discard)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read error lost: %v", err)
	}
}

func TestWatchOversizedLine(t *testing.T) {
	err := watchNotifications(strings.NewReader(strings.Repeat("x", proto.MaxEventLineBytes+1)), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "token too long") {
		t.Fatalf("oversized frame error: %v", err)
	}
}
