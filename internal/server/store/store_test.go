package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/radar"
	"github.com/agensfield/scriba/internal/resetwatch"
)

func TestSQLiteConnectionPragmasWALPoolAndFileMode(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "scriba-server.sqlite")
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := store.db.Stats().MaxOpenConnections; got != 4 {
		t.Fatalf("max connections = %d", got)
	}
	connections := make([]*sql.Conn, 0, 4)
	for i := 0; i < 4; i++ {
		conn, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		connections = append(connections, conn)
		var busy, foreign int
		if err := conn.QueryRowContext(ctx, `pragma busy_timeout`).Scan(&busy); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(ctx, `pragma foreign_keys`).Scan(&foreign); err != nil {
			t.Fatal(err)
		}
		if busy != 5000 || foreign != 1 {
			t.Fatalf("connection %d pragmas: busy=%d foreign=%d", i, busy, foreign)
		}
	}
	for _, conn := range connections {
		_ = conn.Close()
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o", got)
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if sidecarInfo, err := os.Stat(sidecar); err == nil && sidecarInfo.Mode().Perm()&0o077 != 0 {
			t.Fatalf("sidecar %s mode = %o", sidecar, sidecarInfo.Mode().Perm())
		}
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	var mode string
	if err := reopened.db.QueryRow(`pragma journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal mode = %q", mode)
	}
}

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
	if latest.ResetGrants.AvailableCount == nil || *latest.ResetGrants.AvailableCount != 1 {
		t.Fatalf("expected latest reset grants from snapshot, got %#v", latest.ResetGrants)
	}
	if got := latest.ResetGrants.ExpiresAt.Format(time.RFC3339Nano); got != "2026-07-12T01:20:48.728491Z" {
		t.Fatalf("unexpected latest grant expiry: %s", got)
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
	if err := store.MarkDeliverySending(ctx, event.ID, "telegram:123"); err != nil {
		t.Fatalf("mark sending: %v", err)
	}
	delivery, ok, err := store.LoadDelivery(ctx, event.ID, "telegram:123")
	if err != nil || !ok || delivery.Status != "sending" || delivery.NextAttemptAt == nil {
		t.Fatalf("unexpected sending delivery: %#v ok=%v err=%v", delivery, ok, err)
	}
	pending, err := store.PendingDeliveries(ctx, "telegram:123", 10)
	if err != nil {
		t.Fatalf("pending after sending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("sending delivery should be leased away from retry loop: %#v", pending)
	}

	if err := store.MarkDeliveryAttempt(ctx, event.ID, "telegram:123", false, "telegram down", ""); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	delivery, ok, err = store.LoadDelivery(ctx, event.ID, "telegram:123")
	if err != nil {
		t.Fatalf("load failed delivery: %v", err)
	}
	if !ok || delivery.Attempts != 1 || delivery.LastError != "telegram down" || delivery.NextAttemptAt == nil {
		t.Fatalf("unexpected failed delivery: %#v", delivery)
	}
	pending, err = store.PendingDeliveries(ctx, "telegram:123", 10)
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
	var updatedAt string
	if err := store.db.QueryRowContext(ctx, `select updated_at from telegram_offsets where bot_ref = ?`, "codexusagebot").Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTelegramOffset(ctx, "codexusagebot", 41); err != nil {
		t.Fatal(err)
	}
	var unchanged string
	if err := store.db.QueryRowContext(ctx, `select updated_at from telegram_offsets where bot_ref = ?`, "codexusagebot").Scan(&unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged != updatedAt {
		t.Fatalf("updated_at changed on regression: %q != %q", unchanged, updatedAt)
	}
	if err := store.SetTelegramOffset(ctx, "codexusagebot", 43); err != nil {
		t.Fatal(err)
	}
	offset, _, err = store.GetTelegramOffset(ctx, "codexusagebot")
	if err != nil || offset != 43 {
		t.Fatalf("advanced offset=%d err=%v", offset, err)
	}
	var advanced string
	if err := store.db.QueryRowContext(ctx, `select updated_at from telegram_offsets where bot_ref = ?`, "codexusagebot").Scan(&advanced); err != nil {
		t.Fatal(err)
	}
	if advanced == unchanged {
		t.Fatalf("updated_at did not change on advance: %q", advanced)
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

func TestGrantExpiryWarningEventsAndDeliveriesDedupe(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	warning := resetwatch.GrantExpiryWarning{
		ID:            resetwatch.GrantExpiryWarningID("codex", "acct", "credit_1", parseTime("2026-07-12T01:20:48Z"), 5),
		ProviderID:    "codex",
		Account:       resetwatch.Account{Ref: "acct", Label: "personal", Email: "arda@example.com", Plan: "Plus"},
		CreditID:      "credit_1",
		CreditTitle:   "Rate limit reset",
		ThresholdDays: 5,
		ExpiresAt:     parseTime("2026-07-12T01:20:48Z"),
		SnapshotJSON:  []byte(`{"snapshot":true}`),
		DetectedAt:    parseTime("2026-07-07T01:20:48Z"),
	}
	inserted, err := store.InsertGrantExpiryWarningEvents(ctx, []resetwatch.GrantExpiryWarning{warning})
	if err != nil {
		t.Fatalf("insert grant warning: %v", err)
	}
	if len(inserted) != 1 {
		t.Fatalf("expected inserted grant warning, got %#v", inserted)
	}
	inserted, err = store.InsertGrantExpiryWarningEvents(ctx, []resetwatch.GrantExpiryWarning{warning})
	if err != nil {
		t.Fatalf("reinsert grant warning: %v", err)
	}
	if len(inserted) != 0 {
		t.Fatalf("expected grant warning dedupe, got %#v", inserted)
	}
	loaded, ok, err := store.LoadGrantExpiryWarningEvent(ctx, warning.ID)
	if err != nil || !ok || loaded.ThresholdDays != 5 || loaded.CreditID != "credit_1" {
		t.Fatalf("unexpected loaded grant warning: %#v ok=%v err=%v", loaded, ok, err)
	}
	delivery, err := store.EnsureGrantExpiryWarningDelivery(ctx, warning.ID, "telegram:123")
	if err != nil {
		t.Fatalf("ensure grant warning delivery: %v", err)
	}
	if delivery.Status != "pending" || delivery.EventID != warning.ID {
		t.Fatalf("unexpected grant warning delivery: %#v", delivery)
	}
	if err := store.MarkGrantExpiryWarningDeliveryAttempt(ctx, warning.ID, "telegram:123", true, "", "77"); err != nil {
		t.Fatalf("mark grant warning delivered: %v", err)
	}
	delivered, ok, err := store.LoadGrantExpiryWarningDelivery(ctx, warning.ID, "telegram:123")
	if err != nil || !ok || delivered.ProviderMessageID != "77" {
		t.Fatalf("unexpected grant warning delivered row: %#v ok=%v err=%v", delivered, ok, err)
	}
	pending, err := store.PendingGrantExpiryWarningDeliveries(ctx, "telegram:123", 10)
	if err != nil {
		t.Fatalf("pending grant warning deliveries: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending grant warnings, got %#v", pending)
	}
}

func TestResetGrantEventsSeedThenNotifyNewCredits(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	firstObs := observation("2026-06-12T01:30:00Z", "2026-06-14T21:00:00Z", "2026-06-12T06:00:00Z")
	firstObs.ResetGrants = resetwatch.ResetGrants{
		AvailableCount: ptrInt(1),
		Credits: []resetwatch.ResetCredit{
			{ID: "credit_1", Status: "available", Title: "Full reset", ResetType: "codex_rate_limits", GrantedAt: parseTime("2026-06-12T01:20:48Z"), ExpiresAt: parseTime("2026-07-12T01:20:48Z")},
		},
	}
	inserted, err := store.InsertResetGrantEvents(ctx, firstObs, resetwatch.ResetGrantEventCandidates(firstObs))
	if err != nil {
		t.Fatalf("seed reset grants: %v", err)
	}
	if len(inserted) != 0 {
		t.Fatalf("first observation should seed silently, got %#v", inserted)
	}
	secondObs := observation("2026-06-18T00:40:00Z", "2026-06-21T21:00:00Z", "2026-06-18T05:00:00Z")
	secondObs.ResetGrants = resetwatch.ResetGrants{
		AvailableCount: ptrInt(2),
		Credits: []resetwatch.ResetCredit{
			{ID: "credit_1", Status: "available", Title: "Full reset", ResetType: "codex_rate_limits", GrantedAt: parseTime("2026-06-12T01:20:48Z"), ExpiresAt: parseTime("2026-07-12T01:20:48Z")},
			{ID: "credit_2", Status: "available", Title: "Full reset", ResetType: "codex_rate_limits", GrantedAt: parseTime("2026-06-18T00:29:25Z"), ExpiresAt: parseTime("2026-07-18T00:29:25Z")},
		},
	}
	inserted, err = store.InsertResetGrantEvents(ctx, secondObs, resetwatch.ResetGrantEventCandidates(secondObs))
	if err != nil {
		t.Fatalf("insert new reset grant: %v", err)
	}
	if len(inserted) != 1 || inserted[0].CreditID != "credit_2" {
		t.Fatalf("expected only new credit, got %#v", inserted)
	}
	inserted, err = store.InsertResetGrantEvents(ctx, secondObs, resetwatch.ResetGrantEventCandidates(secondObs))
	if err != nil {
		t.Fatalf("reinsert new reset grant: %v", err)
	}
	if len(inserted) != 0 {
		t.Fatalf("expected grant dedupe, got %#v", inserted)
	}
	loaded, ok, err := store.LoadResetGrantEvent(ctx, resetwatch.ResetGrantEventCandidates(secondObs)[1].ID)
	if err != nil || !ok || loaded.AvailableCount != 2 || loaded.CreditID != "credit_2" {
		t.Fatalf("unexpected loaded reset grant: %#v ok=%v err=%v", loaded, ok, err)
	}
	delivery, err := store.EnsureResetGrantDelivery(ctx, loaded.ID, "telegram:123")
	if err != nil {
		t.Fatalf("ensure reset grant delivery: %v", err)
	}
	if delivery.Status != "pending" || delivery.EventID != loaded.ID {
		t.Fatalf("unexpected reset grant delivery: %#v", delivery)
	}
	if err := store.MarkResetGrantDeliveryAttempt(ctx, loaded.ID, "telegram:123", true, "", "88"); err != nil {
		t.Fatalf("mark reset grant delivered: %v", err)
	}
	delivered, ok, err := store.LoadResetGrantDelivery(ctx, loaded.ID, "telegram:123")
	if err != nil || !ok || delivered.ProviderMessageID != "88" {
		t.Fatalf("unexpected reset grant delivered row: %#v ok=%v err=%v", delivered, ok, err)
	}
	pending, err := store.PendingResetGrantDeliveries(ctx, "telegram:123", 10)
	if err != nil {
		t.Fatalf("pending reset grant deliveries: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending reset grant deliveries, got %#v", pending)
	}
}

func TestRadarAlertEventsAndDeliveries(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	alert := radar.ProbabilityAlert{
		ID:               radar.ProbabilityAlertID("2026-06-01T12:00:00Z", 50),
		Milestone:        50,
		Probability24H:   0.64,
		Probability48H:   0.78,
		Level:            "high",
		ExpectedWindow:   "24-48h",
		ReasoningSummary: "test",
		CheckedAt:        "2026-06-01T12:00:00Z",
		DetectedAt:       parseTime("2026-06-01T12:01:00Z"),
		SnapshotJSON:     []byte(`{"ok":true}`),
	}
	inserted, err := store.InsertRadarAlertEvent(ctx, alert)
	if err != nil || !inserted {
		t.Fatalf("insert radar alert: inserted=%t err=%v", inserted, err)
	}
	inserted, err = store.InsertRadarAlertEvent(ctx, alert)
	if err != nil || inserted {
		t.Fatalf("dedupe radar alert: inserted=%t err=%v", inserted, err)
	}
	loaded, ok, err := store.LoadRadarAlertEvent(ctx, alert.ID)
	if err != nil || !ok || loaded.Milestone != 50 || loaded.Probability24H != 0.64 {
		t.Fatalf("unexpected radar alert: %#v ok=%v err=%v", loaded, ok, err)
	}
	delivery, err := store.EnsureRadarAlertDelivery(ctx, alert.ID, "telegram:123")
	if err != nil {
		t.Fatalf("ensure radar delivery: %v", err)
	}
	if delivery.Status != "pending" || delivery.EventID != alert.ID {
		t.Fatalf("unexpected radar delivery: %#v", delivery)
	}
	if err := store.MarkRadarAlertDeliveryAttempt(ctx, alert.ID, "telegram:123", true, "", "55"); err != nil {
		t.Fatalf("mark radar delivery: %v", err)
	}
	delivered, ok, err := store.LoadRadarAlertDelivery(ctx, alert.ID, "telegram:123")
	if err != nil || !ok || delivered.Status != "delivered" || delivered.ProviderMessageID != "55" {
		t.Fatalf("unexpected delivered radar row: %#v ok=%v err=%v", delivered, ok, err)
	}
	pending, err := store.PendingRadarAlertDeliveries(ctx, "telegram:123", 10)
	if err != nil {
		t.Fatalf("pending radar deliveries: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending radar deliveries, got %#v", pending)
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

func TestPruneObservationsBoundsTerminalQueuesAndEventHistory(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	if _, err := store.db.Exec(`
insert into accounts(account_ref,provider_id,label,email,plan,updated_at) values('retention','codex','retention','','pro','2026-06-01T00:00:00Z');
insert into policy_events(id,semantic_key,event_kind,semantic_event_id,rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,payload_version,payload_json,detected_at,created_at) values
 ('terminal-event','terminal-key','limit_warning','terminal-semantic','rule','weekly_limit','remaining_checkpoint','codex','retention','rev','hash',1,'{}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('active-event','active-key','limit_warning','active-semantic','rule','weekly_limit','remaining_checkpoint','codex','retention','rev','hash',1,'{}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
insert into notification_outbox(id,event_kind,source,profile_ref,account_ref,event_id,target,payload_version,payload_json,status,attempts,available_at,delivered_at,created_at,updated_at) values
 ('terminal-outbox','limit_warning','policy','default','retention','terminal-semantic','telegram:1',1,'{}','delivered',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('active-outbox','limit_warning','policy','default','retention','active-semantic','telegram:1',1,'{}','pending',0,'2026-01-01T00:00:00Z',null,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
insert into telegram_updates(bot_ref,update_id,raw_json,status,attempts,available_at,processed_at,created_at,updated_at) values
 ('bot',1,'{}','processed',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('bot',2,'{}','pending',0,'2026-01-01T00:00:00Z',null,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');`); err != nil {
		t.Fatalf("seed retention rows: %v", err)
	}
	var highWater int64
	if err := store.db.QueryRow(`select seq from sqlite_sequence where name='policy_event_replay'`).Scan(&highWater); err != nil {
		t.Fatalf("load replay high-water: %v", err)
	}
	var eligible int
	if err := store.db.QueryRow(`select count(*) from policy_events where detected_at < ? and not exists(select 1 from notification_outbox o where o.event_kind=policy_events.event_kind and o.event_id=policy_events.semantic_event_id and o.status in ('pending','leased'))`, "2026-05-01T00:00:00.000000000Z").Scan(&eligible); err != nil || eligible != 1 {
		t.Fatalf("eligible policy events=%d err=%v", eligible, err)
	}

	result, err := store.PruneObservations(ctx, parseTime("2026-05-01T00:00:00Z"), false)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if result.DeletedEvents != 1 || result.DeletedDeliveries != 1 || result.DeletedReplayRows != 0 || result.DeletedInboxRows != 1 {
		t.Fatalf("unexpected prune result: %#v", result)
	}
	for table, want := range map[string]int{"policy_events": 1, "policy_event_replay": 2, "notification_outbox": 1, "telegram_updates": 1} {
		if got := countRows(t, store, table); got != want {
			t.Fatalf("%s rows=%d want=%d", table, got, want)
		}
	}
	var replayHighWater int64
	if err := store.db.QueryRow(`select seq from sqlite_sequence where name='policy_event_replay'`).Scan(&replayHighWater); err != nil || replayHighWater != highWater {
		t.Fatalf("replay high-water=%d want=%d err=%v", replayHighWater, highWater, err)
	}
	page, err := store.LoadPolicyEventReplay(ctx, "codex", "retention", 0, 0, 10)
	if err != nil {
		t.Fatalf("load retained replay: %v", err)
	}
	if page.HighWater != highWater || page.OldestAvailable != highWater || page.PrunedThrough != highWater-1 || len(page.Events) != 2 || page.Events[0].PolicyEventID != "" || page.Events[1].PolicyEventID != "active-event" {
		t.Fatalf("unexpected retained replay: %#v", page)
	}
}

func TestPruneObservationsUsesExactTimestampAndTerminalBoundaries(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.db.Exec(`
insert into telegram_updates(bot_ref,update_id,raw_json,status,attempts,available_at,processed_at,dead_at,created_at,updated_at) values
 ('bot',1,'{}','processed',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',null,'2026-01-01T00:00:00Z','2026-05-01T00:00:00Z'),
 ('bot',2,'{}','dead',1,'2026-01-01T00:00:00Z',null,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-05-01T00:00:00.000000001Z'),
 ('bot',3,'{}','processed',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',null,'2026-01-01T00:00:00Z','2026-05-01T00:00:00.000000002Z'),
 ('bot',4,'{}','processed',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',null,'2026-01-01T00:00:00Z','2026-05-01T00:00:00.000000003Z'),
 ('bot',5,'{}','pending',0,'2026-01-01T00:00:00Z',null,null,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	result, err := store.PruneObservations(t.Context(), parseTime("2026-05-01T00:00:00.000000002Z"), false)
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedInboxRows != 2 {
		t.Fatalf("deleted inbox rows=%d", result.DeletedInboxRows)
	}
	if got := countRows(t, store, "telegram_updates"); got != 3 {
		t.Fatalf("retained inbox rows=%d", got)
	}
}

func TestPruneObservationsBoundsPacingWarningsWithoutDeletingActiveWork(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.db.Exec(`
insert into accounts(account_ref,provider_id,label,email,plan,updated_at) values('pace-retention','codex','retention','','pro','2026-06-01T00:00:00Z');
insert into pacing_warning_events(id,provider_id,account_ref,account_label,window_key,label,risk,confidence,used_percent,remaining_percent,pace_per_hour,safe_per_hour,projected_exhaustion_at,reset_at,detected_at,created_at) values
 ('old-terminal','codex','pace-retention','retention','primary.weekly','Weekly limit','high','low',40,60,1.5,.5,'2026-01-03T00:00:00Z','2026-01-07T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('old-active','codex','pace-retention','retention','primary.five_hour','5h limit','high','low',40,60,20,12,'2026-01-01T02:00:00Z','2026-01-01T05:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
insert into notification_outbox(id,event_kind,source,profile_ref,account_ref,event_id,target,payload_version,payload_json,status,attempts,available_at,delivered_at,created_at,updated_at) values
 ('pace-terminal','pacing_warning','budget-v1','default','pace-retention','old-terminal','telegram:1',1,'{}','delivered',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('pace-active','pacing_warning','budget-v1','default','pace-retention','old-active','telegram:1',1,'{}','pending',0,'2026-01-01T00:00:00Z',null,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	result, err := store.PruneObservations(t.Context(), parseTime("2026-05-01T00:00:00Z"), false)
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedEvents != 1 || result.DeletedDeliveries != 1 || countRows(t, store, "pacing_warning_events") != 1 || countRows(t, store, "notification_outbox") != 1 {
		t.Fatalf("unexpected pacing retention result: %#v", result)
	}
}

func TestPruneObservationsRollsBackEveryTableOnInvalidTimestamp(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.db.Exec(`
insert into accounts(account_ref,provider_id,label,email,plan,updated_at) values('rollback','codex','rollback','','pro','2026-01-01T00:00:00Z');
insert into policy_events(id,semantic_key,event_kind,semantic_event_id,rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,payload_version,payload_json,detected_at,created_at) values('malformed','malformed','limit_warning','malformed','rule','weekly_limit','remaining_checkpoint','codex','rollback','rev','hash',1,'{}','2026-01-01bad','2026-01-01T00:00:00Z');
insert into telegram_updates(bot_ref,update_id,raw_json,status,attempts,available_at,processed_at,created_at,updated_at) values('bot',1,'{}','processed',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PruneObservations(t.Context(), parseTime("2026-05-01T00:00:00Z"), false); err == nil {
		t.Fatal("prune accepted malformed timestamp")
	}
	if got := countRows(t, store, "telegram_updates"); got != 1 {
		t.Fatalf("committed partial inbox prune: rows=%d", got)
	}
	var violations int
	rows, err := store.db.Query(`pragma foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		violations++
	}
	_ = rows.Close()
	if violations != 0 {
		t.Fatalf("foreign-key violations=%d", violations)
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
	grantWarning := resetwatch.GrantExpiryWarning{
		ID:            resetwatch.GrantExpiryWarningID("codex", "acct", "credit_1", parseTime("2026-07-12T01:20:48Z"), 5),
		ProviderID:    "codex",
		Account:       resetwatch.Account{Ref: "acct", Label: "personal", Email: "arda@example.com", Plan: "Plus"},
		CreditID:      "credit_1",
		ThresholdDays: 5,
		ExpiresAt:     parseTime("2026-07-12T01:20:48Z"),
		SnapshotJSON:  []byte(`{"snapshot":true}`),
		DetectedAt:    parseTime("2026-07-07T01:20:48Z"),
	}
	if _, err := store.InsertGrantExpiryWarningEvents(ctx, []resetwatch.GrantExpiryWarning{grantWarning}); err != nil {
		t.Fatalf("insert grant warning: %v", err)
	}
	if _, err := store.EnsureGrantExpiryWarningDelivery(ctx, grantWarning.ID, "telegram:123"); err != nil {
		t.Fatalf("ensure grant warning delivery: %v", err)
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
	if stats.LastGrantWarning == nil || stats.LastGrantWarning.ThresholdDays != 5 {
		t.Fatalf("unexpected last grant warning: %#v", stats.LastGrantWarning)
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
	grants := 1.0
	return resetwatch.Observation{
		ProviderID: "codex",
		Account:    resetwatch.Account{Ref: "acct", Label: "personal", Email: "arda@example.com", Plan: "Plus"},
		ObservedAt: parseTime(observedAt),
		Windows: []resetwatch.Window{
			{Label: resetwatch.LabelWeeklyLimit, UsedPercent: &weekly, ResetAt: parseTime(weeklyReset)},
			{Label: resetwatch.LabelFiveHour, UsedPercent: &five, ResetAt: parseTime(fiveReset)},
		},
		SnapshotJSON: resetwatch.SnapshotJSON(struct {
			Lines []model.MetricLine `json:"lines"`
		}{
			Lines: []model.MetricLine{
				{Type: "amount", Label: resetwatch.LabelResetGrants, Value: grants, Format: &model.MetricFormat{Kind: "count", Suffix: "available"}},
				{Type: "text", Label: resetwatch.LabelGrantExpiry, Value: "2026-07-12T01:20:48.728491Z"},
			},
		}),
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

func ptrInt(value int) *int {
	return &value
}
