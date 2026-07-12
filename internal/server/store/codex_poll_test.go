package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/policy"
	"github.com/agensfield/scriba/internal/resetwatch"
)

func TestApplyCodexPollBootstrapThenEmitOnce(t *testing.T) {
	s := openPollStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	first := codexPollObservation(base, 70)
	events, err := s.ApplyCodexPoll(ctx, pollInput(first, "telegram:42"))
	if err != nil || len(events.PolicyEvents) != 0 {
		t.Fatalf("bootstrap events=%v err=%v", events, err)
	}
	if !events.PolicyBootstrap || !events.AccountBaseline {
		t.Fatalf("fresh account bootstrap flags=%#v", events)
	}
	assertPollCounts(t, s, 1, 0, 0)
	var missingEvaluations int
	if err = s.db.QueryRow(`select count(*) from policy_states where evaluation_json like '%no_match%'`).Scan(&missingEvaluations); err != nil || missingEvaluations == 0 {
		t.Fatalf("missing evaluations=%d err=%v", missingEvaluations, err)
	}

	second := codexPollObservation(base.Add(15*time.Minute), 81)
	events, err = s.ApplyCodexPoll(ctx, pollInput(second, "telegram:42"))
	if err != nil || len(events.PolicyEvents) != 1 || events.PolicyEvents[0].Checkpoint != 20 || len(events.WarningEvents) != 1 {
		t.Fatalf("second events=%v err=%v", events, err)
	}
	assertPollCounts(t, s, 2, 1, 1)
	wantCreated := formatTime(second.ObservedAt.Add(time.Second))
	for _, table := range []string{"limit_warning_events", "policy_events", "notification_outbox"} {
		var created string
		if err := s.db.QueryRow(`select created_at from ` + table).Scan(&created); err != nil || created != wantCreated {
			t.Fatalf("%s created_at=%q want=%q err=%v", table, created, wantCreated, err)
		}
	}

	events, err = s.ApplyCodexPoll(ctx, pollInput(second, "telegram:42"))
	if err != nil || len(events.PolicyEvents) != 0 {
		t.Fatalf("repeat events=%v err=%v", events, err)
	}
	assertPollCounts(t, s, 2, 1, 1)
}

