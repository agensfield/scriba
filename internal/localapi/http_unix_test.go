//go:build darwin || linux

package localapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthIsMinimizedAndAllowlisted(t *testing.T) {
	s := NewHTTPServer(nil, nil, HTTPConfig{})
	r := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	want := `{"schemaVersion":"scriba.local.health.v1","status":"ok","contextVersion":"scriba.context.v1","eventVersion":"scriba.events.v1"}`
	if strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestHTTPRouteGuards(t *testing.T) {
	s := NewHTTPServer(nil, nil, HTTPConfig{})
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
		allow                    string
	}{
		{"unknown", http.MethodGet, "/v1/nope", "", http.StatusNotFound, ""},
		{"method", http.MethodPost, "/v1/health", "", http.StatusMethodNotAllowed, http.MethodGet},
		{"body", http.MethodGet, "/v1/health", "x", http.StatusBadRequest, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			s.server.Handler.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if got := w.Header().Get("Allow"); got != tc.allow {
				t.Fatalf("Allow = %q, want %q", got, tc.allow)
			}
		})
	}
}

func TestRequestedCursor(t *testing.T) {
	for _, tc := range []struct{ header, query, cursor, code string }{
		{"v1.0000000000000001", "", "v1.0000000000000001", ""},
		{"", "v1.0000000000000002", "v1.0000000000000002", ""},
		{"v1.0000000000000001", "v1.0000000000000001", "v1.0000000000000001", ""},
		{"v1.0000000000000001", "v1.0000000000000002", "", "cursor_disagreement"},
	} {
		path := "/v1/events"
		if tc.query != "" {
			path += "?cursor=" + tc.query
		}
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if tc.header != "" {
			r.Header.Set("Last-Event-ID", tc.header)
		}
		cursor, code := requestedCursor(r)
		if cursor != tc.cursor || code != tc.code {
			t.Fatalf("got (%q, %q), want (%q, %q)", cursor, code, tc.cursor, tc.code)
		}
	}
}

func TestRequestedCursorRejectsDuplicatesAndNonCanonicalValues(t *testing.T) {
	for _, path := range []string{
		"/v1/events?cursor=v1.0000000000000001&cursor=v1.0000000000000001",
		"/v1/events?cursor=%20v1.0000000000000001",
		"/v1/events?cursor=",
	} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if _, code := requestedCursor(r); code != "invalid_cursor" {
			t.Fatalf("%s: code = %q", path, code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	r.Header.Add("Last-Event-ID", "v1.0000000000000001")
	r.Header.Add("Last-Event-ID", "v1.0000000000000001")
	if _, code := requestedCursor(r); code != "invalid_cursor" {
		t.Fatalf("duplicate header code = %q", code)
	}
}
