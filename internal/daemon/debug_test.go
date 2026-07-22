package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/peterkure/wmux/internal/proto"
)

func TestRingWraparound(t *testing.T) {
	r := newRing[int](3)
	for i := 1; i <= 5; i++ {
		r.add(i)
	}
	got := r.snapshot()
	want := []int{3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("snapshot = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("snapshot = %v, want %v", got, want)
		}
	}
}

func TestRingEmptySnapshotNotNil(t *testing.T) {
	r := newRing[string](5)
	got := r.snapshot()
	if got == nil {
		t.Fatal("snapshot() of an empty ring should be an empty slice, not nil (JSON should encode [], not null)")
	}
	if len(got) != 0 {
		t.Fatalf("snapshot() of an empty ring = %v, want empty", got)
	}
}

// TestSafeGoRecoversPanic is the core regression test for the reliability
// gap this file closes: before safeGo, nothing in the codebase called
// recover() anywhere, so any panic in a session goroutine crashed the
// whole daemon. This proves the goroutine's panic is caught, recorded, and
// does not propagate to the test (which would otherwise crash `go test`
// itself).
func TestSafeGoRecoversPanic(t *testing.T) {
	d := New("")
	var wg sync.WaitGroup
	wg.Add(1)
	d.safeGo("test-source", func() {
		defer wg.Done()
		panic("boom")
	})
	wg.Wait()

	// safeGo's own recover() runs in a deferred func after fn's frame
	// unwinds, so give it a moment to record before asserting.
	deadline := time.Now().Add(time.Second)
	for len(d.panics.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	entries := d.panics.snapshot()
	if len(entries) != 1 {
		t.Fatalf("panics recorded = %d, want 1", len(entries))
	}
	if entries[0].Source != "test-source" {
		t.Fatalf("panic source = %q, want test-source", entries[0].Source)
	}
	if entries[0].Err != "boom" {
		t.Fatalf("panic err = %q, want boom", entries[0].Err)
	}
}

func TestRecoverHandlerReturns500AndRecordsPanic(t *testing.T) {
	d := New("")
	h := d.recoverHandler("/test/panic", func(w http.ResponseWriter, r *http.Request) {
		panic("handler exploded")
	})

	req := httptest.NewRequest(http.MethodGet, "/test/panic", nil)
	rec := httptest.NewRecorder()
	h(rec, req) // must not panic out of this call

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	entries := d.panics.snapshot()
	if len(entries) != 1 {
		t.Fatalf("panics recorded = %d, want 1", len(entries))
	}
	if entries[0].Source != "/test/panic" {
		t.Fatalf("panic source = %q, want /test/panic", entries[0].Source)
	}
}

func TestHandleDebugState(t *testing.T) {
	d := New("")
	req := httptest.NewRequest(http.MethodGet, "/debug/state", nil)
	rec := httptest.NewRecorder()
	d.handleDebugState(rec, req)

	var state proto.DebugState
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if state.NumSessions != 0 {
		t.Fatalf("NumSessions = %d, want 0 for a fresh daemon", state.NumSessions)
	}
	if state.StartedAt.IsZero() {
		t.Fatal("StartedAt should be set")
	}
}

func TestPublishFeedsRecentEventsRing(t *testing.T) {
	d := New("")
	d.publishNotify(proto.NotifyEvent{SessionID: "s1", Body: "hi"})

	events := d.recentEvents.snapshot()
	if len(events) != 1 {
		t.Fatalf("recentEvents = %d, want 1", len(events))
	}
	if events[0].Type != proto.EventNotify {
		t.Fatalf("event type = %q, want notify", events[0].Type)
	}
}
