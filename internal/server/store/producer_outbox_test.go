package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/radar"
	"github.com/agensfield/scriba/internal/resetwatch"
)

func TestV7ProducersEnqueueTypedPayloadsOnce(t *testing.T) {
	ctx, target := context.Background(), "telegram:producer"
	s := openTestStore(t)
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"primary", "codex", "Primary", true, true}}); err != nil {
		t.Fatal(err)
	}
	acct := resetwatch.Account{Ref: "acct", Label: "personal", Email: "a@example.com", Plan: "Plus"}
	now := parseTime("2026-07-12T12:00:00Z")
	warning := resetwatch.WarningEvent{ID: "warning-1", ProviderID: "codex", Account: acct, Label: resetwatch.LabelFiveHour, ThresholdRemaining: 5, UsedPercent: 96, RemainingPercent: 4, ResetAt: now.Add(time.Hour), SnapshotJSON: []byte(`{"warning":true}`), DetectedAt: now}
	grantWarning := resetwatch.GrantExpiryWarning{ID: "grant-warning-1", ProviderID: "codex", Account: acct, CreditID: "credit-1", CreditTitle: "Reset", ThresholdDays: 3, ExpiresAt: now.Add(72 * time.Hour), SnapshotJSON: []byte(`{"grantWarning":true}`), DetectedAt: now}
	alert := radar.ProbabilityAlert{ID: "radar-1", Milestone: 50, Probability24H: .6, Probability48H: .8, Level: "high", ExpectedWindow: "24h", ReasoningSummary: "test", CheckedAt: formatTime(now), DetectedAt: now, SnapshotJSON: []byte(`{"radar":true}`)}

	base := observation("2026-07-12T10:00:00Z", "2026-07-19T10:00:00Z", "2026-07-12T15:00:00Z")
	states := resetwatch.Decide(base, nil, testOptions())
	if _, err := s.ApplyDecision(ctx, base, states, target); err != nil {
		t.Fatal(err)
	}
	changed := observation("2026-07-13T10:00:00Z", "2026-07-20T10:00:00Z", "2026-07-13T15:00:00Z")
	old, _ := s.LoadWindowStates(ctx, "acct")
	decision := resetwatch.Decide(changed, old, testOptions())
	if len(decision.Events) != 1 {
		t.Fatalf("reset candidates=%d", len(decision.Events))
	}
	if _, err := s.ApplyDecision(ctx, changed, decision, target); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertWarningEvents(ctx, []resetwatch.WarningEvent{warning}, target); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertGrantExpiryWarningEvents(ctx, []resetwatch.GrantExpiryWarning{grantWarning}, target); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.InsertRadarAlertEvent(ctx, alert, target); err != nil || !ok {
		t.Fatalf("radar ok=%v err=%v", ok, err)
	}

	seed := base
	seed.ResetGrants = resetwatch.ResetGrants{AvailableCount: ptrInt(1), Credits: []resetwatch.ResetCredit{{ID: "credit-1", Status: "available", Title: "Reset", ResetType: "full", GrantedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour)}}}
	if got, err := s.InsertResetGrantEvents(ctx, seed, resetwatch.ResetGrantEventCandidates(seed), target); err != nil || len(got) != 0 {
		t.Fatalf("seed=%#v err=%v", got, err)
	}
	var baselineRows int
	_ = s.db.QueryRow(`select count(*) from notification_outbox where event_kind='reset_grant'`).Scan(&baselineRows)
	if baselineRows != 0 {
		t.Fatalf("baseline grant rows=%d", baselineRows)
	}
	next := seed
	next.ObservedAt = now.Add(time.Hour)
	next.ResetGrants.AvailableCount = ptrInt(2)
	next.ResetGrants.Credits = append(next.ResetGrants.Credits, resetwatch.ResetCredit{ID: "credit-2", Status: "available", Title: "Reset 2", ResetType: "full", GrantedAt: now.Add(time.Hour), ExpiresAt: now.Add(31 * 24 * time.Hour)})
	grants := resetwatch.ResetGrantEventCandidates(next)
	inserted, err := s.InsertResetGrantEvents(ctx, next, grants, target)
	if err != nil || len(inserted) != 1 {
		t.Fatalf("grant inserted=%#v err=%v", inserted, err)
	}

	wantTypes := map[string]any{"reset": resetwatch.Event{}, "limit_warning": resetwatch.WarningEvent{}, "reset_grant_warning": resetwatch.GrantExpiryWarning{}, "reset_grant": resetwatch.ResetGrantEvent{}, "radar_alert": radar.ProbabilityAlert{}}
	rows, err := s.db.Query(`select id,event_kind,source,coalesce(profile_ref,''),coalesce(account_ref,''),event_id,target,payload_version,payload_json,status,attempts,available_at,coalesce(lease_token,''),lease_expires_at,delivered_at,coalesce(provider_message_id,''),coalesce(last_error,''),dead_lettered_at,created_at,updated_at from notification_outbox where source='scriba-v7' order by event_kind`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]int{}
	for rows.Next() {
		m, e := scanOutbox(rows)
		if e != nil {
			t.Fatal(e)
		}
		decoded, e := DecodeOutboxPayload(m)
		if e != nil {
			t.Fatalf("decode %s: %v", m.EventKind, e)
		}
		if reflect.TypeOf(decoded) != reflect.TypeOf(wantTypes[m.EventKind]) {
			t.Fatalf("%s decoded %T", m.EventKind, decoded)
		}
		if m.EventKind == "radar_alert" {
			if m.ProfileRef != "" || m.AccountRef != "" {
				t.Fatal("radar attribution is not global")
			}
		} else if m.ProfileRef != "primary" || m.AccountRef != "acct" {
			t.Fatalf("%s attribution=%q/%q", m.EventKind, m.ProfileRef, m.AccountRef)
		}
		seen[m.EventKind]++
	}
	for kind := range wantTypes {
		if seen[kind] != 1 {
			t.Errorf("%s rows=%d", kind, seen[kind])
		}
	}

	// Replays neither add nor rewrite outbox rows.
	var before string
	_ = s.db.QueryRow(`select group_concat(id||payload_json,'|') from notification_outbox where source='scriba-v7' order by id`).Scan(&before)
	_, _ = s.ApplyDecision(ctx, changed, decision, target)
	_, _ = s.InsertWarningEvents(ctx, []resetwatch.WarningEvent{warning}, target)
	_, _ = s.InsertGrantExpiryWarningEvents(ctx, []resetwatch.GrantExpiryWarning{grantWarning}, target)
	_, _ = s.InsertRadarAlertEvent(ctx, alert, target)
	_, _ = s.InsertResetGrantEvents(ctx, next, grants, target)
	var after string
	_ = s.db.QueryRow(`select group_concat(id||payload_json,'|') from notification_outbox where source='scriba-v7' order by id`).Scan(&after)
	if before != after {
		t.Fatal("duplicate producer rewrote outbox")
	}
	for _, table := range []string{"notification_deliveries", "limit_warning_deliveries", "reset_grant_warning_deliveries", "reset_grant_deliveries", "radar_alert_deliveries"} {
		var n int
		if err := s.db.QueryRow(`select count(*) from ` + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("legacy %s rows=%d err=%v", table, n, err)
		}
	}
}

