package daemon

import (
	"crypto/subtle"
	"net/http"
)

// browserBlocked reports whether a request was originated by a browser,
// which no legitimate wmux client ever is.
//
// Sec-Fetch-Site is set by the browser itself and cannot be overridden by
// page JavaScript, so its presence with a cross-site value is conclusive.
// Origin is checked as a fallback for older browsers that predate Fetch
// Metadata. A CLI sends neither header, so absence is allowed — this
// check alone would be trivially bypassed by a native process, which is
// exactly what the token in guard is for.
func browserBlocked(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "":
		// No Fetch Metadata: fall through to the Origin check below.
	case "same-origin", "none":
		// "none" is a user-typed URL or bookmark; a page cannot forge it.
	default:
		return true // cross-site, same-site, or anything unrecognized
	}
	return r.Header.Get("Origin") != ""
}

// guard authenticates a request before it reaches a handler. Every route
// that can read or mutate session state goes through this; only /healthz
// is exempt, since it is a pure liveness probe that returns no data and
// `wmux update` needs it to work across a version skew where the CLI has
// no token yet.
func (d *Daemon) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if browserBlocked(r) {
			// Deliberately terse: a cross-origin caller learns nothing
			// about whether wmuxd is even running here.
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		got := r.Header.Get(authHeader)
		if subtle.ConstantTimeCompare([]byte(got), []byte(d.token)) != 1 {
			http.Error(w,
				"unauthorized: missing or stale auth token — check that "+
					"~/.wmux/token is readable, and that wmux and wmuxd are the "+
					"same version (`wmux version`, `wmuxd -version`)",
				http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}
