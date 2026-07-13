package delivery

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/agensfield/scriba/internal/budget"
	"github.com/agensfield/scriba/internal/radar"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
)

func TestCanonicalEnvelopeIsTargetIndependentAndMinimized(t *testing.T) {
	at := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	event := resetwatch.WarningEvent{ID: "warning-1", Account: resetwatch.Account{Ref: "acct-secret", Label: "Work", Email: "secret@example.com", Plan: "pro"}, Label: resetwatch.LabelWeeklyLimit, ThresholdRemaining: 20, UsedPercent: 81, RemainingPercent: 19, ResetAt: at.Add(7 * 24 * time.Hour), SnapshotJSON: []byte(`{"token":"secret-token"}`), DetectedAt: at}
	payload, err := store.EncodeOutboxPayload("limit_warning", event)
	if err != nil {
		t.Fatal(err)
	}
	base := store.OutboxMessage{EventKind: "limit_warning", Source: "scriba-v7", ProfileRef: "work", AccountRef: event.Account.Ref, EventID: event.ID, PayloadVersion: 1, PayloadJSON: payload}
	base.Target = "webhook:one"
	one, err := FromOutbox(base)
	if err != nil {
		t.Fatal(err)
	}
	base.ID, base.Target = "other-outbox-id", "ntfy:phone"
	two, err := FromOutbox(base)
	if err != nil {
		t.Fatal(err)
	}
	oneJSON, _ := Marshal(one)
	twoJSON, _ := Marshal(two)
	if !bytes.Equal(oneJSON, twoJSON) {
		t.Fatalf("adapter bodies differ:\n%s\n%s", oneJSON, twoJSON)
	}
	text := string(oneJSON)
	for _, forbidden := range []string{"acct-secret", "secret@example.com", "secret-token", "snapshot", "webhook:one", "ntfy:phone", "other-outbox-id"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("envelope leaked %q: %s", forbidden, text)
		}
	}
	want := `{"schemaVersion":"scriba.notification.v1","eventId":"warning-1","eventKind":"limit_warning","source":"scriba-v7","profileId":"work","occurredAt":"2026-07-13T12:00:00Z","data":{"accountLabel":"Work","label":"Weekly limit","thresholdRemaining":20,"usedPercent":81,"remainingPercent":19,"resetAt":"2026-07-20T12:00:00Z"}}`
	if text != want {
		t.Fatalf("envelope=%s", text)
	}
}

func TestCanonicalEnvelopeSupportsEveryOutboxKind(t *testing.T) {
	at := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	account := resetwatch.Account{Ref: "acct", Label: "Personal"}
	tests := []struct {
		kind  string
		event any
	}{
		{"reset", resetwatch.Event{ID: "reset", Account: account, PrimaryTriggerLabel: "Weekly limit", SecondaryTriggerLabels: []string{}, ResetKind: "scheduled", PreviousResetAt: at, CurrentResetAt: at.Add(7 * 24 * time.Hour), DetectedAt: at, PreviousSnapshotJSON: []byte(`{}`), CurrentSnapshotJSON: []byte(`{}`)}},
		{"limit_warning", resetwatch.WarningEvent{ID: "warning", Account: account, Label: "Weekly limit", ResetAt: at.Add(time.Hour), SnapshotJSON: []byte(`{}`), DetectedAt: at}},
		{"pacing_warning", budget.PacingAlert{ID: "pacing", AccountRef: account.Ref, AccountLabel: account.Label, WindowKey: "primary.weekly", Label: "Weekly limit", Risk: "high", Confidence: "low", UsedPercent: 40, RemainingPercentPoints: 60, PacePercentPointsPerHour: 1.65, SafePercentPointsPerHour: .42, ProjectedExhaustionAt: at.Add(48 * time.Hour), ResetAt: at.Add(7 * 24 * time.Hour), DetectedAt: at}},
		{"reset_grant_warning", resetwatch.GrantExpiryWarning{ID: "grant-warning", Account: account, CreditTitle: "Full reset", ExpiresAt: at.Add(24 * time.Hour), SnapshotJSON: []byte(`{}`), DetectedAt: at}},
		{"reset_grant", resetwatch.ResetGrantEvent{ID: "grant", Account: account, CreditTitle: "Full reset", GrantedAt: at, ExpiresAt: at.Add(24 * time.Hour), SnapshotJSON: []byte(`{}`), DetectedAt: at}},
		{"radar_alert", radar.ProbabilityAlert{ID: "radar", Milestone: 50, DetectedAt: at, SnapshotJSON: []byte(`{}`)}},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			payload, err := store.EncodeOutboxPayload(test.kind, test.event)
			if err != nil {
				t.Fatal(err)
			}
			eventID := test.kind
			switch test.kind {
			case "reset_grant_warning":
				eventID = "grant-warning"
			case "radar_alert":
				eventID = "radar"
			}
			envelope, err := FromOutbox(store.OutboxMessage{EventKind: test.kind, Source: "test", ProfileRef: "default", AccountRef: account.Ref, EventID: eventID, PayloadVersion: 1, PayloadJSON: payload})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Marshal(envelope); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCanonicalEnvelopeBoundsProviderAndOperatorStrings(t *testing.T) {
	at := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	event := radar.ProbabilityAlert{ID: "radar", Milestone: 50, ReasoningSummary: strings.Repeat("🔥", 2000), DetectedAt: at, SnapshotJSON: []byte(`{}`)}
	payload, err := store.EncodeOutboxPayload("radar_alert", event)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := FromOutbox(store.OutboxMessage{EventKind: "radar_alert", EventID: "radar", Source: "test", PayloadVersion: 1, PayloadJSON: payload})
	if err != nil {
		t.Fatal(err)
	}
	body, err := Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 4096 || !utf8.Valid(body) {
		t.Fatalf("body length=%d valid=%t", len(body), utf8.Valid(body))
	}
}
