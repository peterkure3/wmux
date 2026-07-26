package main

import (
	"strings"
	"testing"

	"github.com/peterkure3/wmux/internal/proto"
	"github.com/peterkure3/wmux/internal/version"
)

func TestIdentifyProblem(t *testing.T) {
	cases := []struct {
		name    string
		id      proto.IdentifyResponse
		wantMsg bool
		want    string // substring, only checked when wantMsg is true
	}{
		{
			name:    "matching app and protocol",
			id:      proto.IdentifyResponse{App: "wmuxd", Protocol: proto.ProtocolVersion},
			wantMsg: false,
		},
		{
			name:    "wrong app",
			id:      proto.IdentifyResponse{App: "something-else", Protocol: proto.ProtocolVersion},
			wantMsg: true,
			want:    "something other than wmuxd",
		},
		{
			name:    "protocol mismatch",
			id:      proto.IdentifyResponse{App: "wmuxd", Version: "v0.4.0", Protocol: proto.ProtocolVersion + 1},
			wantMsg: true,
			want:    "restart wmuxd",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := identifyProblem(c.id)
			if c.wantMsg && got == "" {
				t.Fatal("identifyProblem returned \"\"; wanted a message")
			}
			if !c.wantMsg && got != "" {
				t.Fatalf("identifyProblem returned %q; wanted \"\"", got)
			}
			if c.wantMsg && !strings.Contains(got, c.want) {
				t.Fatalf("identifyProblem = %q; want it to contain %q", got, c.want)
			}
		})
	}
}

// TestIdentifyProblemMentionsBothVersions guards the specific message
// shape the plan called for: "wmux 0.3 cannot talk to wmuxd 0.4 (protocol
// 2 vs 3) — restart wmuxd", not just "mismatch" — a user hitting this needs
// enough in the one line to know which side is stale.
func TestIdentifyProblemMentionsBothVersions(t *testing.T) {
	id := proto.IdentifyResponse{App: "wmuxd", Version: "v9.9.9", Protocol: proto.ProtocolVersion + 1}
	got := identifyProblem(id)
	if !strings.Contains(got, version.String()) {
		t.Errorf("message %q does not mention this wmux's own version %q", got, version.String())
	}
	if !strings.Contains(got, "v9.9.9") {
		t.Errorf("message %q does not mention the daemon's version", got)
	}
}
