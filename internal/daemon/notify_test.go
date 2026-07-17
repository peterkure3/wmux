package daemon

import "testing"

func TestParseNote(t *testing.T) {
	cases := []struct {
		name                 string
		code, body           string
		title, message, kind string
	}{
		{"osc9 bare", "9", "build done", "", "build done", ""},
		{"osc99 bare", "99", "done", "", "done", ""},
		{"osc99 kv", "99", "title=Agent;message=needs input;type=agent_input", "Agent", "needs input", "agent_input"},
		{"osc99 partial kv", "99", "message=hi", "", "hi", ""},
		{"osc777 notify", "777", "notify;Build;complete", "Build", "complete", ""},
		{"osc777 other", "777", "somethingelse;x", "", "somethingelse;x", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			title, message, kind := parseNote(c.code, c.body)
			if title != c.title || message != c.message || kind != c.kind {
				t.Errorf("parseNote(%q, %q) = (%q, %q, %q), want (%q, %q, %q)",
					c.code, c.body, title, message, kind, c.title, c.message, c.kind)
			}
		})
	}
}

func TestOscNotifyRe(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		code, body string
	}{
		{"osc9 bel", "junk\x1b]9;hello\x07more", "9", "hello"},
		{"osc99 st", "\x1b]99;title=T;message=M\x1b\\", "99", "title=T;message=M"},
		{"osc777", "\x1b]777;notify;T;M\x07", "777", "notify;T;M"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			loc := oscNotifyRe.FindStringSubmatchIndex(c.in)
			if loc == nil {
				t.Fatalf("no match in %q", c.in)
			}
			code := c.in[loc[2]:loc[3]]
			body := c.in[loc[4]:loc[5]]
			if code != c.code || body != c.body {
				t.Errorf("got code=%q body=%q, want code=%q body=%q", code, body, c.code, c.body)
			}
		})
	}
}
