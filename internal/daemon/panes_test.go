package daemon

import (
	"sync"
	"testing"
	"time"

	"github.com/peterkure3/wmux/internal/proto"
)

func TestClaimPaneSpecRoundTrip(t *testing.T) {
	d := New("", "")
	want := proto.PaneSpec{ID: "p1", Cwd: "/tmp", Distro: "Arch", Command: "claude", Native: true}
	d.AddPaneSpec(want)

	got, err := d.ClaimPaneSpec("p1")
	if err != nil {
		t.Fatalf("claim failed: %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestClaimPaneSpecIsSingleUse is the important one: a spec that could be
// claimed twice would start the same agent twice, in two panes, both
// registering under one session ID.
func TestClaimPaneSpecIsSingleUse(t *testing.T) {
	d := New("", "")
	d.AddPaneSpec(proto.PaneSpec{ID: "p1", Command: "claude"})

	if _, err := d.ClaimPaneSpec("p1"); err != nil {
		t.Fatalf("first claim failed: %v", err)
	}
	if _, err := d.ClaimPaneSpec("p1"); err == nil {
		t.Fatal("second claim succeeded; a spec must only ever start one agent")
	}
}

func TestClaimPaneSpecUnknownID(t *testing.T) {
	d := New("", "")
	if _, err := d.ClaimPaneSpec("nope"); err == nil {
		t.Fatal("claiming an unfiled spec succeeded")
	}
}

// TestClaimPaneSpecExpires pins the reason paneSpecTTL exists: an
// unclaimed spec left lying around would be picked up by an unrelated
// `wt --profile wmux` launch much later, silently starting an agent the
// user did not ask for. The expired spec must also be *removed*, not just
// rejected, or it stays a landmine.
func TestClaimPaneSpecExpires(t *testing.T) {
	d := New("", "")
	d.panes.mu.Lock()
	d.panes.pending = map[string]pendingPane{
		"stale": {
			spec:  proto.PaneSpec{ID: "stale", Command: "claude"},
			filed: time.Now().Add(-paneSpecTTL - time.Second),
		},
	}
	d.panes.mu.Unlock()

	if _, err := d.ClaimPaneSpec("stale"); err == nil {
		t.Fatal("expired spec was claimable")
	}

	d.panes.mu.Lock()
	_, still := d.panes.pending["stale"]
	d.panes.mu.Unlock()
	if still {
		t.Error("expired spec left in the pending map")
	}
}

// TestAddPaneSpecReplaces: re-running `wmux pane --id x` before the first
// pane claimed should hand the new pane the new command, not the old one.
func TestAddPaneSpecReplaces(t *testing.T) {
	d := New("", "")
	d.AddPaneSpec(proto.PaneSpec{ID: "p1", Command: "old"})
	d.AddPaneSpec(proto.PaneSpec{ID: "p1", Command: "new"})

	got, err := d.ClaimPaneSpec("p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Command != "new" {
		t.Errorf("Command = %q, want %q", got.Command, "new")
	}
}

// TestPaneSpecsConcurrent is for -race: `wmux pane` files while several
// pane-exec processes retry their claims.
func TestPaneSpecsConcurrent(t *testing.T) {
	d := New("", "")
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); d.AddPaneSpec(proto.PaneSpec{ID: "p", Command: "c"}) }()
		go func() { defer wg.Done(); d.ClaimPaneSpec("p") }()
	}
	wg.Wait()
}
