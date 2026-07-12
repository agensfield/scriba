package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func enqueueTestOutbox(t *testing.T, s *Store, id string, now time.Time) {
	t.Helper()
	if err := s.migrateNotificationOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = EnqueueOutbox(context.Background(), tx, OutboxEnqueue{ID: id, EventKind: "reset", Source: "test", AccountRef: "acct", EventID: id, Target: "telegram:1", PayloadVersion: 1, PayloadJSON: `{"ok":true}`}, now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxEnqueueRollback(t *testing.T) {
	s := openTestStore(t)
	if err := s.migrateNotificationOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	tx, _ := s.db.BeginTx(context.Background(), nil)
	if err := EnqueueOutbox(context.Background(), tx, OutboxEnqueue{ID: "x", EventKind: "reset", Source: "test", EventID: "e", Target: "t", PayloadVersion: 1, PayloadJSON: `{}`}, now); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback()
	var n int
	if err := s.db.QueryRow(`select count(*) from notification_outbox`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rollback count=%d err=%v", n, err)
	}
}

func TestOutboxConcurrentClaimHasOneOwner(t *testing.T) {
	s := openTestStore(t)
	now := time.Unix(1700000000, 0).UTC()
	enqueueTestOutbox(t, s, "one", now)
	var wg sync.WaitGroup
	tokens := make(chan string, 20)
	errs := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, e := s.ClaimOutbox(context.Background(), now, time.Minute, 1)
			if e != nil {
				errs <- e
				return
			}
			if len(rows) == 1 {
				tokens <- rows[0].LeaseToken
			}
		}()
	}
	wg.Wait()
	close(tokens)
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	var got []string
	for x := range tokens {
		got = append(got, x)
	}
	if len(got) != 1 {
		t.Fatalf("owners=%d tokens=%v", len(got), got)
	}
}

func TestOutboxStaleLeaseCannotFinish(t *testing.T) {
	s := openTestStore(t)
	now := time.Unix(1700000000, 0).UTC()
	enqueueTestOutbox(t, s, "one", now)
	a, _ := s.ClaimOutbox(context.Background(), now, time.Second, 1)
	b, _ := s.ClaimOutbox(context.Background(), now.Add(time.Second), time.Minute, 1)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("claims %d %d", len(a), len(b))
	}
	ok, err := s.FinishOutboxSuccess(context.Background(), a[0].ID, a[0].LeaseToken, "old", now.Add(2*time.Second))
	if err != nil || ok {
		t.Fatalf("stale finish ok=%v err=%v", ok, err)
	}
	ok, err = s.FinishOutboxSuccess(context.Background(), b[0].ID, b[0].LeaseToken, "new", now.Add(2*time.Second))
	if err != nil || !ok {
		t.Fatalf("owner finish ok=%v err=%v", ok, err)
	}
}

func TestOutboxFailureBackoffAndDeadLetter(t *testing.T) {
	s := openTestStore(t)
	now := time.Unix(1700000000, 0).UTC()
	enqueueTestOutbox(t, s, "one", now)
	for attempt := 1; attempt <= OutboxMaxAttempts; attempt++ {
		rows, err := s.ClaimOutbox(context.Background(), now, time.Minute, 1)
		if err != nil || len(rows) != 1 {
			t.Fatalf("attempt %d claim=%d err=%v", attempt, len(rows), err)
		}
		ok, err := s.FinishOutboxFailure(context.Background(), rows[0], "nope", now)
		if err != nil || !ok {
			t.Fatalf("attempt %d finish=%v err=%v", attempt, ok, err)
		}
		if attempt < OutboxMaxAttempts {
			now = now.Add(OutboxBackoff(attempt))
		}
	}
	var status string
	var attempts int
	if err := s.db.QueryRow(`select status,attempts from notification_outbox where id='one'`).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "dead_letter" || attempts != OutboxMaxAttempts {
		t.Fatalf("status=%s attempts=%d", status, attempts)
	}
}

