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

func TestWarningEventsAndDeliveriesDedupe(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	warning := resetwatch.WarningEvent{
		ID:                 resetwatch.WarningEventID("codex", "acct", resetwatch.LabelFiveHour, parseTime("2026-05-31T17:00:00Z"), 5),
		ProviderID:         "codex",
		Account:            resetwatch.Account{Ref: "acct", Label: "personal", Email: "arda@example.com", Plan: "Plus"},
		Label:              resetwatch.LabelFiveHour,
		ThresholdRemaining: 5,
		UsedPercent:        96,
		RemainingPercent:   4,
		ResetAt:            parseTime("2026-05-31T17:00:00Z"),
		SnapshotJSON:       []byte(`{"snapshot":true}`),
		DetectedAt:         parseTime("2026-05-31T12:00:00Z"),
	}
	inserted, err := store.InsertWarningEvents(ctx, []resetwatch.WarningEvent{warning})
	if err != nil {
		t.Fatalf("insert warning: %v", err)
	}
	if len(inserted) != 1 {
		t.Fatalf("expected inserted warning, got %#v", inserted)
	}
	inserted, err = store.InsertWarningEvents(ctx, []resetwatch.WarningEvent{warning})
	if err != nil {
		t.Fatalf("reinsert warning: %v", err)
	}
	if len(inserted) != 0 {
		t.Fatalf("expected warning dedupe, got %#v", inserted)
	}
	loaded, ok, err := store.LoadWarningEvent(ctx, warning.ID)
	if err != nil || !ok || loaded.ThresholdRemaining != 5 {
		t.Fatalf("unexpected loaded warning: %#v ok=%v err=%v", loaded, ok, err)
	}
	delivery, err := store.EnsureWarningDelivery(ctx, warning.ID, "telegram:123")
	if err != nil {
		t.Fatalf("ensure warning delivery: %v", err)
	}
	if delivery.Status != "pending" || delivery.EventID != warning.ID {
		t.Fatalf("unexpected warning delivery: %#v", delivery)
	}
	if err := store.MarkWarningDeliveryAttempt(ctx, warning.ID, "telegram:123", true, "", "99"); err != nil {
		t.Fatalf("mark warning delivered: %v", err)
	}
	delivered, ok, err := store.LoadWarningDelivery(ctx, warning.ID, "telegram:123")
	if err != nil || !ok || delivered.ProviderMessageID != "99" {
		t.Fatalf("unexpected warning delivered row: %#v ok=%v err=%v", delivered, ok, err)
	}
	pending, err := store.PendingWarningDeliveries(ctx, "telegram:123", 10)
	if err != nil {
		t.Fatalf("pending warning deliveries: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending warnings, got %#v", pending)
	}
}

func TestPruneObservationsKeepsEventsAndDeliveries(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	oldObs := observation("2026-01-01T12:00:00Z", "2026-01-07T12:00:00Z", "2026-01-01T17:00:00Z")
	oldDecision := resetwatch.Decide(oldObs, nil, testOptions())
	if _, err := store.ApplyDecision(ctx, oldObs, oldDecision); err != nil {
		t.Fatalf("apply old observation: %v", err)
	}
	newObs := observation("2026-06-01T12:00:00Z", "2026-06-07T12:00:00Z", "2026-06-01T17:00:00Z")
	states, err := store.LoadWindowStates(ctx, "acct")
	if err != nil {
		t.Fatalf("load states: %v", err)
	}
	newDecision := resetwatch.Decide(newObs, states, testOptions())
	if _, err := store.ApplyDecision(ctx, newObs, newDecision); err != nil {
		t.Fatalf("apply new observation: %v", err)
	}
	event := resetEvent()
	if inserted, err := store.InsertResetEvents(ctx, []resetwatch.Event{event}); err != nil || inserted != 1 {
		t.Fatalf("insert event: inserted=%d err=%v", inserted, err)
	}
	if _, err := store.EnsureDelivery(ctx, event.ID, "telegram:123"); err != nil {
		t.Fatalf("ensure delivery: %v", err)
	}
	eventCount := countRows(t, store, "reset_events")
	deliveryCount := countRows(t, store, "notification_deliveries")

	result, err := store.PruneObservations(ctx, parseTime("2026-05-01T00:00:00Z"), false)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if result.DeletedObservations != 1 || result.DeletedWindows != 2 {
		t.Fatalf("unexpected prune result: %#v", result)
	}
	if got := countRows(t, store, "limit_observations"); got != 1 {
		t.Fatalf("unexpected observations after prune: %d", got)
	}
	if got := countRows(t, store, "observed_windows"); got != 2 {
		t.Fatalf("unexpected windows after prune: %d", got)
	}
	if got := countRows(t, store, "reset_events"); got != eventCount {
		t.Fatalf("reset events should be retained, got %d", got)
	}
	if got := countRows(t, store, "notification_deliveries"); got != deliveryCount {
		t.Fatalf("deliveries should be retained, got %d", got)
	}
	latest, ok, err := store.LoadLatestObservation(ctx)
	if err != nil || !ok || !latest.ObservedAt.Equal(newObs.ObservedAt) {
		t.Fatalf("unexpected latest after prune: %#v ok=%v err=%v", latest, ok, err)
	}
}

