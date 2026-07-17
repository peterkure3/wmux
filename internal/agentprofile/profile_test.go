package agentprofile

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// pointHome redirects os.UserHomeDir to a temp dir so tests never touch
// the real ~/.wmux/agents.
func pointHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home) // windows
	t.Setenv("HOME", home)        // everything else
	return home
}

func TestBundledProfilesLoadAndValidate(t *testing.T) {
	pointHome(t)
	for _, name := range []string{"claude", "codex", "kimi", "kiro"} {
		p, err := Load(name)
		if err != nil {
			t.Fatalf("Load(%q): %v", name, err)
		}
		if p.Name != name {
			t.Errorf("Load(%q).Name = %q", name, p.Name)
		}
	}
}

func TestClaudeProfileShape(t *testing.T) {
	pointHome(t)
	p, err := Load("claude")
	if err != nil {
		t.Fatal(err)
	}
	if p.Wire != WireStdinJSON || p.SessionField != "session_id" || p.CwdField != "cwd" ||
		p.MessageField != "message" || len(p.EventAllow) != 0 || p.DefaultMessage != "" || p.SessionFallback != "" {
		t.Errorf("claude profile drifted from hook-claude behavior: %+v", p)
	}
}

func TestCodexProfileShape(t *testing.T) {
	pointHome(t)
	p, err := Load("codex")
	if err != nil {
		t.Fatal(err)
	}
	if p.Wire != WireArgvJSON || p.MessageField != "last-assistant-message" ||
		!slices.Equal(p.EventAllow, []string{"agent-turn-complete"}) ||
		p.DefaultMessage != "Codex finished a turn" || p.SessionFallback != "getwd" {
		t.Errorf("codex profile drifted from hook-codex behavior: %+v", p)
	}
}

func TestUnknownProfile(t *testing.T) {
	pointHome(t)
	if _, err := Load("nonesuch"); err == nil {
		t.Fatal("Load(nonesuch) succeeded")
	}
}

func TestInvalidAgentName(t *testing.T) {
	pointHome(t)
	for _, name := range []string{"../etc", `a\b`, "x.toml"} {
		if _, err := Load(name); err == nil {
			t.Errorf("Load(%q) succeeded, want error", name)
		}
	}
}

func TestUserOverrideReplacesBundled(t *testing.T) {
	home := pointHome(t)
	dir := filepath.Join(home, ".wmux", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	override := `name = "claude"
wire = "stdin-json"
message_field = "msg"
`
	if err := os.WriteFile(filepath.Join(dir, "claude.toml"), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load("claude")
	if err != nil {
		t.Fatal(err)
	}
	if p.MessageField != "msg" {
		t.Errorf("override not applied: MessageField = %q", p.MessageField)
	}
	if p.SessionField != "" {
		t.Errorf("override should replace wholesale, but bundled session_field leaked through: %q", p.SessionField)
	}
}

func TestUserProfileNewAgent(t *testing.T) {
	home := pointHome(t)
	dir := filepath.Join(home, ".wmux", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	newAgent := `name = "futura"
wire = "stdin-json"
session_field = "sid"
message_field = "text"
event_field = "kind"
event_allow = ["done"]
`
	if err := os.WriteFile(filepath.Join(dir, "futura.toml"), []byte(newAgent), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load("futura")
	if err != nil {
		t.Fatal(err)
	}
	if !p.EventAllowed("done") || p.EventAllowed("started") {
		t.Error("event_allow filter wrong")
	}
	if !slices.Contains(List(), "futura") {
		t.Errorf("List() missing user profile: %v", List())
	}
}

func TestNameMismatchRejected(t *testing.T) {
	home := pointHome(t)
	dir := filepath.Join(home, ".wmux", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := `name = "other"
wire = "stdin-json"
message_field = "m"
`
	if err := os.WriteFile(filepath.Join(dir, "mislabeled.toml"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("mislabeled"); err == nil {
		t.Fatal("mismatched name accepted")
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		p  Profile
		ok bool
	}{
		{Profile{Name: "x", Wire: WireStdinJSON, MessageField: "m"}, true},
		{Profile{Name: "x", Wire: "smoke-signals", MessageField: "m"}, false},
		{Profile{Name: "x", Wire: WireStdinJSON}, false},
		{Profile{Name: "x", Wire: WireStdinJSON, MessageField: "m", EventAllow: []string{"a"}}, false},
		{Profile{Name: "x", Wire: WireStdinJSON, MessageField: "m", SessionFallback: "guess"}, false},
	}
	for i, c := range cases {
		if err := c.p.Validate(); (err == nil) != c.ok {
			t.Errorf("case %d: Validate() = %v, want ok=%v", i, err, c.ok)
		}
	}
}

func TestExtract(t *testing.T) {
	payload := map[string]any{
		"message": "hi",
		"count":   float64(3),
		"props":   map[string]any{"sessionID": "s1", "deep": map[string]any{"x": "y"}},
	}
	cases := []struct {
		path, want string
	}{
		{"message", "hi"},
		{"count", ""},   // non-string
		{"missing", ""}, //
		{"", ""},        // empty path
		{"props.sessionID", "s1"},
		{"props.deep.x", "y"},
		{"props.missing", ""},
		{"message.deeper", ""}, // path through a non-map
	}
	for _, c := range cases {
		if got := Extract(payload, c.path); got != c.want {
			t.Errorf("Extract(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
