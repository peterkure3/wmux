package daemon

import (
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestMain silences the daemon's per-notify log lines — the burst tests
// below publish thousands of events and the log write dominates runtime.
func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

// feed pushes data through scanNotes the way watchOutput does — appending
// chunks to a pending buffer and applying the same maxPending trim — and
// collects every published event.
type feed struct {
	d       *Daemon
	sess    *Session
	pending []byte
}

func newFeed(t *testing.T) (*feed, chan struct{}) {
	t.Helper()
	d := New("") // no persistence
	sess := &Session{ID: "stress"}
	d.mu.Lock()
	d.sessions[sess.ID] = sess
	d.mu.Unlock()
	return &feed{d: d, sess: sess}, nil
}

func (f *feed) write(chunk []byte) {
	const maxPending = 16 * 1024
	f.pending = append(f.pending, chunk...)
	f.pending = f.d.scanNotes(f.sess, f.pending)
	if len(f.pending) > maxPending {
		f.pending = f.pending[len(f.pending)-maxPending:]
	}
}

func (f *feed) lastNote() string {
	f.sess.mu.Lock()
	defer f.sess.mu.Unlock()
	return f.sess.lastNote
}

// TestScanNotesFragmented splits one sequence across every possible chunk
// boundary — a notify must be detected no matter where reads cut it.
func TestScanNotesFragmented(t *testing.T) {
	seq := "\x1b]99;title=Frag;message=split test;type=agent_done\x07"
	for cut := 1; cut < len(seq); cut++ {
		f, _ := newFeed(t)
		f.write([]byte("noise before " + seq[:cut]))
		if f.lastNote() != "" {
			t.Fatalf("cut=%d: matched on incomplete sequence", cut)
		}
		f.write([]byte(seq[cut:] + " noise after"))
		if got := f.lastNote(); got != "Frag: split test" {
			t.Fatalf("cut=%d: lastNote=%q, want %q", cut, got, "Frag: split test")
		}
	}
}

// TestScanNotesByteAtATime feeds a stream one byte at a time.
func TestScanNotesByteAtATime(t *testing.T) {
	f, _ := newFeed(t)
	stream := "\x1b[31mred\x1b[0m\x1b]9;one\x07mid\x1b]777;notify;T;two\x1b\\tail"
	for i := 0; i < len(stream); i++ {
		f.write([]byte{stream[i]})
	}
	if got := f.lastNote(); got != "T: two" {
		t.Fatalf("lastNote=%q, want %q", got, "T: two")
	}
}

// TestScanNotesBurst pushes 5000 sequences in random-sized chunks.
// Publish intentionally DROPS events for subscribers whose 32-slot buffer
// is full, so the assertion is: every event that does arrive is intact
// and strictly in order, and lastNote reflects the final sequence.
func TestScanNotesBurst(t *testing.T) {
	const n = 5000
	f, _ := newFeed(t)
	sub := f.d.Subscribe()

	var got []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for evt := range sub {
			if evt.Type == "notify" && evt.Notify != nil {
				got = append(got, evt.Notify.Body)
			}
		}
	}()

	var stream []byte
	for i := 0; i < n; i++ {
		stream = append(stream, []byte(fmt.Sprintf("\x1b[2K\x1b[1G output %d \x1b]99;title=B;message=%d;type=agent_done\x07", i, i))...)
	}
	rng := rand.New(rand.NewSource(1))
	for len(stream) > 0 {
		c := rng.Intn(300) + 1
		if c > len(stream) {
			c = len(stream)
		}
		f.write(stream[:c])
		stream = stream[c:]
	}
	f.d.Unsubscribe(sub) // closes sub, ends the drain goroutine
	<-done

	if len(got) == 0 {
		t.Fatal("no notifies arrived at all")
	}
	prev := -1
	for _, body := range got {
		v, err := strconv.Atoi(body)
		if err != nil {
			t.Fatalf("corrupted body %q", body)
		}
		if v <= prev {
			t.Fatalf("out of order: %d after %d", v, prev)
		}
		prev = v
	}
	if f.lastNote() != fmt.Sprintf("B: %d", n-1) {
		t.Fatalf("lastNote=%q, want last of burst", f.lastNote())
	}
	t.Logf("delivered %d/%d (drop-on-slow-subscriber is by design)", len(got), n)
}