func TestLegacyAccountProducersWithoutTargetsBindConfiguredDefault(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"primary", "codex", "Primary", true, true}}); err != nil {
		t.Fatal(err)
	}
	acct := resetwatch.Account{Ref: "acct", Label: "personal"}
	now := parseTime("2026-07-12T12:00:00Z")
	base := observation("2026-07-12T10:00:00Z", "2026-07-19T10:00:00Z", "2026-07-12T15:00:00Z")
	states := resetwatch.Decide(base, nil, testOptions())
	if _, err := s.ApplyDecision(ctx, base, states); err != nil {
		t.Fatal(err)
	}
	changedObs := observation("2026-07-13T10:00:00Z", "2026-07-20T10:00:00Z", "2026-07-13T15:00:00Z")
	old, _ := s.LoadWindowStates(ctx, "acct")
	if _, err := s.ApplyDecision(ctx, changedObs, resetwatch.Decide(changedObs, old, testOptions())); err != nil {
		t.Fatal(err)
	}
	w := resetwatch.WarningEvent{ID: "w", ProviderID: "codex", Account: acct, Label: resetwatch.LabelFiveHour, ThresholdRemaining: 5, UsedPercent: 96, RemainingPercent: 4, ResetAt: now.Add(time.Hour), SnapshotJSON: []byte(`{}`), DetectedAt: now}
	if _, err := s.InsertWarningEvents(ctx, []resetwatch.WarningEvent{w}); err != nil {
		t.Fatal(err)
	}
	gw := resetwatch.GrantExpiryWarning{ID: "gw", ProviderID: "codex", Account: acct, CreditID: "c", CreditTitle: "C", ThresholdDays: 3, ExpiresAt: now.Add(72 * time.Hour), SnapshotJSON: []byte(`{}`), DetectedAt: now}
	if _, err := s.InsertGrantExpiryWarningEvents(ctx, []resetwatch.GrantExpiryWarning{gw}); err != nil {
		t.Fatal(err)
	}
	obs := base
	obs.ResetGrants = resetwatch.ResetGrants{AvailableCount: ptrInt(2), Credits: []resetwatch.ResetCredit{{ID: "c", Status: "available", Title: "C", GrantedAt: now, ExpiresAt: now.Add(24 * time.Hour)}}}
	grants := resetwatch.ResetGrantEventCandidates(obs)
	if _, err := s.InsertResetGrantEvents(ctx, obs, grants); err != nil {
		t.Fatal(err)
	}
	alert := radar.ProbabilityAlert{ID: "radar-no-target", Milestone: 50, Probability24H: .6, Level: "high", DetectedAt: now, SnapshotJSON: []byte(`{}`)}
	if _, err := s.InsertRadarAlertEvent(ctx, alert); err != nil {
		t.Fatal(err)
	}
	var mapping, outbox, resets, warnings, grantWarnings, grantEvents, radarRows int
	if err := s.db.QueryRow(`select (select count(*) from profile_accounts where profile_ref='primary' and account_ref='acct'),(select count(*) from notification_outbox),(select count(*) from reset_events),(select count(*) from limit_warning_events),(select count(*) from reset_grant_warning_events),(select count(*) from reset_grant_events),(select count(*) from radar_alert_events)`).Scan(&mapping, &outbox, &resets, &warnings, &grantWarnings, &grantEvents, &radarRows); err != nil {
		t.Fatal(err)
	}
	if mapping != 1 || outbox != 0 || resets < 1 || warnings != 1 || grantWarnings != 1 || grantEvents < 1 || radarRows != 1 {
		t.Fatalf("mapping=%d outbox=%d rows=%d/%d/%d/%d radar=%d", mapping, outbox, resets, warnings, grantWarnings, grantEvents, radarRows)
	}
}

