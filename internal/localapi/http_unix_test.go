//go:build darwin || linux

package localapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agensfield/scriba/internal/agentcontext"
)

func TestHealthIsMinimizedAndAllowlisted(t *testing.T) {
	s := NewHTTPServer(nil, nil, HTTPConfig{})
	r := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	want := `{"schemaVersion":"scriba.local.health.v1","status":"ok","contextVersion":"scriba.context.v2","eventVersion":"scriba.events.v2"}`
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

func TestRequestedAccount(t *testing.T) {
	for _, tc := range []struct{ path, account, code string }{
		{"/v1/context", "", ""},
		{"/v1/context?account=work", "work", ""},
		{"/v1/context?account=", "", "invalid_account"},
		{"/v1/context?account=%20work", "", "invalid_account"},
		{"/v1/context?account=one&account=two", "", "invalid_account"},
		{"/v1/context?account=work;bad=x", "", "invalid_account"},
		{"/v1/context?profile=work", "", "invalid_account"},
		{"/v1/context?cursor=v1.0000000000000000", "", "invalid_account"},
	} {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		account, code := requestedAccount(r, false)
		if account != tc.account || code != tc.code {
			t.Fatalf("%s: got (%q, %q), want (%q, %q)", tc.path, account, code, tc.account, tc.code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/events?account=work&cursor=v1.0000000000000000", nil)
	if account, code := requestedAccount(r, true); account != "work" || code != "" {
		t.Fatalf("events selector: got (%q, %q)", account, code)
	}
}

func TestHTTPRejectsUnknownAccount(t *testing.T) {
	s := NewHTTPServer(nil, agentcontext.New(agentcontext.Config{StorePath: t.TempDir() + "/missing.sqlite"}), HTTPConfig{})
	for _, path := range []string{"/v1/context?account=unknown", "/v1/events?account=unknown"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		s.server.Handler.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "account_unavailable") {
			t.Fatalf("%s: status=%d body=%s", path, w.Code, w.Body.String())
		}
	}
}
