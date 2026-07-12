package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

func legacySnapshot(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.db.Query(`select type,name,coalesce(sql,'') from sqlite_master where name not like 'sqlite_%' and name not in ('profiles','profile_accounts','profile_poll_health','profiles_one_default','profiles_provider_identity','profile_accounts_current','profile_accounts_owner') order by type,name`)
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for rows.Next() {
		var typ, name, sqlText string
		if err := rows.Scan(&typ, &name, &sqlText); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, fmt.Sprintf("object:%s:%s:%s", typ, name, sqlText))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	tables, err := s.db.Query(`select name from sqlite_master where type='table' and name not like 'sqlite_%' and name not in ('profiles','profile_accounts','profile_poll_health','schema_migrations') order by name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := tables.Err(); err != nil {
		t.Fatal(err)
	}
	_ = tables.Close()
	for _, name := range names {
		if name == "notification_outbox" {
			continue
		}
		data, err := s.db.Query(`select * from ` + name)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := data.Columns()
		if err != nil {
			_ = data.Close()
			t.Fatal(err)
		}
		var records []string
		for data.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := data.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			records = append(records, fmt.Sprint(vals))
		}
		if err := data.Err(); err != nil {
			t.Fatal(err)
		}
		_ = data.Close()
		sort.Strings(records)
		parts = append(parts, "data:"+name+":"+strings.Join(records, "|"))
	}
	var migrations, outbox string
	if err := s.db.QueryRow(`select coalesce(group_concat(version||':'||applied_at,'|'),'') from (select version,applied_at from schema_migrations where version<=10 order by version)`).Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`select coalesce(group_concat(id||':'||event_kind||':'||source||':'||coalesce(account_ref,'')||':'||event_id||':'||target||':'||payload_version||':'||payload_json||':'||status||':'||attempts||':'||available_at||':'||coalesce(lease_token,'')||':'||coalesce(lease_expires_at,'')||':'||coalesce(delivered_at,'')||':'||coalesce(provider_message_id,'')||':'||coalesce(last_error,'')||':'||coalesce(dead_lettered_at,'')||':'||created_at||':'||updated_at,'|'),'') from (select * from notification_outbox order by id)`).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	parts = append(parts, "migrations:"+migrations, "outbox_without_profile:"+outbox)
	var quick string
	if err := s.db.QueryRow(`pragma quick_check`).Scan(&quick); err != nil {
		t.Fatal(err)
	}
	var fk int
	if err := s.db.QueryRow(`select count(*) from pragma_foreign_key_check`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if quick != "ok" || fk != 0 {
		t.Fatalf("quick=%q fk=%d", quick, fk)
	}
	parts = append(parts, "quick:"+quick, fmt.Sprintf("fk:%d", fk))
	return strings.Join(parts, "\n")
}

func makeProfileV10(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.Exec(`drop table profile_poll_health; drop table profile_accounts; drop table profiles; delete from schema_migrations where version=11`); err != nil {
		t.Fatal(err)
	}
}

func TestProfileMigrationBackfillsAccountsAndSanitizedHealth(t *testing.T) {
	s := openTestStore(t)
	makeProfileV10(t, s)
	_, err := s.db.Exec(`
insert into accounts values('z','codex','Z','','pro','2026-07-10T00:00:00Z');
insert into accounts values('a','codex','A','','pro','2026-07-11T00:00:00Z');
insert into accounts values('new','codex','New','','pro','2026-07-13T00:00:00Z');
insert into limit_observations values('o1','codex','z','2026-07-14T00:00:00Z','{}','2026-07-14T00:00:00Z');
insert into limit_observations values('o2','codex','a','2026-07-12T00:00:00Z','{}','2026-07-12T00:00:00Z');
insert into server_settings values('poll_attempt_at','not/a/time','2026-07-12T00:00:00Z');
insert into server_settings values('poll_success_at','2026-07-12T01:02:03Z','2026-07-12T00:00:00Z');
insert into server_settings values('poll_failure_count','-9','2026-07-12T00:00:00Z');
insert into server_settings values('health_alert_state','failing','2026-07-12T00:00:00Z');
insert into server_settings values('poll_failure_error','/secret/auth.json bearer nope','2026-07-12T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.migrateProfiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	var current, first, last string
	if err := s.db.QueryRow(`select account_ref,first_seen_at,last_seen_at from profile_accounts where is_current=1`).Scan(&current, &first, &last); err != nil {
		t.Fatal(err)
	}
	if current != "z" || first != "2026-07-14T00:00:00Z" || last != first {
		t.Fatalf("current=%q first=%q last=%q", current, first, last)
	}
	if err := s.db.QueryRow(`select last_seen_at from profile_accounts where account_ref='new'`).Scan(&last); err != nil || last != "2026-07-13T00:00:00Z" {
		t.Fatalf("account activity last=%q err=%v", last, err)
	}
	var attempt any
	var success, kind, code, alert string
	var failures int
	if err := s.db.QueryRow(`select last_attempt_at,last_success_at,consecutive_failures,failure_kind,last_error_code,alert_state from profile_poll_health`).Scan(&attempt, &success, &failures, &kind, &code, &alert); err != nil {
		t.Fatal(err)
	}
	if attempt != nil || success != "2026-07-12T01:02:03Z" || failures != 0 || kind != "" || code != "" || alert != "ok" {
		t.Fatalf("health=%v %q %d %q %q", attempt, success, failures, kind, code)
	}
}

func TestProfileMigrationParsesFractionalActivityExactly(t *testing.T) {
	s := openTestStore(t)
	makeProfileV10(t, s)
	for _, x := range []struct{ ref, raw string }{{"exact", "2026-07-13T00:00:00Z"}, {"hundredth", "2026-07-13T00:00:00.01Z"}, {"tenth", "2026-07-13T00:00:00.1Z"}, {"nano-z", "2026-07-13T00:00:00.100000001Z"}, {"nano-a", "2026-07-13T00:00:00.100000001Z"}} {
		if _, err := s.db.Exec(`insert into accounts values(?,'codex','','','',?)`, x.ref, x.raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.migrateProfiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	var current string
	if err := s.db.QueryRow(`select account_ref from profile_accounts where is_current=1`).Scan(&current); err != nil || current != "nano-a" {
		t.Fatalf("current=%q err=%v", current, err)
	}
}

func TestProfileMigrationEmptyIdempotentAndConcurrent(t *testing.T) {
	s := openTestStore(t)
	makeProfileV10(t, s)
	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.migrateProfiles(context.Background()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.migrateProfiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	var profiles, mappings, versions int
	if err := s.db.QueryRow(`select (select count(*) from profiles),(select count(*) from profile_accounts),(select count(*) from schema_migrations where version=11)`).Scan(&profiles, &mappings, &versions); err != nil {
		t.Fatal(err)
	}
	if profiles != 1 || mappings != 0 || versions != 1 {
		t.Fatalf("profiles=%d mappings=%d versions=%d", profiles, mappings, versions)
	}
}

func TestProfileMigrationRejectsMalformedStampedV11(t *testing.T) {
	for name, mutation := range map[string]string{
		"owner index":            `drop index profile_accounts_owner`,
		"identity index":         `drop index profiles_provider_identity`,
		"current index":          `drop index profile_accounts_current`,
		"profiles table":         `alter table profiles rename to profiles_good; create table profiles(profile_ref text primary key)`,
		"accounts table":         `alter table profile_accounts rename to profile_accounts_good; create table profile_accounts(profile_ref text primary key)`,
		"health table":           `alter table profile_poll_health rename to profile_poll_health_good; create table profile_poll_health(profile_ref text primary key)`,
		"column rename":          `alter table profile_poll_health rename column updated_at to touched_at`,
		"nullability":            `pragma writable_schema=on; update sqlite_master set sql=replace(sql,'label text not null','label text') where type='table' and name='profiles'; pragma writable_schema=off`,
		"quoted enum whitespace": `pragma writable_schema=on; update sqlite_master set sql=replace(sql,'''network''','''net work''') where type='table' and name='profile_poll_health'; pragma writable_schema=off`,
		"quoted enum case":       `pragma writable_schema=on; update sqlite_master set sql=replace(sql,'''network''','''NETWORK''') where type='table' and name='profile_poll_health'; pragma writable_schema=off`,
	} {
		t.Run(name, func(t *testing.T) {
			s := openTestStore(t)
			if _, err := s.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if err := s.migrateProfiles(context.Background()); err == nil {
				t.Fatal("malformed v11 accepted")
			}
		})
	}
}

func TestProfileAccountRejectsContradictoryProviderAndDisabledDefault(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.db.Exec(`insert into accounts values('other','other','O','','','2026-07-12T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`insert into profile_accounts values('default','other','other',0,'2026-07-12T00:00:00Z','2026-07-12T00:00:00Z')`); err == nil {
		t.Fatal("contradictory provider accepted")
	}
	if _, err := s.db.Exec(`update profiles set enabled=0 where profile_ref='default'`); err == nil {
		t.Fatal("disabled default accepted")
	}
	var fk int
	if err := s.db.QueryRow(`select count(*) from pragma_foreign_key_check`).Scan(&fk); err != nil || fk != 0 {
		t.Fatalf("fk=%d err=%v", fk, err)
	}
}

func TestProfileSchemaRejectsNullPrimaryReferences(t *testing.T) {
	s := openTestStore(t)
	for range 2 {
		if _, err := s.db.Exec(`insert into profiles(profile_ref,provider_id,label,enabled,is_default,created_at,updated_at) values(null,'codex','Null',1,0,'2026-07-12T00:00:00Z','2026-07-12T00:00:00Z')`); err == nil {
			t.Fatal("null profile ref accepted")
		}
	}
	if _, err := s.db.Exec(`insert into profile_poll_health(profile_ref,consecutive_failures,failure_kind,last_error_code,alert_state,updated_at) values(null,0,'','','ok','2026-07-12T00:00:00Z')`); err == nil {
		t.Fatal("null health profile ref accepted")
	}
}

func TestProfileMigrationBackfillsOnlyAccountOutboxRowsAndRollsBack(t *testing.T) {
	s := openTestStore(t)
	makeProfileV10(t, s)
	if _, err := s.db.Exec(`insert into accounts values('acct','codex','A','','pro','2026-07-12T00:00:00Z');
insert into notification_outbox(id,event_kind,source,account_ref,event_id,target,payload_version,payload_json,status,attempts,available_at,created_at,updated_at) values
('pending','policy','test','acct','e1','t',1,'{}','pending',2,'2026-07-12T00:00:00Z','2026-07-12T00:00:00Z','2026-07-12T00:00:00Z'),
('global','policy','test',null,'e2','t',1,'{}','pending',3,'2026-07-12T00:00:00Z','2026-07-12T00:00:00Z','2026-07-12T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`insert into notification_outbox(id,event_kind,source,account_ref,event_id,target,payload_version,payload_json,status,attempts,available_at,lease_token,lease_expires_at,delivered_at,dead_lettered_at,created_at,updated_at) values
('leased','policy','test','acct','e3','t',1,'{}','leased',1,'2026-07-12T00:00:00Z','lease','2026-07-12T01:00:00Z',null,null,'2026-07-12T00:00:00Z','2026-07-12T00:00:00Z'),
('delivered','policy','test','acct','e4','t',1,'{}','delivered',1,'2026-07-12T00:00:00Z',null,null,'2026-07-12T01:00:00Z',null,'2026-07-12T00:00:00Z','2026-07-12T00:00:00Z'),
('dead','policy','test','acct','e5','t',1,'{}','dead_letter',1,'2026-07-12T00:00:00Z',null,null,null,'2026-07-12T01:00:00Z','2026-07-12T00:00:00Z','2026-07-12T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	before := legacySnapshot(t, s)
	var eligible int
	if err := s.db.QueryRow(`select count(*) from notification_outbox where account_ref is not null and profile_ref is null`).Scan(&eligible); err != nil || eligible != 4 {
		t.Fatalf("eligible=%d err=%v", eligible, err)
	}
	s.profileMigrationFault = func(stage string) error { return errors.New(stage) }
	if err := s.migrateProfiles(context.Background()); err == nil {
		t.Fatal("forced failure succeeded")
	}
	s.profileMigrationFault = nil
	var attributed, version int
	if err := s.db.QueryRow(`select (select count(*) from notification_outbox where profile_ref is not null),(select max(version)) from schema_migrations`).Scan(&attributed, &version); err != nil {
		t.Fatal(err)
	}
	if attributed != 0 || version != 10 {
		t.Fatalf("rollback attribution=%d version=%d", attributed, version)
	}
	if err := s.migrateProfiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateProfiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	var mapped, global int
	if err := s.db.QueryRow(`select (select count(*) from notification_outbox where account_ref is not null and profile_ref='default'),(select count(*) from notification_outbox where account_ref is null and profile_ref is null)`).Scan(&mapped, &global); err != nil {
		t.Fatal(err)
	}
	after := legacySnapshot(t, s)
	if mapped != eligible || global != 1 || after != before {
		t.Fatalf("mapped=%d eligible=%d global=%d unchanged=%v", mapped, eligible, global, after == before)
	}
}

func TestProfileMigrationRejectsInvalidOutboxOwnershipAndRollsBack(t *testing.T) {
	for name, setup := range map[string]string{
		"orphan":            `insert into notification_outbox(id,event_kind,source,account_ref,event_id,target,payload_version,payload_json,status,attempts,available_at,created_at,updated_at) values('bad','policy','test','missing','e','t',1,'{}','pending',0,'2026-07-12T00:00:00Z','2026-07-12T00:00:00Z','2026-07-12T00:00:00Z')`,
		"unmapped provider": `insert into accounts values('other','other','O','','','2026-07-12T00:00:00Z'); insert into notification_outbox(id,event_kind,source,account_ref,event_id,target,payload_version,payload_json,status,attempts,available_at,created_at,updated_at) values('bad','policy','test','other','e','t',1,'{}','pending',0,'2026-07-12T00:00:00Z','2026-07-12T00:00:00Z','2026-07-12T00:00:00Z')`,
		"bogus attribution": `insert into accounts values('acct','codex','A','','','2026-07-12T00:00:00Z'); insert into notification_outbox(id,event_kind,source,profile_ref,account_ref,event_id,target,payload_version,payload_json,status,attempts,available_at,created_at,updated_at) values('bad','policy','test','bogus','acct','e','t',1,'{}','pending',0,'2026-07-12T00:00:00Z','2026-07-12T00:00:00Z','2026-07-12T00:00:00Z')`,
	} {
		t.Run(name, func(t *testing.T) {
			s := openTestStore(t)
			makeProfileV10(t, s)
			if _, err := s.db.Exec(setup); err != nil {
				t.Fatal(err)
			}
			before := legacySnapshot(t, s)
			if err := s.migrateProfiles(context.Background()); err == nil {
				t.Fatal("invalid ownership migrated")
			}
			var version, newObjects int
			if err := s.db.QueryRow(`select (select max(version)),(select count(*) from sqlite_master where name in ('profile_accounts','profile_poll_health')) from schema_migrations`).Scan(&version, &newObjects); err != nil {
				t.Fatal(err)
			}
			if version != 10 || newObjects != 0 || legacySnapshot(t, s) != before {
				t.Fatalf("rollback version=%d objects=%d", version, newObjects)
			}
		})
	}
}

func TestProfileMigrationFailureRollsBackWithoutChangingV10Data(t *testing.T) {
	s := openTestStore(t)
	makeProfileV10(t, s)
	if _, err := s.db.Exec(`insert into accounts values('acct','codex','A','','pro','2026-07-12T00:00:00Z'); create table profiles(blocker text)`); err != nil {
		t.Fatal(err)
	}
	before := legacySnapshot(t, s)
	if err := s.migrateProfiles(context.Background()); err == nil {
		t.Fatal("conflicting table accepted")
	}
	var accounts, version, profileAccounts int
	if err := s.db.QueryRow(`select (select count(*) from accounts),(select max(version)),(select count(*) from sqlite_master where type='table' and name='profile_accounts') from schema_migrations`).Scan(&accounts, &version, &profileAccounts); err != nil {
		t.Fatal(err)
	}
	if accounts != 1 || version != 10 || profileAccounts != 0 {
		t.Fatalf("accounts=%d version=%d partial=%d", accounts, version, profileAccounts)
	}
	if after := legacySnapshot(t, s); after != before {
		t.Fatal("failed migration changed v10 schema or data")
	}
}

func TestProfileMigrationPreservesPrunedReplayHighWater(t *testing.T) {
	s := openTestStore(t)
	insertReplayTestAccount(t, s, "codex", "acct")
	insertReplayTestEvent(t, s, "one", "codex", "acct", "2026-07-12T00:00:00Z")
	if _, err := s.db.Exec(`delete from policy_events; delete from policy_event_replay`); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := s.db.QueryRow(`select seq from sqlite_sequence where name='policy_event_replay'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	makeProfileV10(t, s)
	if err := s.migrateProfiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	var after, rows int
	if err := s.db.QueryRow(`select (select seq from sqlite_sequence where name='policy_event_replay'),(select count(*) from policy_event_replay)`).Scan(&after, &rows); err != nil {
		t.Fatal(err)
	}
	if after != before || rows != 0 {
		t.Fatalf("before=%d after=%d rows=%d", before, after, rows)
	}
}

func TestProfileMigrationPreservesCompleteV10Snapshot(t *testing.T) {
	s := openTestStore(t)
	insertReplayTestAccount(t, s, "codex", "acct")
	insertReplayTestEvent(t, s, "event", "codex", "acct", "2026-07-12T00:00:00Z")
	if _, err := s.db.Exec(`insert into notification_outbox(id,event_kind,source,account_ref,event_id,target,payload_version,payload_json,status,attempts,available_at,created_at,updated_at) values('proof-outbox','policy','test','acct','proof-event','t',1,'{}','pending',0,'2026-07-12T00:00:00Z','2026-07-12T00:00:00Z','2026-07-12T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`insert into server_settings values('proof','kept','2026-07-12T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	makeProfileV10(t, s)
	before := legacySnapshot(t, s)
	var highBefore int
	if err := s.db.QueryRow(`select seq from sqlite_sequence where name='policy_event_replay'`).Scan(&highBefore); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateProfiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := legacySnapshot(t, s)
	var highAfter int
	if err := s.db.QueryRow(`select seq from sqlite_sequence where name='policy_event_replay'`).Scan(&highAfter); err != nil {
		t.Fatal(err)
	}
	if before != after || highBefore != highAfter {
		t.Fatalf("v10 changed=%v high before=%d after=%d", before != after, highBefore, highAfter)
	}
}

func TestProfileMigrationFutureSchemaRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`insert into schema_migrations values(12,'future')`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err = Open(path); err == nil {
		t.Fatal("future schema accepted")
	}
}
