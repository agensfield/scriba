package delivery

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/radar"
	"github.com/agensfield/scriba/internal/server/store"
)

func TestRealOutboxDispatchesSameCanonicalEventToWebhookAndNtfy(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "server.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })

	var webhookBody, ntfyMessage []byte
	webhookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webhookBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(webhookServer.Close)
	ntfyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		ntfyMessage = []byte(payload.Message)
		_, _ = io.WriteString(w, `{"id":"ntfy-1"}`)
	}))
	t.Cleanup(ntfyServer.Close)

	at := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	alert := radar.ProbabilityAlert{ID: "radar-parity", Milestone: 50, Probability24H: 0.6, Level: "high", DetectedAt: at, SnapshotJSON: []byte(`{"private":"snapshot"}`)}
	if inserted, err := state.InsertRadarAlertEvent(t.Context(), alert, "webhook:one", "ntfy:phone"); err != nil || !inserted {
		t.Fatalf("inserted=%t err=%v", inserted, err)
	}
	dispatchAt := time.Now().UTC().Add(time.Minute)
	webhook := Webhook{ID: "one", URL: webhookServer.URL, Secret: []byte("secret")}
	ntfy := Ntfy{ID: "phone", URL: ntfyServer.URL, Topic: "scriba_test"}
	if processed, err := (Dispatcher{Store: state, Adapter: webhook, Now: func() time.Time { return dispatchAt }}).DispatchOnce(t.Context()); err != nil || processed != 1 {
		t.Fatalf("webhook processed=%d err=%v", processed, err)
	}
	if processed, err := (Dispatcher{Store: state, Adapter: ntfy, Now: func() time.Time { return dispatchAt }}).DispatchOnce(t.Context()); err != nil || processed != 1 {
		t.Fatalf("ntfy processed=%d err=%v", processed, err)
	}
	if string(webhookBody) != string(ntfyMessage) || len(webhookBody) == 0 {
		t.Fatalf("webhook=%s ntfy=%s", webhookBody, ntfyMessage)
	}
	if bytes.Contains(webhookBody, []byte("private")) || bytes.Contains(webhookBody, []byte("snapshot")) {
		t.Fatalf("raw snapshot escaped: %s", webhookBody)
	}
	rows, err := state.ListOutbox(t.Context(), store.OutboxFilter{Limit: 10})
	if err != nil || len(rows) != 2 || rows[0].Status != "delivered" || rows[1].Status != "delivered" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}