func TestStatsSummarizesStorageDeliveriesAndRecentRows(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	obs := observation("2026-06-01T12:00:00Z", "2026-06-07T12:00:00Z", "2026-06-01T17:00:00Z")
	decision := resetwatch.Decide(obs, nil, testOptions())
	if _, err := store.ApplyDecision(ctx, obs, decision); err != nil {
		t.Fatalf("apply observation: %v", err)
	}
	event := resetEvent()
	if _, err := store.InsertResetEvents(ctx, []resetwatch.Event{event}); err != nil {
		t.Fatalf("insert reset: %v", err)
	}
	if _, err := store.EnsureDelivery(ctx, event.ID, "telegram:123"); err != nil {
		t.Fatalf("ensure delivery: %v", err)
	}
	if err := store.MarkDeliveryAttempt(ctx, event.ID, "telegram:123", true, "", "42"); err != nil {
		t.Fatalf("mark delivery: %v", err)
	}
	warning := resetwatch.WarningEvent{
		ID:                 resetwatch.WarningEventID("codex", "acct", resetwatch.LabelFiveHour, parseTime("2026-06-01T17:00:00Z"), 5),
		ProviderID:         "codex",
		Account:            resetwatch.Account{Ref: "acct", Label: "personal", Email: "arda@example.com", Plan: "Plus"},
		Label:              resetwatch.LabelFiveHour,
		ThresholdRemaining: 5,
		UsedPercent:        96,
		RemainingPercent:   4,
		ResetAt:            parseTime("2026-06-01T17:00:00Z"),
		SnapshotJSON:       []byte(`{"snapshot":true}`),
		DetectedAt:         parseTime("2026-06-01T12:00:00Z"),
	}
	if _, err := store.InsertWarningEvents(ctx, []resetwatch.WarningEvent{warning}); err != nil {
		t.Fatalf("insert warning: %v", err)
	}

	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.SchemaVersion != SchemaVersion {
		t.Fatalf("unexpected schema version: %d", stats.SchemaVersion)
	}
	if stats.Counts["limit_observations"] != 1 || stats.Counts["observed_windows"] != 2 {
		t.Fatalf("unexpected counts: %#v", stats.Counts)
	}
	if stats.ResetDeliveries["delivered"].Count != 1 || stats.ResetDeliveries["delivered"].Attempts != 1 {
		t.Fatalf("unexpected delivery stats: %#v", stats.ResetDeliveries)
	}
	if stats.LatestObservation == nil || stats.LatestObservation.Windows != 2 {
		t.Fatalf("unexpected latest observation: %#v", stats.LatestObservation)
	}
	if stats.LastReset == nil || stats.LastReset.Kind != resetwatch.ResetKindEarly {
		t.Fatalf("unexpected last reset: %#v", stats.LastReset)
	}
	if stats.LastWarning == nil || stats.LastWarning.ThresholdRemaining != 5 {
		t.Fatalf("unexpected last warning: %#v", stats.LastWarning)
	}
	if stats.DBFiles.MainBytes == 0 {
		t.Fatalf("expected db file size, got %#v", stats.DBFiles)
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

func countRows(t *testing.T, store *Store, table string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`select count(*) from ` + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
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