func TestV7ProducerPayloadFailureRollsBackBusinessRows(t *testing.T) {
	ctx, target := context.Background(), "telegram:rollback"
	acct := resetwatch.Account{Ref: "acct", Label: "x"}
	now := time.Now().UTC()
	tests := []struct {
		name, table, id string
		run             func(*Store) error
	}{
		{"warning", "limit_warning_events", "bad-warning", func(s *Store) error {
			_, e := s.InsertWarningEvents(ctx, []resetwatch.WarningEvent{{ID: "bad-warning", Account: acct, SnapshotJSON: []byte(`{`), DetectedAt: now}}, target)
			return e
		}},
		{"grant-warning", "reset_grant_warning_events", "bad-grant-warning", func(s *Store) error {
			_, e := s.InsertGrantExpiryWarningEvents(ctx, []resetwatch.GrantExpiryWarning{{ID: "bad-grant-warning", Account: acct, SnapshotJSON: []byte(`{`), DetectedAt: now}}, target)
			return e
		}},
		{"radar", "radar_alert_events", "bad-radar", func(s *Store) error {
			_, e := s.InsertRadarAlertEvent(ctx, radar.ProbabilityAlert{ID: "bad-radar", SnapshotJSON: []byte(`{`), DetectedAt: now}, target)
			return e
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			if err := tt.run(s); err == nil {
				t.Fatal("expected payload error")
			}
			var n int
			if err := s.db.QueryRow(`select count(*) from `+tt.table+` where id=?`, tt.id).Scan(&n); err != nil || n != 0 {
				t.Fatalf("business rows=%d err=%v", n, err)
			}
		})
	}
}

