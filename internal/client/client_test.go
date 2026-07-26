package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/peterkure3/wmux/internal/authtoken"
	"github.com/peterkure3/wmux/internal/proto"
)

// mockDaemon builds an httptest server that answers /identify like a real
// wmuxd (app=wmuxd, protocol=proto.ProtocolVersion) plus whatever extra
// handler the test supplies, so callers only need to describe the one
// route they care about.
func mockDaemon(t *testing.T, extra http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/identify", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(proto.IdentifyResponse{App: "wmuxd", Protocol: proto.ProtocolVersion})
	})
	mux.HandleFunc("/", extra)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestListSessionsRoundTrip(t *testing.T) {
	want := []proto.SessionInfo{{ID: "a", Running: true}, {ID: "b"}}
	srv := mockDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		json.NewEncoder(w).Encode(want)
	})
	c := New(srv.URL, "tok")

	got, err := c.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("ListSessions = %+v, want %+v", got, want)
	}
}

func TestPostJSONNonMatchingStatusIsStatusError(t *testing.T) {
	srv := mockDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("no such session"))
	})
	c := New(srv.URL, "tok")

	err := c.CloseSession("ghost")
	if err == nil {
		t.Fatal("CloseSession: want error for a 404, got nil")
	}
	se, ok := err.(*StatusError)
	if !ok {
		t.Fatalf("error type = %T, want *StatusError", err)
	}
	if se.Code != http.StatusNotFound {
		t.Errorf("Code = %d, want 404", se.Code)
	}
	if se.Body != "no such session" {
		t.Errorf("Body = %q, want %q", se.Body, "no such session")
	}
}

func TestNotifyAccepts202NotOK(t *testing.T) {
	srv := mockDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	c := New(srv.URL, "tok")

	if err := c.Notify(proto.NotifyEvent{SessionID: "s", Body: "hi"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
}

func TestIdentifyMismatchProtocolIsSticky(t *testing.T) {
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/identify", func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(proto.IdentifyResponse{
			App: "wmuxd", Version: "v9.9.9", Protocol: proto.ProtocolVersion + 1,
		})
	})
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		t.Error("the real handler ran; a protocol mismatch should have short-circuited before this")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "tok")

	_, err := c.ListSessions()
	pe, ok := err.(*ProtocolError)
	if !ok {
		t.Fatalf("error type = %T, want *ProtocolError", err)
	}
	if pe.DaemonProtocol != proto.ProtocolVersion+1 {
		t.Errorf("DaemonProtocol = %d, want %d", pe.DaemonProtocol, proto.ProtocolVersion+1)
	}

	// A second call must not hit /identify again (sync.Once) — and must
	// still return the same error.
	if _, err := c.ListSessions(); err == nil {
		t.Fatal("second ListSessions: want the sticky ProtocolError again, got nil")
	}
	if calls != 1 {
		t.Errorf("/identify was called %d times, want exactly 1 (checkIdentify is sync.Once)", calls)
	}
}

func TestIdentifyWrongApp(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/identify", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(proto.IdentifyResponse{App: "some-other-service", Protocol: proto.ProtocolVersion})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "tok")
	_, err := c.ListSessions()
	pe, ok := err.(*ProtocolError)
	if !ok {
		t.Fatalf("error type = %T, want *ProtocolError", err)
	}
	if pe.DaemonApp != "some-other-service" {
		t.Errorf("DaemonApp = %q, want %q", pe.DaemonApp, "some-other-service")
	}
}

// TestIdentifyUnreachableDoesNotBlockTheRealRequest: if /identify itself
// can't be reached or decoded (old wmuxd predating the route, or a
// network blip), checkIdentify must fail open — the real request behind
// it should still run and report its own failure on its own terms.
func TestIdentifyRouteMissingFailsOpen(t *testing.T) {
	mux := http.NewServeMux() // no /identify handler at all -> 404
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]proto.SessionInfo{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "tok")
	sessions, err := c.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v (an old-wmuxd-shaped /identify 404 should not block the real call)", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %v, want empty", sessions)
	}
}

func TestGetJSONNonOKStatus(t *testing.T) {
	srv := mockDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("bad token"))
	})
	c := New(srv.URL, "wrong-tok")

	var v any
	err := c.GetJSON("/sessions", &v)
	se, ok := err.(*StatusError)
	if !ok {
		t.Fatalf("error type = %T, want *StatusError", err)
	}
	if se.Code != http.StatusUnauthorized {
		t.Errorf("Code = %d, want 401", se.Code)
	}
}

func TestTokenHeaderAttached(t *testing.T) {
	var gotToken string
	srv := mockDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get(authtoken.HeaderName)
		json.NewEncoder(w).Encode([]proto.SessionInfo{})
	})
	c := New(srv.URL, "s3cret")
	if _, err := c.ListSessions(); err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if gotToken != "s3cret" {
		t.Errorf("token header = %q, want s3cret", gotToken)
	}
}
