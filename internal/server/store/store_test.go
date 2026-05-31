package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/resetwatch"
)

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scriba-server.sqlite")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	defer func() { _ = second.Close() }()
	version, err := second.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if version != SchemaVersion {
		t.Fatalf("unexpected schema version: %d", version)
	}
}

func TestApplyDecisionStoresObservationWindowsAndDedupesEvents(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	baseline := observation("2026-05-31T12:00:00Z", "2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z")
	baselineDecision := resetwatch.Decide(baseline, nil, testOptions())
	inserted, err := store.ApplyDecision(ctx, baseline, baselineDecision)
	if err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("baseline inserted events: %d", inserted)
	}
	states, err := store.LoadWindowStates(ctx, "acct")
	if err != nil {
		t.Fatalf("load states: %v", err)
	}
	if got := states[resetwatch.StateKey("acct", resetwatch.LabelWeeklyLimit)].StableResetAt.Format(time.RFC3339); got != "2026-06-06T21:00:00Z" {
		t.Fatalf("unexpected stable reset: %s", got)
	}

	resetObs := observation("2026-06-02T12:00:00Z", "2026-06-09T12:00:00Z", "2026-06-02T17:00:00Z")
	resetDecision := resetwatch.Decide(resetObs, states, testOptions())
	inserted, err = store.ApplyDecision(ctx, resetObs, resetDecision)
	if err != nil {
		t.Fatalf("apply reset: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("expected one inserted event, got %d", inserted)
	}
	inserted, err = store.ApplyDecision(ctx, resetObs, resetDecision)
	if err != nil {
		t.Fatalf("reapply reset: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("expected event dedupe, got %d", inserted)
	}

	event, ok, err := store.LoadResetEvent(ctx, resetDecision.Events[0].ID)
	if err != nil {
		t.Fatalf("load event: %v", err)
	}
	if !ok {
		t.Fatal("event not found")
	}
	if event.Account.Email != "arda@example.com" || event.ResetKind != resetwatch.ResetKindEarly {
		t.Fatalf("unexpected event: %#v", event)
	}

	latest, ok, err := store.LoadLatestObservation(ctx)
	if err != nil {
		t.Fatalf("latest observation: %v", err)
	}
	if !ok {
		t.Fatal("latest observation not found")
	}
	if !latest.ObservedAt.Equal(resetObs.ObservedAt) || latest.Account.Email != "arda@example.com" {
		t.Fatalf("unexpected latest observation: %#v", latest)
	}
	if len(latest.Windows) != 2 {
		t.Fatalf("expected two latest windows, got %#v", latest.Windows)
	}
}

func TestDeliveriesSettingsAndTelegramOffset(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	event := resetEvent()
	if inserted, err := store.InsertResetEvents(ctx, []resetwatch.Event{event}); err != nil || inserted != 1 {
		t.Fatalf("insert event: inserted=%d err=%v", inserted, err)
	}

	delivery, err := store.EnsureDelivery(ctx, event.ID, "telegram:123")
	if err != nil {
		t.Fatalf("ensure delivery: %v", err)
	}
	if delivery.Status != "pending" || delivery.Attempts != 0 {
		t.Fatalf("unexpected delivery: %#v", delivery)
	}
	again, err := store.EnsureDelivery(ctx, event.ID, "telegram:123")
	if err != nil {
		t.Fatalf("ensure delivery again: %v", err)
	}
	if again.ID != delivery.ID {
		t.Fatalf("delivery id changed: %s != %s", again.ID, delivery.ID)
	}

	if err := store.MarkDeliveryAttempt(ctx, event.ID, "telegram:123", false, "telegram down", ""); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	delivery, ok, err := store.LoadDelivery(ctx, event.ID, "telegram:123")
	if err != nil {
		t.Fatalf("load failed delivery: %v", err)
	}
	if !ok || delivery.Attempts != 1 || delivery.LastError != "telegram down" || delivery.NextAttemptAt == nil {
		t.Fatalf("unexpected failed delivery: %#v", delivery)
	}
	pending, err := store.PendingDeliveries(ctx, "telegram:123", 10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected failed delivery to wait for backoff: %#v", pending)
	}
	if err := store.MarkDeliveryAttempt(ctx, event.ID, "telegram:123", true, "", "42"); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	delivered, ok, err := store.LoadDelivery(ctx, event.ID, "telegram:123")
	if err != nil || !ok || delivered.ProviderMessageID != "42" {
		t.Fatalf("unexpected delivered row: %#v ok=%v err=%v", delivered, ok, err)
	}
	pending, err = store.PendingDeliveries(ctx, "telegram:123", 10)
	if err != nil {
		t.Fatalf("pending after delivered: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending deliveries: %#v", pending)
	}

	if err := store.SetSetting(ctx, "poll_interval", "5m"); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	value, ok, err := store.GetSetting(ctx, "poll_interval")
	if err != nil || !ok || value != "5m" {
		t.Fatalf("unexpected setting value=%q ok=%v err=%v", value, ok, err)
	}
	if err := store.SetTelegramOffset(ctx, "codexusagebot", 42); err != nil {
		t.Fatalf("set offset: %v", err)
	}
	offset, ok, err := store.GetTelegramOffset(ctx, "codexusagebot")
	if err != nil || !ok || offset != 42 {
		t.Fatalf("unexpected offset=%d ok=%v err=%v", offset, ok, err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "scriba-server.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func observation(observedAt, weeklyReset, fiveReset string) resetwatch.Observation {
	weekly := 51.0
	five := 96.0
	return resetwatch.Observation{
		ProviderID: "codex",
		Account:    resetwatch.Account{Ref: "acct", Label: "personal", Email: "arda@example.com", Plan: "Plus"},
		ObservedAt: parseTime(observedAt),
		Windows: []resetwatch.Window{
			{Label: resetwatch.LabelWeeklyLimit, UsedPercent: &weekly, ResetAt: parseTime(weeklyReset)},
			{Label: resetwatch.LabelFiveHour, UsedPercent: &five, ResetAt: parseTime(fiveReset)},
		},
		SnapshotJSON: []byte(`{"snapshot":true}`),
	}
}

func resetEvent() resetwatch.Event {
	return resetwatch.Event{
		ID:                  resetwatch.EventID("codex", "acct", resetwatch.LabelWeeklyLimit, parseTime("2026-06-09T12:00:00Z")),
		ProviderID:          "codex",
		Account:             resetwatch.Account{Ref: "acct", Label: "personal", Email: "arda@example.com", Plan: "Plus"},
		PrimaryTriggerLabel: resetwatch.LabelWeeklyLimit,
		ResetKind:           resetwatch.ResetKindEarly,
		PreviousResetAt:     parseTime("2026-06-06T21:00:00Z"),
		CurrentResetAt:      parseTime("2026-06-09T12:00:00Z"),
		DetectedAt:          parseTime("2026-06-02T12:00:00Z"),
		JokeID:              "test-joke",
	}
}

func testOptions() resetwatch.Options {
	return resetwatch.Options{ClockJitter: time.Minute, DueJitter: 10 * time.Minute, JokeChooser: resetwatch.JokeChooserFunc(func(resetwatch.Event) string { return "test-joke" })}
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}
