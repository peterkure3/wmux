package daemon

import (
	"bytes"
	"strings"
	"testing"
)

// TestTrimPending covers the invariant the optimization rests on:
// oscNotifyRe's body class excludes ESC, so a match can never span one,
// so an incomplete match can only begin at the LAST ESC in the buffer.
func TestTrimPending(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no escape at all is entirely dead weight", "plain agent output", ""},
		{"empty stays empty", "", ""},
		{"partial sequence is preserved verbatim", "\x1b]9;half", "\x1b]9;half"},
		{"junk before a partial is discarded", "noise\x1b]9;half", "\x1b]9;half"},
		{"only the last escape can still match", "\x1b]9;dead\x1b]99;live", "\x1b]99;live"},
		{"escape at the very end survives", "output\x1b", "\x1b"},
		{
			"an over-long partial is dropped rather than grown forever",
			"\x1b]99;title=" + strings.Repeat("x", maxPending+1),
			"",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := trimPending([]byte(c.in))
			if !bytes.Equal(got, []byte(c.want)) {
				t.Errorf("trimPending(%.40q) = %.40q, want %.40q", c.in, got, c.want)
			}
		})
	}
}

// TestTrimPendingPreservesDetection is the behavioural guarantee: trimming
// must never cost us a notification that arrives split across reads.
func TestTrimPendingPreservesDetection(t *testing.T) {
	seq := "\x1b]99;title=build;message=done\x07"
	for cut := 1; cut < len(seq); cut++ {
		d := New("", "")
		sess := &Session{ID: "s"}
		addSession(d, sess)

		var pending []byte
		for _, chunk := range []string{
			strings.Repeat("noise ", 200), // must be discarded entirely
			seq[:cut],
			seq[cut:],
		} {
			pending = append(pending, chunk...)
			pending = trimPending(d.scanNotes(sess, pending))
		}

		if got := sess.Info().LastNote; got != "build: done" {
			t.Fatalf("cut at %d: lastNote = %q, want %q", cut, got, "build: done")
		}
		if len(pending) != 0 {
			t.Fatalf("cut at %d: %d bytes left pending after a complete match", cut, len(pending))
		}
	}
}

// TestPidVisible documents the namespace boundary Close and pollMetadata
// both depend on. Getting this wrong in Close means killing an unrelated
// local process that happens to hold a WSL PID's number.
func TestPidVisible(t *testing.T) {
	cases := []struct {
		name    string
		native  bool
		command string
		want    bool
	}{
		{"native session is in our namespace", true, "", true},
		{"daemon-spawned session has a local wsl.exe frontend PID", false, "claude", true},
		{"WSL-registered attach/pane session is not", false, "", runsDirectly(false)},
	}
	for _, c := range cases {
		if got := pidVisible(c.native, c.command); got != c.want {
			t.Errorf("%s: pidVisible(%v, %q) = %v, want %v",
				c.name, c.native, c.command, got, c.want)
		}
	}
}
