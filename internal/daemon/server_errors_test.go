package daemon

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIErrorClassification(t *testing.T) {
	d := New("", "token")
	addSession(d, &Session{ID: "active", running: true})
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/sessions", `{}`, 400},
		{"/surfaces", `{}`, 400},
		{"/sessions/register", `{}`, 400},
		{"/sessions/close", `{}`, 400},
		{"/sessions/deregister", `{}`, 400},
		{"/surfaces/input", `{}`, 400},
		{"/surfaces", `{"id":"new","command":"unused","cols":99999,"rows":30}`, 400},
		{"/surfaces/resize", `{"id":"active","cols":99999,"rows":30}`, 400},
		{"/surfaces/resize", `{"id":"missing","cols":80,"rows":24}`, 404},
		{"/sessions/close", `{"id":"missing"}`, 404},
		{"/surfaces", `{"id":"active","command":"unused"}`, 409},
		{"/sessions/register", `{"id":"active"}`, 409},
		{"/surfaces/input", `{"id":"active"}`, 409},
	} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			r.Header.Set(authHeader, "token")
			w := httptest.NewRecorder()
			d.handler().ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("got %d (%s), want %d", w.Code, w.Body, tc.status)
			}
		})
	}
	w := httptest.NewRecorder()
	writeAPIError(w, fmt.Errorf("wrapped: %w", invalidf("bad size")))
	if w.Code != 400 {
		t.Fatal("wrapped errors must retain classification")
	}
	w = httptest.NewRecorder()
	writeAPIError(w, fmt.Errorf("unexpected OS failure"))
	if w.Code != 500 {
		t.Fatalf("unexpected failures must be 500, got %d", w.Code)
	}
}