func TestClaimOutboxForTargetIsolation(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()
	for _, target := range []string{"telegram:a", "telegram:b"} {
		tx, _ := s.db.BeginTx(context.Background(), nil)
		if err := EnqueueOutbox(context.Background(), tx, OutboxEnqueue{EventKind: "radar_alert", Source: "test", EventID: target, Target: target, PayloadVersion: 1, PayloadJSON: `{"version":1,"kind":"reset"}`}, now); err != nil {
			t.Fatal(err)
		}
		_ = tx.Commit()
	}
	rows, err := s.ClaimOutboxForTarget(context.Background(), "telegram:a", now, time.Minute, 10)
	if err != nil || len(rows) != 1 || rows[0].Target != "telegram:a" {
		t.Fatalf("claims=%#v err=%v", rows, err)
	}
	var status string
	_ = s.db.QueryRow(`select status from notification_outbox where target='telegram:b'`).Scan(&status)
	if status != "pending" {
		t.Fatalf("other target status=%s", status)
	}
}

func TestRadarProducerAtomicallyFansOutToEveryTarget(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	alert := radar.ProbabilityAlert{ID: "radar-fanout", Milestone: 50, Probability24H: 0.6, Level: "high", DetectedAt: now, SnapshotJSON: []byte(`{}`)}
	targets := []string{"telegram:42", "webhook:deploy", "ntfy:phone"}
	inserted, err := s.InsertRadarAlertEvent(ctx, alert, targets...)
	if err != nil || !inserted {
		t.Fatalf("inserted=%t err=%v", inserted, err)
	}
	rows, err := s.ListOutbox(ctx, OutboxFilter{Limit: 10})
	if err != nil || len(rows) != len(targets) {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	want := make(map[string]bool, len(targets))
	for _, target := range targets {
		want[target] = true
	}
	for i, row := range rows {
		if !want[row.Target] || row.EventID != alert.ID || row.PayloadJSON != rows[0].PayloadJSON {
			t.Fatalf("row %d=%+v", i, row)
		}
		delete(want, row.Target)
	}
	if len(want) != 0 {
		t.Fatalf("missing targets=%v", want)
	}
}

func TestDuplicateFanoutTargetRejectsWholeProducerTransaction(t *testing.T) {
	s := openTestStore(t)
	alert := radar.ProbabilityAlert{ID: "radar-duplicate", Milestone: 25, Probability24H: 0.3, Level: "medium", DetectedAt: time.Now().UTC(), SnapshotJSON: []byte(`{}`)}
	if _, err := s.InsertRadarAlertEvent(context.Background(), alert, "webhook:a", "webhook:a"); err == nil {
		t.Fatal("expected duplicate target error")
	}
	var events, deliveries int
	if err := s.db.QueryRow(`select (select count(*) from radar_alert_events where id=?),(select count(*) from notification_outbox where event_id=?)`, alert.ID, alert.ID).Scan(&events, &deliveries); err != nil {
		t.Fatal(err)
	}
	if events != 0 || deliveries != 0 {
		t.Fatalf("events=%d deliveries=%d", events, deliveries)
	}
}
