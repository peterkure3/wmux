package daemon

import (
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/peterkure/wmux/internal/proto"
)

// Matches OSC 9 (basic notify), OSC 99 (extended notify, iTerm2-style),
// and OSC 777 (rxvt-style notify), capturing the code and body separately.
// All three are terminated by BEL (\x07) or ST (\x1b\\).
var oscNotifyRe = regexp.MustCompile(`\x1b\](9|99|777);([^\x07\x1b]*)(?:\x07|\x1b\\)`)

// parseNote turns a matched OSC notify sequence into structured fields.
// OSC 9 carries a bare message. OSC 99 carries key=value pairs
// (title=...;message=...;type=...); a body with no '=' at all is treated
// as a bare message so plain `printf '\e]99;done\a'` still works. OSC 777
// is rxvt-style `notify;<title>;<message>`; other OSC 777 subcommands are
// passed through as a bare message.
func parseNote(code, body string) (title, message, kind string) {
	switch code {
	case "99":
		if !strings.Contains(body, "=") {
			return "", body, ""
		}
		kv := make(map[string]string)
		for _, part := range strings.Split(body, ";") {
			if k, v, ok := strings.Cut(part, "="); ok {
				kv[k] = v
			}
		}
		return kv["title"], kv["message"], kv["type"]
	case "777":
		parts := strings.SplitN(body, ";", 3)
		if len(parts) == 3 && parts[0] == "notify" {
			return parts[1], parts[2], ""
		}
		return "", body, ""
	default: // "9"
		return "", body, ""
	}
}

// clipNote caps a stored lastNote so SessionInfo stays small on the wire —
// it is embedded in every GET /sessions response and every sessions push.
// The full body still reaches /events subscribers on the notify event itself.
func clipNote(s string) string {
	const maxRunes = 200
	if len(s) <= maxRunes { // fast path: byte len ≤ max ⇒ rune count ≤ max
		return s
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}

// scanNotes extracts every complete OSC notify sequence from pending,
// publishing each as a structured event against sess, and returns the
// unconsumed remainder for the caller to keep buffering.
func (d *Daemon) scanNotes(sess *Session, pending []byte) []byte {
	for {
		loc := oscNotifyRe.FindSubmatchIndex(pending)
		if loc == nil {
			return pending
		}
		code := string(pending[loc[2]:loc[3]])
		title, message, kind := parseNote(code, string(pending[loc[4]:loc[5]]))
		evt := proto.NotifyEvent{
			SessionID: sess.ID, Title: title, Body: message, Kind: kind,
			Time: time.Now(),
		}

		sess.mu.Lock()
		sess.lastNote = clipNote(evt.Display())
		sess.mu.Unlock()

		d.publishNotify(evt)
		slog.Debug("notify", "session", sess.ID, "note", evt.Display(), "kind", kind)

		pending = pending[loc[1]:] // drop everything through the matched sequence
	}
}
