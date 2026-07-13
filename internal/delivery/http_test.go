package delivery

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fixtureEnvelope() Envelope {
	return Envelope{SchemaVersion: SchemaVersion, EventID: "event-1", EventKind: "limit_warning", Source: "test", ProfileID: "default", OccurredAt: time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC), Data: json.RawMessage(`{"usedPercent":81}`)}
}

func TestWebhookSignsExactBodyAndDoesNotFollowRedirects(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 30, 0, 0, time.UTC)
	secret := []byte("fixture-secret")
	redirectHits := 0
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirectHits++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		body, _ = io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte("1783945800."))
		_, _ = mac.Write(body)
		want := "v1=" + hex.EncodeToString(mac.Sum(nil))
		if r.Header.Get("X-Scriba-Signature") != want || r.Header.Get("X-Scriba-Event-ID") != "event-1" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers=%v", r.Header)
		}
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	out := (Webhook{URL: server.URL, Secret: secret, Now: func() time.Time { return now }}).Deliver(t.Context(), fixtureEnvelope())
	if out.Disposition != Terminal || redirectHits != 0 || len(body) == 0 {
		t.Fatalf("out=%+v redirectHits=%d body=%s", out, redirectHits, body)
	}
}

func TestHTTPDispositionAndRetryAfterAreClosedAndCapped(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		status int
		retry  string
		want   Disposition
		wait   time.Duration
	}{
		{204, "", Delivered, 0},
		{400, "3600", Terminal, 0},
		{408, "5", Retryable, 5 * time.Second},
		{425, "", Retryable, 0},
		{429, "7200", Retryable, time.Hour},
		{500, now.Add(30 * time.Minute).Format(http.TimeFormat), Retryable, 30 * time.Minute},
		{599, "invalid", Retryable, 0},
	}
	for _, test := range tests {
		header := make(http.Header)
		header.Set("Retry-After", test.retry)
		out := classifyHTTP(test.status, header, now)
		if out.Disposition != test.want || out.RetryAfter != test.wait {
			t.Fatalf("status %d: %+v", test.status, out)
		}
	}
}

func TestNtfyUsesJSONSurfaceAndCanonicalEnvelope(t *testing.T) {
	var got struct {
		Topic, Title, Message string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token-secret" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers=%v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ntfy-message-1"}`)
	}))
	t.Cleanup(server.Close)
	out := (Ntfy{URL: server.URL, Topic: "scriba-alerts", Token: "token-secret"}).Deliver(t.Context(), fixtureEnvelope())
	if out.Disposition != Delivered || out.ProviderID != "ntfy-message-1" || got.Topic != "scriba-alerts" || got.Title != "Scriba: limit_warning" {
		t.Fatalf("out=%+v payload=%+v", out, got)
	}
	canonical, _ := Marshal(fixtureEnvelope())
	if got.Message != string(canonical) || strings.Contains(got.Message, "token-secret") {
		t.Fatalf("message=%s", got.Message)
	}
}
