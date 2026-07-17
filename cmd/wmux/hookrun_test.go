package main

import (
	"testing"

	"github.com/peterkure/wmux/internal/agentprofile"
)

// Parity tests: evalHook applied to the bundled claude/codex profiles must
// make exactly the decisions the old cmdHookClaude/cmdHookCodex made.

func mustLoad(t *testing.T, name string) *agentprofile.Profile {
	t.Helper()
	home := t.TempDir() // isolate from real ~/.wmux/agents overrides
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	p, err := agentprofile.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func noGetwd(t *testing.T) func() (string, error) {
	return func() (string, error) {
		t.Error("getwd consulted when it should not be")
		return "", nil
	}
}

func TestEvalHookClaudeParity(t *testing.T) {
	p := mustLoad(t, "claude")

	// The documented Claude Code Notification payload.
	dec, err := evalHook(p, []byte(`{"session_id":"abc123","cwd":"/home/you/proj","hook_event_name":"Notification","message":"Claude is waiting for your input"}`), "", noGetwd(t))
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Notify || dec.Session != "abc123" || dec.Body != "Claude is waiting for your input" {
		t.Errorf("normal payload: %+v", dec)
	}

	// session_id omitted: fall back to cwd (old best-effort fallback).
	dec, err = evalHook(p, []byte(`{"cwd":"/home/you/proj","message":"m"}`), "", noGetwd(t))
	if err != nil || dec.Session != "/home/you/proj" {
		t.Errorf("cwd fallback: %+v err=%v", dec, err)
	}

	// Empty message: silent skip, exit 0 — never a default body.
	dec, err = evalHook(p, []byte(`{"session_id":"abc","hook_event_name":"Stop"}`), "", noGetwd(t))
	if err != nil || dec.Notify {
		t.Errorf("empty message must skip: %+v err=%v", dec, err)
	}

	// Both IDs empty: session stays "" (old code sent an empty session,
	// no os.Getwd fallback).
	dec, err = evalHook(p, []byte(`{"message":"m"}`), "", noGetwd(t))
	if err != nil || dec.Session != "" || !dec.Notify {
		t.Errorf("empty session parity: %+v err=%v", dec, err)
	}

	// Any event name passes — old code never filtered on hook_event_name.
	dec, err = evalHook(p, []byte(`{"session_id":"s","hook_event_name":"SomethingNew","message":"m"}`), "", noGetwd(t))
	if err != nil || !dec.Notify {
		t.Errorf("unfiltered events parity: %+v err=%v", dec, err)
	}

	// Garbage payload: parse error.
	if _, err = evalHook(p, []byte(`{not json`), "", noGetwd(t)); err == nil {
		t.Error("garbage payload parsed")
	}
}

func TestEvalHookCodexParity(t *testing.T) {
	p := mustLoad(t, "codex")
	getwd := func() (string, error) { return `C:\proj`, nil }

	// The documented Codex payload with --session set.
	dec, err := evalHook(p, []byte(`{"type":"agent-turn-complete","last-assistant-message":"All tests passed"}`), "my-project", getwd)
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Notify || dec.Session != "my-project" || dec.Body != "All tests passed" {
		t.Errorf("normal payload: %+v", dec)
	}

	// --session omitted: fall back to the working directory.
	dec, err = evalHook(p, []byte(`{"type":"agent-turn-complete","last-assistant-message":"x"}`), "", getwd)
	if err != nil || dec.Session != `C:\proj` {
		t.Errorf("getwd fallback: %+v err=%v", dec, err)
	}

	// Other event types are ignored.
	dec, err = evalHook(p, []byte(`{"type":"agent-turn-started"}`), "s", getwd)
	if err != nil || dec.Notify {
		t.Errorf("event filter: %+v err=%v", dec, err)
	}

	// Empty message: substitute the fixed body, still notify.
	dec, err = evalHook(p, []byte(`{"type":"agent-turn-complete"}`), "s", getwd)
	if err != nil || !dec.Notify || dec.Body != "Codex finished a turn" {
		t.Errorf("default body: %+v err=%v", dec, err)
	}

	if _, err = evalHook(p, []byte(`]`), "s", getwd); err == nil {
		t.Error("garbage payload parsed")
	}
}

func TestEvalHookKimiKiroSpeakClaudeDialect(t *testing.T) {
	// Kimi and Kiro copied Claude Code's hook payload shape; the same
	// payload must produce the same decision through their profiles.
	payload := []byte(`{"session_id":"k1","cwd":"/p","hook_event_name":"Notification","message":"done"}`)
	for _, name := range []string{"kimi", "kiro"} {
		p := mustLoad(t, name)
		dec, err := evalHook(p, payload, "", noGetwd(t))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !dec.Notify || dec.Session != "k1" || dec.Body != "done" {
			t.Errorf("%s: %+v", name, dec)
		}
	}
}

func TestEvalHookSessionFlagBeatsPayload(t *testing.T) {
	p := mustLoad(t, "claude")
	dec, err := evalHook(p, []byte(`{"session_id":"payload-id","message":"m"}`), "flag-id", noGetwd(t))
	if err != nil || dec.Session != "flag-id" {
		t.Errorf("--session must win over payload: %+v err=%v", dec, err)
	}
}
