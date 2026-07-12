package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
)

func TestPolicyMigrationFromV7IsAdditiveEmptyAndIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v7.sqlite")
	db, err := sql.Open("sqlite", sqliteDSN(path, ""))
	if err != nil {
		t.Fatal(err)
	}
	v7 := &Store{db: db, path: path}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		t.Fatal(err)
	}
	if err := v7.migrateNotificationDeliveries(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
insert into schema_migrations(version,applied_at) values(6,'2026-07-12T00:00:00Z');
insert into accounts(account_ref,provider_id,label,email,plan,updated_at) values('acct','codex','Test','','pro','2026-07-12T00:00:00Z');
insert into reset_events(id,provider_id,account_ref,account_label,account_email,account_plan,primary_trigger_label,secondary_trigger_labels_json,reset_kind,previous_reset_at,current_reset_at,previous_snapshot_json,current_snapshot_json,joke_id,detected_at,created_at) values('reset-1','codex','acct','Test','','pro','weekly_limit','[]','weekly','2026-07-05T00:00:00Z','2026-07-12T00:00:00Z','{}','{}','joke','2026-07-12T00:00:00Z','2026-07-12T00:00:00Z');
insert into notification_deliveries(id,event_id,target,status,attempts,delivered_at,provider_message_id,created_at,updated_at) values('delivery-1','reset-1','telegram:1','delivered',1,'2026-07-12T00:00:01Z','message-1','2026-07-12T00:00:00Z','2026-07-12T00:00:01Z');
insert into server_settings(key,value,updated_at) values('policy-migration-proof','preserved','2026-07-12T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	if err := v7.migrateNotificationOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	if err := v7.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open v7 through normal migration: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.migratePolicy(ctx); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	var version, versions, states, events, resets, deliveries, outbox int
	var proof string
	if err := s.db.QueryRowContext(ctx, `select max(version) from schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `select count(*) from schema_migrations where version=8`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `select count(*) from policy_states`).Scan(&states); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `select count(*) from policy_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	for query, destination := range map[string]*int{
		`select count(*) from reset_events`:            &resets,
		`select count(*) from notification_deliveries`: &deliveries,
		`select count(*) from notification_outbox`:     &outbox,
	} {
		if err := s.db.QueryRowContext(ctx, query).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.QueryRowContext(ctx, `select value from server_settings where key='policy-migration-proof'`).Scan(&proof); err != nil {
		t.Fatal(err)
	}
	if version != PolicySchemaVersion || versions != 1 || states != 0 || events != 0 || resets != 1 || deliveries != 1 || outbox != 1 || proof != "preserved" {
		t.Fatalf("version=%d versions=%d states=%d events=%d resets=%d deliveries=%d outbox=%d proof=%q", version, versions, states, events, resets, deliveries, outbox, proof)
	}
	assertSQLiteIntegrity(t, s)
}

func TestPolicyMigrationConcurrentCallsRecordOneVersion(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `delete from schema_migrations where version=8; drop table policy_events; drop table policy_states;`); err != nil {
		t.Fatal(err)
	}
	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.migratePolicy(context.Background())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var versions int
	if err := s.db.QueryRowContext(ctx, `select count(*) from schema_migrations where version=8`).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("versions=%d err=%v", versions, err)
	}
	assertSQLiteIntegrity(t, s)
}

func TestPolicyMigrationRejectsMalformedStampedV8(t *testing.T) {
	for _, mutation := range []string{
		`drop table policy_events`,
		`drop index idx_policy_events_correlation`,
	} {
		t.Run(mutation, func(t *testing.T) {
			s := openTestStore(t)
			if _, err := s.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if err := s.migratePolicy(context.Background()); err == nil {
				t.Fatal("malformed stamped v8 schema was accepted")
			}
			if err := s.migratePolicy(context.Background()); err == nil {
				t.Fatal("malformed stamped v8 schema was accepted on retry")
			}
		})
	}
}

func TestPolicySchemaConstraintsAndSemanticDedupe(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `insert into accounts(account_ref,provider_id,label,email,plan,updated_at) values('acct','codex','Test','','pro','2026-07-12T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	stateSQL := `insert into policy_states(rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,state_json,evaluation_json,observed_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`
	validState := []any{"weekly-remaining", "weekly_limit", "remaining_checkpoint", "codex", "acct", "current-v1", "sha256:fixture", `{}`, `{"result":"bootstrap"}`, "2026-07-12T00:00:00Z", "2026-07-12T00:00:00Z", "2026-07-12T00:00:00Z"}
	if _, err := s.db.ExecContext(ctx, stateSQL, validState...); err != nil {
		t.Fatal(err)
	}
	mismatchedProviderState := append([]any(nil), validState...)
	mismatchedProviderState[1], mismatchedProviderState[3] = "other", "claude"
	if _, err := s.db.ExecContext(ctx, stateSQL, mismatchedProviderState...); err == nil {
		t.Fatal("state with mismatched account provider was accepted")
	}
	invalidState := append([]any(nil), validState...)
	invalidState[1], invalidState[7] = "other", "not-json"
	if _, err := s.db.ExecContext(ctx, stateSQL, invalidState...); err == nil {
		t.Fatal("invalid state JSON was accepted")
	}
	invalidState = append([]any(nil), validState...)
	invalidState[1], invalidState[2] = "other", "arbitrary_rule"
	if _, err := s.db.ExecContext(ctx, stateSQL, invalidState...); err == nil {
		t.Fatal("unknown rule kind was accepted")
	}
	eventSQL := `insert into policy_events(id,semantic_key,event_kind,semantic_event_id,rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,payload_version,payload_json,detected_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	validEvent := []any{"event-1", "codex:acct:weekly-remaining:weekly_limit:20:reset", "limit_warning", "warning-1", "weekly-remaining", "weekly_limit", "remaining_checkpoint", "codex", "acct", "current-v1", "sha256:fixture", 1, `{"version":1}`, "2026-07-12T00:00:00Z", "2026-07-12T00:00:00Z"}
	if _, err := s.db.ExecContext(ctx, eventSQL, validEvent...); err != nil {
		t.Fatal(err)
	}
	mismatchedProviderEvent := append([]any(nil), validEvent...)
	mismatchedProviderEvent[0], mismatchedProviderEvent[1], mismatchedProviderEvent[3], mismatchedProviderEvent[7] = "event-mismatch", "mismatch", "warning-mismatch", "claude"
	if _, err := s.db.ExecContext(ctx, eventSQL, mismatchedProviderEvent...); err == nil {
		t.Fatal("event with mismatched account provider was accepted")
	}
	duplicate := append([]any(nil), validEvent...)
	duplicate[0] = "event-2"
	if _, err := s.db.ExecContext(ctx, eventSQL, duplicate...); err == nil {
		t.Fatal("duplicate semantic event was accepted")
	}
	invalidEvent := append([]any(nil), validEvent...)
	invalidEvent[0], invalidEvent[1], invalidEvent[3], invalidEvent[12] = "event-3", "unique", "warning-3", "not-json"
	if _, err := s.db.ExecContext(ctx, eventSQL, invalidEvent...); err == nil {
		t.Fatal("invalid event payload was accepted")
	}
	missingAccount := append([]any(nil), validEvent...)
	missingAccount[0], missingAccount[1], missingAccount[3], missingAccount[8] = "event-4", "unique-2", "warning-4", "missing"
	if _, err := s.db.ExecContext(ctx, eventSQL, missingAccount...); err == nil {
		t.Fatal("missing account reference was accepted")
	}
	duplicateCorrelation := append([]any(nil), validEvent...)
	duplicateCorrelation[0], duplicateCorrelation[1] = "event-5", "unique-3"
	if _, err := s.db.ExecContext(ctx, eventSQL, duplicateCorrelation...); err == nil {
		t.Fatal("duplicate typed event correlation was accepted")
	}
	emptyCorrelation := append([]any(nil), validEvent...)
	emptyCorrelation[0], emptyCorrelation[1], emptyCorrelation[3] = "event-6", "unique-4", ""
	if _, err := s.db.ExecContext(ctx, eventSQL, emptyCorrelation...); err == nil {
		t.Fatal("empty typed event correlation was accepted")
	}
	assertSQLiteIntegrity(t, s)
}

func assertSQLiteIntegrity(t *testing.T, s *Store) {
	t.Helper()
	var quick string
	if err := s.db.QueryRow(`pragma quick_check`).Scan(&quick); err != nil || quick != "ok" {
		t.Fatalf("quick_check=%q err=%v", quick, err)
	}
	rows, err := s.db.Query(`pragma foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		var table, parent string
		var rowID any
		var fk int
		if err := rows.Scan(&table, &rowID, &parent, &fk); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("foreign key violation: table=%s row=%v parent=%s fk=%d", table, rowID, parent, fk)
	}
}
