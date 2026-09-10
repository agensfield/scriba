package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSchema12To13PreservesAccountsHistoryOutboxAndReplay(t *testing.T) {
	s := makeSchema12Store(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`
insert into accounts(account_ref,provider_id,label,email,plan,updated_at) values
 ('one','codex','One','one@example.com','plus','2026-09-08T00:00:00Z'),
 ('two','codex','Two','two@example.com','pro','2026-09-09T00:00:00Z');
insert into profile_accounts(profile_ref,provider_id,account_ref,is_current,first_seen_at,last_seen_at) values
 ('default','codex','one',0,'2026-09-08T00:00:00Z','2026-09-08T00:00:00Z'),
 ('default','codex','two',1,'2026-09-09T00:00:00Z','2026-09-09T00:00:00Z');
insert into limit_observations(id,provider_id,account_ref,observed_at,snapshot_json,created_at) values
 ('obs-one','codex','one','2026-09-08T00:00:00Z','{}','2026-09-08T00:00:00Z'),
 ('obs-two','codex','two','2026-09-09T00:00:00Z','{}','2026-09-09T00:00:00Z');
insert into policy_events(id,semantic_key,event_kind,semantic_event_id,rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,payload_version,payload_json,detected_at,created_at) values
 ('event-one','semantic-one','limit_warning','warn-one','rule','subject','remaining_checkpoint','codex','one','rev','hash',1,'{}','2026-09-08T00:00:00Z','2026-09-08T00:00:00Z');
insert into notification_outbox(id,event_kind,source,profile_ref,account_ref,event_id,target,payload_version,payload_json,status,attempts,available_at,created_at,updated_at) values
 ('outbox-one','limit_warning','policy-v1','default','one','warn-one','telegram:1',1,'{"stable":true}','pending',0,'2026-09-08T00:00:00Z','2026-09-08T00:00:00Z','2026-09-08T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	var replayBefore int64
	if err := s.db.QueryRow(`select replay_seq from policy_event_replay where policy_event_id='event-one'`).Scan(&replayBefore); err != nil {
		t.Fatal(err)
	}
	s.accountMigrationFault = func(point string) error {
		if point == "after_v13_outbox_copy" {
			return errors.New("test fault")
		}
		return nil
	}
	if err := s.migrateAccounts(ctx); err == nil {
		t.Fatal("faulted migration succeeded")
	}
	var version int
	if err := s.db.QueryRow(`select max(version) from schema_migrations`).Scan(&version); err != nil || version != 12 {
		t.Fatalf("rollback version=%d err=%v", version, err)
	}
	s.accountMigrationFault = nil
	if err := s.migrateAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	accounts, err := s.ListAccounts(ctx)
	if err != nil || len(accounts) != 2 || accounts[0].Ref != "two" || accounts[1].Ref != "one" {
		t.Fatalf("accounts=%+v err=%v", accounts, err)
	}
	for _, account := range accounts {
		if account.Alias != "" || account.CredentialsAvailable || len(account.SourceRefs) != 0 || account.LastSeenAt.IsZero() {
			t.Fatalf("migrated account=%+v", account)
		}
	}
	var outboxID, payload string
	if err := s.db.QueryRow(`select id,payload_json from notification_outbox`).Scan(&outboxID, &payload); err != nil || outboxID != "outbox-one" || payload != `{"stable":true}` {
		t.Fatalf("outbox=%q payload=%q err=%v", outboxID, payload, err)
	}
	var replayAfter int64
	if err := s.db.QueryRow(`select replay_seq from policy_event_replay where policy_event_id='event-one'`).Scan(&replayAfter); err != nil || replayAfter != replayBefore {
		t.Fatalf("replay=%d want=%d err=%v", replayAfter, replayBefore, err)
	}
	var profileTables, fkFailures int
	if err := s.db.QueryRow(`select count(*) from sqlite_master where type='table' and name in ('profiles','profile_accounts','profile_poll_health')`).Scan(&profileTables); err != nil || profileTables != 0 {
		t.Fatalf("legacy tables=%d err=%v", profileTables, err)
	}
	if err := s.db.QueryRow(`select count(*) from pragma_foreign_key_check`).Scan(&fkFailures); err != nil || fkFailures != 0 {
		t.Fatalf("foreign keys=%d err=%v", fkFailures, err)
	}
	var archivedHealth int
	if err := s.db.QueryRow(`select count(*) from legacy_poll_health_evidence where legacy_ref='default'`).Scan(&archivedHealth); err != nil || archivedHealth != 1 {
		t.Fatalf("archived health=%d err=%v", archivedHealth, err)
	}
	path := s.path
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if version, err = reopened.SchemaVersion(ctx); err != nil || version != 13 {
		t.Fatalf("reopened version=%d err=%v", version, err)
	}
}

func makeSchema12Store(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema12.sqlite")
	db, err := sql.Open("sqlite", sqliteDSN(path, ""))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(maxOpenConnections)
	s := &Store{db: db, path: path}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err = db.ExecContext(ctx, schemaSQL); err != nil {
		t.Fatal(err)
	}
	if err = s.migrateNotificationDeliveries(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `insert into schema_migrations(version,applied_at) values(6,?)`, formatTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	for _, migrate := range []func(context.Context) error{s.migrateNotificationOutbox, s.migratePolicy, s.migratePolicyEventReplay, s.migrateProfiles, s.migratePacingAlerts} {
		if err = migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return s
}