// TestScanNotesMalformed throws garbage at the scanner: unterminated
// sequences, empty bodies, pathological kv strings, ESC inside a body.
// Nothing may panic, and later well-formed sequences must still parse.
func TestScanNotesMalformed(t *testing.T) {
	cases := []string{
		"\x1b]99;title=never terminated",               // no BEL/ST — must not match
		"\x1b]99;\x07",                                 // empty body
		"\x1b]99;=;;;===;x=\x07",                       // kv soup
		"\x1b]99;type=agent_done\x07",                  // kind only, no text
		"\x1b]777;notify;;\x07",                        // empty title+message
		"\x1b]9;\x07",                                  // empty OSC 9
		"\x1b]99;title=a\x1b]9;inner\x07",              // ESC inside body aborts outer, inner matches
		strings.Repeat("A", 20*1024) + "\x1b]9;ok\x07", // body after >maxPending junk
	}
	for i, c := range cases {
		f, _ := newFeed(t)
		f.write([]byte(c))
		// follow-up must still work regardless of what came before
		f.write([]byte("\x1b]9;still alive\x07"))
		if got := f.lastNote(); got != "still alive" {
			t.Fatalf("case %d (%.40q...): scanner wedged, lastNote=%q", i, c, got)
		}
	}
}

// TestScanNotesUnicode checks multibyte titles/messages survive parsing.
func TestScanNotesUnicode(t *testing.T) {
	f, _ := newFeed(t)
	f.write([]byte("\x1b]99;title=构建;message=完成 ✅ émoji 🎉;type=agent_done\x07"))
	if got := f.lastNote(); got != "构建: 完成 ✅ émoji 🎉" {
		t.Fatalf("lastNote=%q", got)
	}
}

// TestScanNotesHugeUnterminated verifies an unterminated sequence larger
// than maxPending can't wedge the buffer forever.
func TestScanNotesHugeUnterminated(t *testing.T) {
	f, _ := newFeed(t)
	f.write([]byte("\x1b]99;title=" + strings.Repeat("x", 64*1024))) // 4x cap, never terminated
	f.write([]byte("\x07"))                                          // terminator arrives after head was trimmed away — must NOT match trimmed garbage
	f.write([]byte("\x1b]9;recovered\x07"))
	if got := f.lastNote(); got != "recovered" {
		t.Fatalf("lastNote=%q, want %q", got, "recovered")
	}
}

// TestScanNotesConcurrentSessions runs scanNotes for many sessions in
// parallel against one daemon — meant for -race.
func TestScanNotesConcurrentSessions(t *testing.T) {
	d := New("")
	sub := d.Subscribe()
	defer d.Unsubscribe(sub)
	go func() {
		for range sub {
		}
	}()

	const sessions, per = 16, 500
	var wg sync.WaitGroup
	for i := 0; i < sessions; i++ {
		sess := &Session{ID: fmt.Sprintf("s%d", i)}
		d.mu.Lock()
		d.sessions[sess.ID] = sess
		d.mu.Unlock()

		wg.Add(1)
		go func(sess *Session) {
			defer wg.Done()
			var pending []byte
			for j := 0; j < per; j++ {
				pending = append(pending, []byte(fmt.Sprintf("\x1b]99;title=%s;message=%d\x07", sess.ID, j))...)
				pending = d.scanNotes(sess, pending)
			}
		}(sess)
	}
	wg.Wait()

	for _, info := range d.List() {
		want := fmt.Sprintf("%s: %d", info.ID, per-1)
		if info.LastNote != want {
			t.Errorf("session %s lastNote=%q, want %q", info.ID, info.LastNote, want)
		}
	}
}