func TestV6MigrationBackfillsOnlyDeliveryRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v6.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`drop table if exists notification_outbox; delete from schema_migrations where version>=7; insert or ignore into schema_migrations(version,applied_at) values(6,'2020-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`insert into radar_alert_events(id,milestone,probability_24h,probability_48h,level,expected_window,reasoning_summary,checked_at,detected_at,snapshot_json,created_at) values
('eligible',1,0,0,'low','','','2020-01-01T00:00:00Z','2020-01-01T00:00:00Z','{}','2020-01-01T00:00:00Z'),
('silent',2,0,0,'low','','','2020-01-01T00:00:00Z','2020-01-01T00:00:00Z','{}','2020-01-01T00:00:00Z');
insert into radar_alert_deliveries(id,alert_id,target,status,attempts,created_at,updated_at) values('terminal','eligible','t','failed',8,'2020-01-01T00:00:00Z','2020-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	db, err := sql.Open("sqlite", sqliteDSN(path, ""))
	if err != nil {
		t.Fatal(err)
	}
	s = &Store{db: db, path: path}
	if err = s.migrateNotificationOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var version int
	if err = s.db.QueryRow(`select max(version) from schema_migrations`).Scan(&version); err != nil || version != 7 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	var check string
	if err = s.db.QueryRow(`pragma quick_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("quick_check=%s err=%v", check, err)
	}
	var fk int
	if err = s.db.QueryRow(`select count(*) from pragma_foreign_key_check`).Scan(&fk); err != nil || fk != 0 {
		t.Fatalf("fk=%d err=%v", fk, err)
	}
	var count int
	var status string
	if err = s.db.QueryRow(`select count(*),min(status) from notification_outbox`).Scan(&count, &status); err != nil || count != 1 || status != "dead_letter" {
		t.Fatalf("backfill count=%d status=%s err=%v", count, status, err)
	}
}

func TestV6MigrationRollsBackSchemaAndVersionOnBadPayload(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v6-bad.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`drop table if exists notification_outbox; delete from schema_migrations where version>=7;
insert or ignore into schema_migrations(version,applied_at) values(6,'2020-01-01T00:00:00Z');
insert into radar_alert_events(id,milestone,probability_24h,probability_48h,level,expected_window,reasoning_summary,checked_at,detected_at,snapshot_json,created_at)
values('e',1,0,0,'low','','','2020-01-01T00:00:00Z','2020-01-01T00:00:00Z','not-json','2020-01-01T00:00:00Z');
insert into radar_alert_deliveries(id,alert_id,target,status,attempts,created_at,updated_at) values('d','e','t','pending',0,'2020-01-01T00:00:00Z','2020-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	db, err := sql.Open("sqlite", sqliteDSN(path, ""))
	if err != nil {
		t.Fatal(err)
	}
	s = &Store{db: db, path: path}
	defer func() { _ = s.Close() }()
	if err = s.migrateNotificationOutbox(ctx); err == nil {
		t.Fatal("expected invalid payload migration failure")
	}
	var version int
	if err = s.db.QueryRow(`select max(version) from schema_migrations`).Scan(&version); err != nil || version != 6 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	var exists int
	if err = s.db.QueryRow(`select count(*) from sqlite_master where type='table' and name='notification_outbox'`).Scan(&exists); err != nil || exists != 0 {
		t.Fatalf("outbox exists=%d err=%v", exists, err)
	}
}

func TestOutboxClaimValidation(t *testing.T) {
	s := openTestStore(t)
	if err := s.migrateNotificationOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(0, 0)
	if _, err := s.ClaimOutbox(context.Background(), now, 0, 1); err == nil {
		t.Fatal("expected lease error")
	}
	if _, err := s.ClaimOutbox(context.Background(), now, time.Second, 1001); err == nil {
		t.Fatal("expected limit error")
	}
}

func TestOutboxActivatedByNormalMigration(t *testing.T) {
	s := openTestStore(t)
	var v int
	if err := s.db.QueryRow(`select max(version) from schema_migrations`).Scan(&v); err != nil || v != SchemaVersion {
		t.Fatalf("version=%d err=%v", v, err)
	}
	var n int
	if err := s.db.QueryRow(`select count(*) from sqlite_master where name='notification_outbox'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("outbox active=%d err=%v", n, err)
	}
}

func TestOutboxConflictingSemanticDuplicateFails(t *testing.T) {
	s := openTestStore(t)
	now := time.Unix(1700000000, 0).UTC()
	enqueueTestOutbox(t, s, "same", now)
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	err = EnqueueOutbox(context.Background(), tx, OutboxEnqueue{ID: "different", EventKind: "reset", Source: "other", EventID: "same", Target: "telegram:1", PayloadVersion: 1, PayloadJSON: `{"different":true}`}, now)
	if err == nil {
		t.Fatal("expected semantic conflict")
	}
}

func TestOutboxExpiredFinalAttemptSelfDeadLettersOnClaim(t *testing.T) {
	s := openTestStore(t)
	now := time.Unix(1700000000, 0).UTC()
	enqueueTestOutbox(t, s, "crash", now)
	_, err := s.db.Exec(`update notification_outbox set status='leased',attempts=?,lease_token='old',lease_expires_at=? where id='crash'`, OutboxMaxAttempts, formatTime(now.Add(-time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.ClaimOutbox(context.Background(), now, time.Minute, 1)
	if err != nil || len(rows) != 0 {
		t.Fatalf("claim=%d err=%v", len(rows), err)
	}
	var status string
	if err = s.db.QueryRow(`select status from notification_outbox where id='crash'`).Scan(&status); err != nil || status != "dead_letter" {
		t.Fatalf("status=%s err=%v", status, err)
	}
}

func TestOutboxMigrationConcurrentAndIdempotent(t *testing.T) {
	s := openTestStore(t)
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.migrateNotificationOutbox(context.Background()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.migrateNotificationOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	var versions int
	if err := s.db.QueryRow(`select count(*) from schema_migrations where version=?`, OutboxSchemaVersion).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("v7 rows=%d err=%v", versions, err)
	}
}