func TestApplyCodexPollTransitionFixturesPersistExactPolicyAndOutbox(t *testing.T) {
	s := openPollStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	periodFive := int64((5 * time.Hour) / time.Millisecond)
	periodWeek := int64((7 * 24 * time.Hour) / time.Millisecond)
	oldFiveReset := base.Add(5 * time.Hour)
	oldWeekReset := base.Add(24 * time.Hour)
	usedFive, usedWeek := 70.0, 34.0
	count := 1
	firstGrant := resetwatch.ResetCredit{ID: "grant-old", Status: "available", Title: "Old", GrantedAt: base.Add(-time.Hour), ExpiresAt: base.Add(10 * 24 * time.Hour)}
	bootstrap := resetwatch.Observation{
		ProviderID: resetwatch.ProviderCodex,
		Account:    resetwatch.Account{Ref: "acct", Label: "Fixture"},
		ObservedAt: base,
		Windows: []resetwatch.Window{
			{Label: resetwatch.LabelFiveHour, UsedPercent: &usedFive, ResetAt: oldFiveReset, PeriodDurationMs: &periodFive},
			{Label: resetwatch.LabelWeeklyLimit, UsedPercent: &usedWeek, ResetAt: oldWeekReset, PeriodDurationMs: &periodWeek},
		},
		ResetGrants:  resetwatch.ResetGrants{AvailableCount: &count, Credits: []resetwatch.ResetCredit{firstGrant}},
		SnapshotJSON: []byte(`{"fixture":"bootstrap"}`),
	}
	if got, err := s.ApplyCodexPoll(ctx, pollInput(bootstrap, "telegram:42")); err != nil || len(got.PolicyEvents) != 0 {
		t.Fatalf("bootstrap=%+v err=%v", got, err)
	}

	at := base.Add(time.Hour)
	usedFive, usedWeek, count = 81, 40, 2
	nextWeekReset := base.Add(7 * 24 * time.Hour)
	newGrant := resetwatch.ResetCredit{ID: "grant-new", Status: "available", Title: "New", GrantedAt: at, ExpiresAt: at.Add(2 * 24 * time.Hour)}
	transition := resetwatch.Observation{
		ProviderID: resetwatch.ProviderCodex,
		Account:    bootstrap.Account,
		ObservedAt: at,
		Windows: []resetwatch.Window{
			{Label: resetwatch.LabelFiveHour, UsedPercent: &usedFive, ResetAt: oldFiveReset, PeriodDurationMs: &periodFive},
			{Label: resetwatch.LabelWeeklyLimit, UsedPercent: &usedWeek, ResetAt: nextWeekReset, PeriodDurationMs: &periodWeek},
		},
		ResetGrants:  resetwatch.ResetGrants{AvailableCount: &count, Credits: []resetwatch.ResetCredit{firstGrant, newGrant}},
		SnapshotJSON: []byte(`{"fixture":"transition"}`),
	}
	got, err := s.ApplyCodexPoll(ctx, pollInput(transition, "telegram:42"))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		rule, subject string
		kind          policy.EventKind
		checkpoint    int
	}{
		{"current.remaining.primary", "primary.five_hour", policy.EventRemainingCheckpoint, 20},
		{"current.reset.weekly", "primary.weekly", policy.EventResetTransition, 0},
		{"current.grant.available", "grant-new", policy.EventGrantAvailable, 0},
		{"current.grant.expiry", "grant-new", policy.EventGrantExpiryCheckpoint, 5},
		{"current.grant.expiry", "grant-new", policy.EventGrantExpiryCheckpoint, 3},
	}
	if len(got.PolicyEvents) != len(want) {
		t.Fatalf("policy events=%+v", got.PolicyEvents)
	}
	for i, event := range got.PolicyEvents {
		if event.RuleID != want[i].rule || event.Subject != want[i].subject || event.Kind != want[i].kind || event.Checkpoint != want[i].checkpoint || event.DetectedAt != at {
			t.Fatalf("event %d=%+v want=%+v", i, event, want[i])
		}
	}
	if len(got.WarningEvents) != 1 || len(got.ResetEvents) != 1 || len(got.ResetGrantEvents) != 1 || len(got.GrantExpiryWarningEvents) != 2 {
		t.Fatalf("typed legacy parity=%+v", got)
	}

	rows, err := s.db.Query(`select p.semantic_event_id,p.rule_id,p.subject_key,p.event_kind,p.payload_version,p.payload_json,o.id,o.target,o.payload_version,o.payload_json
from policy_events p join notification_outbox o on o.event_kind=p.event_kind and o.event_id=p.semantic_event_id
order by p.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	byID := make(map[string]policy.Event, len(got.PolicyEvents))
	for _, event := range got.PolicyEvents {
		byID[event.ID] = event
	}
	rowCount := 0
	for rows.Next() {
		var eventID, rule, subject, kind, policyPayload, outboxID, target, outboxPayload string
		var policyVersion, outboxVersion int
		if err = rows.Scan(&eventID, &rule, &subject, &kind, &policyVersion, &policyPayload, &outboxID, &target, &outboxVersion, &outboxPayload); err != nil {
			t.Fatal(err)
		}
		event, ok := byID[eventID]
		if !ok {
			t.Fatalf("unexpected persisted event %s", eventID)
		}
		persistedKind := map[policy.EventKind]string{
			policy.EventRemainingCheckpoint:   "limit_warning",
			policy.EventResetTransition:       "reset",
			policy.EventGrantAvailable:        "reset_grant",
			policy.EventGrantExpiryCheckpoint: "reset_grant_warning",
		}[event.Kind]
		if eventID != event.ID || rule != event.RuleID || subject != event.Subject || kind != persistedKind || policyVersion != 1 || outboxID != OutboxID(kind, eventID, "telegram:42") || target != "telegram:42" || outboxVersion != 1 || policyPayload != outboxPayload {
			t.Fatalf("persisted event mismatch: id=%q rule=%q subject=%q kind=%q policyVersion=%d outboxID=%q target=%q outboxVersion=%d payloadEqual=%t", eventID, rule, subject, kind, policyVersion, outboxID, target, outboxVersion, policyPayload == outboxPayload)
		}
		delete(byID, eventID)
		rowCount++
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if rowCount != len(want) || len(byID) != 0 {
		t.Fatalf("persisted rows=%d remaining=%v want=%d", rowCount, byID, len(want))
	}
}

func TestApplyCodexPollTreatsLegacyHistoryAsBootstrap(t *testing.T) {
	s := openPollStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	obs := codexPollObservation(base, 95)
	legacy := resetwatch.Decide(obs, map[string]resetwatch.WindowState{}, resetwatch.DefaultOptions())
	if _, err := s.ApplyDecision(ctx, obs, legacy); err != nil {
		t.Fatal(err)
	}

	events, err := s.ApplyCodexPoll(ctx, pollInput(codexPollObservation(base.Add(time.Minute), 96), "telegram:42"))
	if err != nil || len(events.PolicyEvents) != 0 {
		t.Fatalf("migration bootstrap events=%v err=%v", events, err)
	}
	if !events.PolicyBootstrap || events.AccountBaseline {
		t.Fatalf("legacy migration bootstrap flags=%#v", events)
	}
	assertPollCounts(t, s, 2, 0, 0)
}

func TestApplyCodexPollRejectsStaleObservationWithoutWrites(t *testing.T) {
	s := openPollStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	if _, err := s.ApplyCodexPoll(ctx, pollInput(codexPollObservation(base, 70), "")); err != nil {
		t.Fatal(err)
	}
	_, err := s.ApplyCodexPoll(ctx, pollInput(codexPollObservation(base.Add(-time.Second), 81), ""))
	if !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("err=%v", err)
	}
	assertPollCounts(t, s, 1, 0, 0)
}

func TestApplyCodexPollRejectsEqualTimeConflictAndExactReplayDoesNotRewrite(t *testing.T) {
	s := openPollStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	first := codexPollObservation(base, 70)
	if _, err := s.ApplyCodexPoll(ctx, pollInput(first, "telegram:42")); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := s.db.QueryRow(`select updated_at from policy_states order by updated_at desc limit 1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	replay := pollInput(first, "telegram:42")
	replay.CommittedAt = base.Add(time.Hour)
	if got, err := s.ApplyCodexPoll(ctx, replay); err != nil || len(got.PolicyEvents) != 0 {
		t.Fatalf("replay=%v err=%v", got, err)
	}
	var after string
	if err := s.db.QueryRow(`select updated_at from policy_states order by updated_at desc limit 1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("replay rewrote timestamp %q -> %q", before, after)
	}
	conflict := codexPollObservation(base, 71)
	if _, err := s.ApplyCodexPoll(ctx, pollInput(conflict, "telegram:42")); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("conflict err=%v", err)
	}
}

func TestPersistPolicyEventsPayloadParityAllKinds(t *testing.T) {
	s := openPollStore(t)
	ctx := context.Background()
	at := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	committed := at.Add(time.Second)
	obs := codexPollObservation(at, 81)
	obs.SnapshotJSON = []byte(`{"full":"snapshot"}`)
	obs.ResetGrants.AvailableCount = ptrPollInt(3)
	credit := resetwatch.ResetCredit{Title: "fallback title", ResetType: "full", GrantedAt: at.Add(-time.Hour), ExpiresAt: at.Add(48 * time.Hour)}
	fallback := resetwatch.ResetGrantEventCandidates(resetwatch.Observation{ProviderID: "codex", Account: obs.Account, ObservedAt: at, ResetGrants: resetwatch.ResetGrants{AvailableCount: ptrPollInt(3), Credits: []resetwatch.ResetCredit{credit}}, SnapshotJSON: obs.SnapshotJSON})[0]
	// Seed the account outside the transaction under test.
	if _, err := s.db.Exec(`insert into accounts(account_ref,provider_id,label,email,plan,updated_at) values(?,?,?,?,?,?)`, obs.Account.Ref, "codex", obs.Account.Label, obs.Account.Email, obs.Account.Plan, formatTime(committed)); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	legacy := map[string]resetwatch.WindowState{resetwatch.StateKey("acct", resetwatch.LabelWeeklyLimit): {AccountRef: "acct", Label: resetwatch.LabelWeeklyLimit, LastSnapshotJSON: []byte(`{"previous":true}`)}}
	events := []policy.Event{
		{ID: "reset_fixture", RuleID: "current.reset.weekly", Kind: policy.EventResetTransition, Subject: "primary.weekly", LegacyLabel: resetwatch.LabelWeeklyLimit, SecondaryLegacyLabels: []string{resetwatch.LabelReviewWeek, resetwatch.LabelSparkWeekly}, PreviousResetAt: at.Add(-7 * 24 * time.Hour), ResetAt: at.Add(7 * 24 * time.Hour), ResetKind: "scheduled", DetectedAt: at},
		{ID: "warning_fixture", RuleID: "current.remaining.primary", Kind: policy.EventRemainingCheckpoint, Subject: "primary.five_hour", LegacyLabel: resetwatch.LabelFiveHour, Checkpoint: 20, UsedPercent: 81, RemainingPercent: 19, ResetAt: at.Add(5 * time.Hour), DetectedAt: at},
		{ID: fallback.ID, RuleID: "current.grant.available", Kind: policy.EventGrantAvailable, Subject: fallback.CreditID, Grant: policy.GrantObservation{ID: fallback.CreditID, Title: credit.Title, ResetType: credit.ResetType, GrantedAt: credit.GrantedAt, ExpiresAt: credit.ExpiresAt}, AvailableCount: 3, DetectedAt: at},
		{ID: "grant_warning_fixture", RuleID: "current.grant.expiry", Kind: policy.EventGrantExpiryCheckpoint, Subject: fallback.CreditID, Checkpoint: 3, Grant: policy.GrantObservation{ID: fallback.CreditID, Title: credit.Title, ExpiresAt: credit.ExpiresAt}, DetectedAt: at},
	}
	chooser := resetwatch.JokeChooserFunc(func(resetwatch.Event) string { return "configured-joke" })
	got, err := persistPolicyEvents(ctx, tx, obs, legacy, events, "current-v1", "fixture-hash", "telegram:42", chooser, committed)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.PolicyEvents) != 4 || len(got.ResetEvents) != 1 || len(got.WarningEvents) != 1 || len(got.ResetGrantEvents) != 1 || len(got.GrantExpiryWarningEvents) != 1 {
		t.Fatalf("result=%+v", got)
	}
	if got.ResetEvents[0].JokeID != "configured-joke" || string(got.ResetEvents[0].PreviousSnapshotJSON) != `{"previous":true}` || len(got.ResetEvents[0].SecondaryTriggerLabels) != 2 {
		t.Fatalf("reset parity=%+v", got.ResetEvents[0])
	}
	if got.ResetGrantEvents[0].CreditID != fallback.CreditID || got.ResetGrantEvents[0].AvailableCount != 3 {
		t.Fatalf("grant parity=%+v", got.ResetGrantEvents[0])
	}
	rows, err := tx.QueryContext(ctx, `select p.event_kind,p.payload_json,o.payload_json from policy_events p join notification_outbox o on o.event_kind=p.event_kind and o.event_id=p.semantic_event_id order by p.event_kind`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		var kind, payload, outbox string
		if err = rows.Scan(&kind, &payload, &outbox); err != nil {
			t.Fatal(err)
		}
		if payload != outbox {
			t.Fatalf("%s payload mismatch\npolicy=%s\noutbox=%s", kind, payload, outbox)
		}
		n++
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("joined payloads=%d", n)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	loadedReset, ok, err := s.LoadResetEvent(ctx, got.ResetEvents[0].ID)
	if err != nil || !ok || !reflect.DeepEqual(loadedReset, got.ResetEvents[0]) {
		t.Fatalf("reset typed parity ok=%t err=%v\nloaded=%+v\nwant=%+v", ok, err, loadedReset, got.ResetEvents[0])
	}
	loadedWarning, ok, err := s.LoadWarningEvent(ctx, got.WarningEvents[0].ID)
	if err != nil || !ok || !reflect.DeepEqual(loadedWarning, got.WarningEvents[0]) {
		t.Fatalf("warning typed parity ok=%t err=%v\nloaded=%+v\nwant=%+v", ok, err, loadedWarning, got.WarningEvents[0])
	}
	loadedGrant, ok, err := s.LoadResetGrantEvent(ctx, got.ResetGrantEvents[0].ID)
	if err != nil || !ok || !reflect.DeepEqual(loadedGrant, got.ResetGrantEvents[0]) {
		t.Fatalf("grant typed parity ok=%t err=%v\nloaded=%+v\nwant=%+v", ok, err, loadedGrant, got.ResetGrantEvents[0])
	}
	loadedExpiry, ok, err := s.LoadGrantExpiryWarningEvent(ctx, got.GrantExpiryWarningEvents[0].ID)
	if err != nil || !ok || !reflect.DeepEqual(loadedExpiry, got.GrantExpiryWarningEvents[0]) {
		t.Fatalf("expiry typed parity ok=%t err=%v\nloaded=%+v\nwant=%+v", ok, err, loadedExpiry, got.GrantExpiryWarningEvents[0])
	}
	if _, err = s.db.Exec(`delete from policy_events; delete from notification_outbox`); err != nil {
		t.Fatal(err)
	}
	repair, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	repaired, err := persistPolicyEvents(ctx, repair, obs, legacy, events, "current-v1", "fixture-hash", "telegram:42", chooser, committed.Add(time.Minute))
	if err != nil {
		_ = repair.Rollback()
		t.Fatal(err)
	}
	if len(repaired.PolicyEvents) != 0 {
		_ = repair.Rollback()
		t.Fatalf("repair reported new events: %+v", repaired)
	}
	if err = repair.Commit(); err != nil {
		t.Fatal(err)
	}
	var policyCount, outboxCount int
	if err = s.db.QueryRow(`select count(*) from policy_events`).Scan(&policyCount); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`select count(*) from notification_outbox`).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if policyCount != 4 || outboxCount != 4 {
		t.Fatalf("repair policy=%d outbox=%d", policyCount, outboxCount)
	}
}

func TestTypedEventSemanticConflictRollsBack(t *testing.T) {
	s := openPollStore(t)
	ctx := context.Background()
	at := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	obs := codexPollObservation(at, 81)
	if _, err := s.db.Exec(`insert into accounts(account_ref,provider_id,label,email,plan,updated_at) values(?,?,?,?,?,?)`, obs.Account.Ref, "codex", obs.Account.Label, obs.Account.Email, obs.Account.Plan, formatTime(at)); err != nil {
		t.Fatal(err)
	}
	v := resetwatch.WarningEvent{ID: "same-id", ProviderID: "codex", Account: obs.Account, Label: resetwatch.LabelFiveHour, ThresholdRemaining: 20, UsedPercent: 81, RemainingPercent: 19, ResetAt: at.Add(5 * time.Hour), SnapshotJSON: obs.SnapshotJSON, DetectedAt: at}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = insertWarningEventTx(ctx, tx, v, "telegram:42", at); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`update limit_warning_events set used_percent=82 where id='same-id'`); err != nil {
		t.Fatal(err)
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = insertWarningEventTx(ctx, tx, v, "telegram:42", at.Add(time.Minute)); err == nil {
		t.Fatal("expected immutable typed-event conflict")
	}
}

func TestApplyCodexPollPartialResetOptionsPreserveDurations(t *testing.T) {
	s := openPollStore(t)
	ctx := context.Background()
	at := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	used := 50.0
	period := int64((7 * 24 * time.Hour) / time.Millisecond)
	makeObs := func(observed, reset time.Time) resetwatch.Observation {
		return resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: "acct", Label: "Test"}, ObservedAt: observed, SnapshotJSON: []byte(fmt.Sprintf(`{"at":%q}`, observed.Format(time.RFC3339))), Windows: []resetwatch.Window{{Label: resetwatch.LabelWeeklyLimit, UsedPercent: &used, ResetAt: reset, PeriodDurationMs: &period}}}
	}
	opts := resetwatch.Options{ClockJitter: 17 * time.Minute, DueJitter: 19 * time.Minute}
	first := CodexPollInput{Observation: makeObs(at, at.Add(7*24*time.Hour)), NotificationTarget: "telegram:42", ResetOptions: opts, CommittedAt: at.Add(time.Second)}
	if _, err := s.ApplyCodexPoll(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := CodexPollInput{Observation: makeObs(at.Add(time.Hour), at.Add(14*24*time.Hour)), NotificationTarget: "telegram:42", ResetOptions: opts, CommittedAt: at.Add(time.Hour + time.Second)}
	got, err := s.ApplyCodexPoll(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ResetEvents) != 1 || got.ResetEvents[0].JokeID != "tibo-ceiling" {
		t.Fatalf("partial options result=%+v", got)
	}
}

func TestApplyCodexPollFaultRollsBackEverything(t *testing.T) {
	s := openPollStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	if _, err := s.ApplyCodexPoll(ctx, pollInput(codexPollObservation(base, 70), "telegram:42")); err != nil {
		t.Fatal(err)
	}
	s.applyCodexPollFault = func(stage string) error { return fmt.Errorf("injected at %s", stage) }
	_, err := s.ApplyCodexPoll(ctx, pollInput(codexPollObservation(base.Add(time.Minute), 81), "telegram:42"))
	if err == nil {
		t.Fatal("expected fault")
	}
	s.applyCodexPollFault = nil
	assertPollCounts(t, s, 1, 0, 0)
	var used float64
	if err = s.db.QueryRow(`select used_percent from observed_windows`).Scan(&used); err != nil || used != 70 {
		t.Fatalf("used=%v err=%v", used, err)
	}
}

func TestApplyCodexPollSemanticConflictRollsBackTypedEvent(t *testing.T) {
	s := openPollStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	first := codexPollObservation(base, 70)
	if _, err := s.ApplyCodexPoll(ctx, pollInput(first, "telegram:42")); err != nil {
		t.Fatal(err)
	}
	second := codexPollObservation(base.Add(time.Minute), 81)
	eventID := resetwatch.WarningEventID("codex", "acct", resetwatch.LabelFiveHour, second.Windows[0].ResetAt, 20)
	now := formatTime(base)
	_, err := s.db.Exec(`insert into policy_events(id,semantic_key,event_kind,semantic_event_id,rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,payload_version,payload_json,detected_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "conflict", "conflict", "limit_warning", eventID, "current.remaining.primary", "primary.five_hour", "remaining_checkpoint", "codex", "acct", "other", "other", 1, `{}`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyCodexPoll(ctx, pollInput(second, "telegram:42")); err == nil {
		t.Fatal("expected semantic conflict")
	}
	var typed, outbox int
	if err = s.db.QueryRow(`select count(*) from limit_warning_events`).Scan(&typed); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`select count(*) from notification_outbox`).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if typed != 0 || outbox != 0 {
		t.Fatalf("partial write typed=%d outbox=%d", typed, outbox)
	}
	var observations int
	if err = s.db.QueryRow(`select count(*) from limit_observations`).Scan(&observations); err != nil || observations != 1 {
		t.Fatalf("observations=%d err=%v", observations, err)
	}
}

func TestApplyCodexPollTwoHandlesSerializeEmission(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scriba.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()
	base := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	if _, err = a.ApplyCodexPoll(ctx, pollInput(codexPollObservation(base, 70), "telegram:42")); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	counts := make(chan int, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, s := range []*Store{a, b} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			<-start
			events, callErr := s.ApplyCodexPoll(ctx, pollInput(codexPollObservation(base.Add(time.Minute), 81), "telegram:42"))
			counts <- len(events.PolicyEvents)
			errs <- callErr
		}(s)
	}
	close(start)
	wg.Wait()
	close(counts)
	close(errs)
	for callErr := range errs {
		if callErr != nil {
			t.Fatal(callErr)
		}
	}
	total := 0
	for n := range counts {
		total += n
	}
	if total != 1 {
		t.Fatalf("emitted %d events, want 1", total)
	}
	assertPollCounts(t, a, 2, 1, 1)
}

func openPollStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "scriba.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func codexPollObservation(at time.Time, used float64) resetwatch.Observation {
	period := int64((5 * time.Hour) / time.Millisecond)
	return resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: "acct", Label: "Test"}, ObservedAt: at, SnapshotJSON: []byte(`{"source":"test"}`), Windows: []resetwatch.Window{{Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: at.Add(5 * time.Hour), PeriodDurationMs: &period}}}
}

func pollInput(obs resetwatch.Observation, target string) CodexPollInput {
	return CodexPollInput{Observation: obs, NotificationTarget: target, ResetOptions: resetwatch.DefaultOptions(), CommittedAt: obs.ObservedAt.Add(time.Second)}
}

func ptrPollInt(v int) *int { return &v }

func assertPollCounts(t *testing.T, s *Store, observations, events, outbox int) {
	t.Helper()
	queries := []struct {
		q    string
		want int
	}{{`select count(*) from limit_observations`, observations}, {`select count(*) from policy_events`, events}, {`select count(*) from notification_outbox`, outbox}}
	for _, q := range queries {
		var got int
		if err := s.db.QueryRow(q.q).Scan(&got); err != nil || got != q.want {
			t.Fatalf("%s got=%d want=%d err=%v", q.q, got, q.want, err)
		}
	}
}
