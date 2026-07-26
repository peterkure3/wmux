package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/peterkure3/wmux/internal/authtoken"
	"github.com/peterkure3/wmux/internal/client"
	"github.com/peterkure3/wmux/internal/proto"
)

// TestDaemonHelpersUseDC is a wiring test, not a behavior test — the
// identify handshake and status-code handling are internal/client's own,
// already covered there. This just proves daemonGet/daemonPost actually
// go through the package-level dc (built from daemonAddr/daemonToken)
// rather than something disconnected from it.
func TestDaemonHelpersUseDC(t *testing.T) {
	var gotToken, gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/identify", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(proto.IdentifyResponse{App: "wmuxd", Protocol: proto.ProtocolVersion})
	})
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get(authtoken.HeaderName)
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode([]proto.SessionInfo{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	old := dc
	dc = client.New(srv.URL, "test-tok")
	defer func() { dc = old }()

	resp, err := daemonGet("/sessions")
	if err != nil {
		t.Fatalf("daemonGet: %v", err)
	}
	resp.Body.Close()

	if gotPath != "/sessions" {
		t.Errorf("daemon saw path %q, want /sessions", gotPath)
	}
	if gotToken != "test-tok" {
		t.Errorf("daemon saw token %q, want test-tok", gotToken)
	}
}
