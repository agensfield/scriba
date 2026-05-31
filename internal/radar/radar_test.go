package radar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchAndRenderCurrentRadar(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
  "schema_version": "1.0",
  "service": "codex-reset-radar",
  "checked_at": "2026-06-01T04:58:08.210696+08:00",
  "status": "none",
  "window_open": false,
  "message": "none",
  "recommended_action": "wait",
  "last_window": {
    "id": "codex-speed-window-2026-05-31-codex",
    "title": "Codex 用量限制重置",
    "status": "closed",
    "opened_at": "2026-05-31T13:59:10+08:00",
    "closed_at": "2026-05-31T23:25:06+08:00",
    "window_minutes": 565,
    "window_human": "9小时25分",
    "scope": "所有付费计划"
  },
  "prediction": {"level": "low", "probability_24h": 0.04}
}`))
	}))
	defer server.Close()

	client := Client{
		URL: server.URL,
		Now: func() time.Time {
			return parseTime("2026-06-01T20:00:00+08:00")
		},
	}
	current, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if current.LastWindow == nil || current.LastWindow.ID != "codex-speed-window-2026-05-31-codex" {
		t.Fatalf("unexpected current: %#v", current)
	}
	text := client.RenderText(current)
	for _, want := range []string{"no active reset window", "last reset:", "20 hours ago", "duration 9h 25m", "all paid plans", "prediction: low"} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q in:\n%s", want, text)
		}
	}
}

func TestFetchRejectsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	_, err := (Client{URL: server.URL}).Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("expected 502 error, got %v", err)
	}
}

func parseTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return t
}
