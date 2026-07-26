package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGuardRejectsBrowserOriginatedRequests covers the attack the token
// alone does not: a web page the user happens to be visiting POSTing to
// loopback. A cross-origin POST with a simple Content-Type needs no CORS
// preflight, and POST /sessions executes its Command field — so an
// unguarded daemon is remote code execution from any visited page.
func TestGuardRejectsBrowserOriginatedRequests(t *testing.T) {
	d := New("", "s3cret")
	h := d.guard(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler ran; the request should have been rejected")
	})

	cases := []struct{ name, header, value string }{
		{"cross-site fetch metadata", "Sec-Fetch-Site", "cross-site"},
		{"same-site fetch metadata", "Sec-Fetch-Site", "same-site"},
		{"origin header only", "Origin", "https://evil.example"},
		{"origin claiming to be us", "Origin", "http://127.0.0.1:47823"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader("{}"))
			r.Header.Set(c.header, c.value)
			// Even *with* a valid token: a page that somehow learned the
			// token still must not be able to drive the daemon.
			r.Header.Set(authHeader, "s3cret")
			w := httptest.NewRecorder()
			h(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
			}
		})
	}
}

// TestGuardAllowsCLIShapedRequests: a CLI sends neither Fetch Metadata nor
// Origin, and Sec-Fetch-Site: none (a typed URL) is not a page-driven
// request either.
func TestGuardAllowsCLIShapedRequests(t *testing.T) {
	d := New("", "s3cret")
	for _, site := range []string{"", "none", "same-origin"} {
		ran := false
		h := d.guard(func(w http.ResponseWriter, r *http.Request) { ran = true })
		r := httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader("{}"))
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		r.Header.Set(authHeader, "s3cret")
		h(httptest.NewRecorder(), r)
		if !ran {
			t.Fatalf("Sec-Fetch-Site=%q: handler did not run", site)
		}
	}
}

// TestGuardRequiresToken covers the other half: a local process that is
// not a browser and so passes browserBlocked.
func TestGuardRequiresToken(t *testing.T) {
	d := New("", "s3cret")
	cases := []struct {
		name  string
		token string
	}{
		{"absent", ""},
		{"wrong", "guess"},
		{"prefix of the real token", "s3cre"},
		{"real token plus trailing data", "s3cretX"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := d.guard(func(w http.ResponseWriter, r *http.Request) {
				t.Error("handler ran without a valid token")
			})
			r := httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader("{}"))
			if c.token != "" {
				r.Header.Set(authHeader, c.token)
			}
			w := httptest.NewRecorder()
			h(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
			}
		})
	}
}

// TestServeGuardsEveryMutatingRoute is the regression test that matters
// most: it walks the real mux, so a route added later without going
// through the route() helper fails here instead of shipping open.
func TestServeGuardsEveryMutatingRoute(t *testing.T) {
	d := New("", "s3cret")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()

	// Every route except /healthz must reject an unauthenticated caller.
	routes := []struct {
		method, path string
	}{
		{http.MethodGet, "/sessions"},
		{http.MethodPost, "/sessions"},
		{http.MethodPost, "/sessions/register"},
		{http.MethodPost, "/sessions/deregister"},
		{http.MethodPost, "/sessions/close"},
		{http.MethodPost, "/sessions/prune"},
		{http.MethodPost, "/surfaces"},
		{http.MethodGet, "/surfaces/attach"},
		{http.MethodPost, "/surfaces/input"},
		{http.MethodPost, "/surfaces/resize"},
		{http.MethodPost, "/notify"},
		{http.MethodGet, "/events"},
		{http.MethodGet, "/debug/state"},
		{http.MethodGet, "/debug/panics"},
		{http.MethodGet, "/debug/events/recent"},
		{http.MethodGet, "/debug/pprof/"},
		{http.MethodGet, "/debug/pprof/cmdline"},
		{http.MethodPost, "/shutdown"}, // MUST be guarded: unauth = free daemon kill
	}
	for _, rt := range routes {
		req, err := http.NewRequest(rt.method, srv.URL+rt.path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", rt.method, rt.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want %d (route is unauthenticated)",
				rt.method, rt.path, resp.StatusCode, http.StatusUnauthorized)
		}
	}

	// /healthz and /identify are the deliberate exemptions.
	for _, path := range []string{"/healthz", "/identify"} {
		resp, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d, want %d", path, resp.StatusCode, http.StatusOK)
		}
	}
}
