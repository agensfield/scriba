package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func insertReplayTestAccount(t *testing.T, s *Store, provider, account string) {
	t.Helper()
	_, err := s.db.Exec(`insert into accounts(account_ref,provider_id,label,email,plan,updated_at) values(?,?,?,?,?,?)`, account, provider, "Test", "", "pro", "2026-07-12T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
}

func insertReplayTestEvent(t *testing.T, s *Store, id, provider, account, detected string) {
	t.Helper()
	_, err := s.db.Exec(`insert into policy_events(id,semantic_key,event_kind,semantic_event_id,rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,payload_version,payload_json,detected_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, id, "limit_warning", id, "rule", "weekly_limit", "remaining_checkpoint", provider, account, "rev", "hash", 1, `{}`, detected, detected)
	if err != nil {
		t.Fatal(err)
	}
}

func makeReplayV8(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.Exec(`drop trigger policy_events_replay_after_insert; drop table policy_event_replay; delete from schema_migrations where version>=9`); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyReplayMigrationBackfillsDeterministicallyAndIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	insertReplayTestAccount(t, s, "codex", "acct")
	insertReplayTestEvent(t, s, "z", "codex", "acct", "2026-07-12T00:00:00Z")
	insertReplayTestEvent(t, s, "a", "codex", "acct", "2026-07-12T00:00:00Z")
	makeReplayV8(t, s)
	if err := s.migratePolicyEventReplay(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.migratePolicyEventReplay(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.Query(`select policy_event_id from policy_event_replay order by replay_seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if fmt.Sprint(got) != "[a z]" {
		t.Fatalf("backfill order=%v", got)
	}
	var versions int
	if err := s.db.QueryRow(`select count(*) from schema_migrations where version=10`).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("versions=%d err=%v", versions, err)
	}
}

func TestPolicyReplayMigratesExactCommittedV9PreservingSequenceAndHighWater(t *testing.T) {
	s := openTestStore(t)
	insertReplayTestAccount(t, s, "codex", "acct")
	insertReplayTestEvent(t, s, "one", "codex", "acct", "2026-07-12T00:00:00Z")
	insertReplayTestEvent(t, s, "two", "codex", "acct", "2026-07-12T00:00:01Z")
	if _, err := s.db.Exec(`drop trigger policy_events_replay_after_insert; drop table policy_event_replay; delete from schema_migrations where version>=9; ` + policyReplayV9SchemaSQL + `; insert into schema_migrations(version,applied_at) values(9,'2026-07-12T00:00:02Z'); delete from policy_events where id='two'`); err != nil {
		t.Fatal(err)
	}
	if err := s.migratePolicyEventReplay(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.migratePolicyEventReplay(context.Background()); err != nil {
		t.Fatal(err)
	}
	var seq, version int
	if err := s.db.QueryRow(`select seq from sqlite_sequence where name='policy_event_replay'`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`select max(version) from schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := s.db.QueryRow(`select policy_event_id from policy_event_replay where replay_seq=1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if seq != 2 || version != 10 || id != "one" {
		t.Fatalf("seq=%d version=%d id=%q", seq, version, id)
	}
}

func TestPolicyReplayMigratesFullyPrunedCommittedV9PreservingHighWater(t *testing.T) {
	s := openTestStore(t)
	insertReplayTestAccount(t, s, "codex", "acct")
	insertReplayTestEvent(t, s, "one", "codex", "acct", "2026-07-12T00:00:00Z")
	insertReplayTestEvent(t, s, "two", "codex", "acct", "2026-07-12T00:00:01Z")
	if _, err := s.db.Exec(`drop trigger policy_events_replay_after_insert; drop table policy_event_replay; delete from schema_migrations where version>=9; ` + policyReplayV9SchemaSQL + `; insert into schema_migrations(version,applied_at) values(9,'2026-07-12T00:00:02Z'); delete from policy_events`); err != nil {
		t.Fatal(err)
	}
	if err := s.migratePolicyEventReplay(context.Background()); err != nil {
		t.Fatal(err)
	}
	var seq, mappings int
	if err := s.db.QueryRow(`select seq from sqlite_sequence where name='policy_event_replay'`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`select count(*) from policy_event_replay`).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if seq != 2 || mappings != 0 {
		t.Fatalf("seq=%d mappings=%d", seq, mappings)
	}
}

func TestPolicyReplayTriggerTransactionAndExactConflict(t *testing.T) {
	s := openTestStore(t)
	insertReplayTestAccount(t, s, "codex", "acct")
	insertReplayTestEvent(t, s, "direct", "codex", "acct", "2026-07-12T00:00:00Z")
	var mappings int
	if err := s.db.QueryRow(`select count(*) from policy_event_replay where policy_event_id='direct'`).Scan(&mappings); err != nil || mappings != 1 {
		t.Fatalf("mappings=%d err=%v", mappings, err)
	}
	if _, err := s.db.Exec(`insert into policy_events(id,semantic_key,event_kind,semantic_event_id,rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,payload_version,payload_json,detected_at,created_at) values('direct','direct','limit_warning','direct','rule','weekly_limit','remaining_checkpoint','codex','acct','rev','hash',1,'{}','2026-07-12T00:00:00Z','2026-07-12T00:00:00Z') on conflict(id) do nothing`); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`select count(*) from policy_event_replay where policy_event_id='direct'`).Scan(&mappings); err != nil || mappings != 1 {
		t.Fatalf("conflict mappings=%d err=%v", mappings, err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`insert into policy_events(id,semantic_key,event_kind,semantic_event_id,rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,payload_version,payload_json,detected_at,created_at) values('rolled','rolled','limit_warning','rolled','rule','weekly_limit','remaining_checkpoint','codex','acct','rev','hash',1,'{}','2026-07-12T00:00:01Z','2026-07-12T00:00:01Z')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`select count(*) from policy_event_replay where policy_event_id='rolled'`).Scan(&mappings); err != nil || mappings != 0 {
		t.Fatalf("rollback mappings=%d err=%v", mappings, err)
	}
}

func TestPolicyReplayPagingIsolationHighWaterAndCancellation(t *testing.T) {
	s := openTestStore(t)
	insertReplayTestAccount(t, s, "codex", "a")
	insertReplayTestAccount(t, s, "codex", "b")
	for _, event := range []struct{ id, account string }{{"one", "a"}, {"other", "b"}, {"two", "a"}, {"three", "a"}} {
		insertReplayTestEvent(t, s, event.id, "codex", event.account, "2026-07-12T00:00:00Z")
	}
	ctx := context.Background()
	first, err := s.LoadPolicyEventReplay(ctx, "codex", "a", 0, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 2 || first.Events[0].PolicyEventID != "one" || first.Events[1].PolicyEventID != "two" {
		t.Fatalf("first=%#v", first)
	}
	second, err := s.LoadPolicyEventReplay(ctx, "codex", "a", first.NextCursor, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 || second.Events[0].PolicyEventID != "three" {
		t.Fatalf("second=%#v", second)
	}
	high := second.HighWater
	if _, err := s.db.Exec(`delete from policy_events where id in ('two','three')`); err != nil {
		t.Fatal(err)
	}
	pruned, err := s.LoadPolicyEventReplay(ctx, "codex", "a", first.NextCursor, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if pruned.HighWater != high || pruned.OldestAvailable == 0 || len(pruned.Events) != 1 || pruned.Events[0].PolicyEventID != "" {
		t.Fatalf("pruned=%#v high=%d", pruned, high)
	}
	if _, err := s.db.Exec(`delete from policy_events; delete from policy_event_replay`); err != nil {
		t.Fatal(err)
	}
	fullyPruned, err := s.LoadPolicyEventReplay(ctx, "codex", "a", 0, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if fullyPruned.HighWater != high || fullyPruned.OldestAvailable != 0 {
		t.Fatalf("fully pruned=%#v", fullyPruned)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.LoadPolicyEventReplay(canceled, "codex", "a", 0, 0, 1); err == nil {
		t.Fatal("canceled replay succeeded")
	}
}

func TestPolicyReplayRejectsMalformedClaimedV10(t *testing.T) {
	for _, mutation := range []string{`drop trigger policy_events_replay_after_insert`, `drop table policy_event_replay`, `drop trigger policy_events_replay_after_insert; create trigger policy_events_replay_after_insert after insert on policy_events when 0 begin insert into policy_event_replay(policy_event_id,provider_id,account_ref) values(new.id,new.provider_id,new.account_ref); end`, `insert into accounts(account_ref,provider_id,label,email,plan,updated_at) values('acct','codex','','','','2026-07-12T00:00:00Z'); insert into policy_events(id,semantic_key,event_kind,semantic_event_id,rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,payload_version,payload_json,detected_at,created_at) values('event','event','limit_warning','event','rule','weekly_limit','remaining_checkpoint','codex','acct','rev','hash',1,'{}','2026-07-12T00:00:00Z','2026-07-12T00:00:00Z'); update policy_event_replay set account_ref='wrong' where policy_event_id='event'`} {
		t.Run(mutation, func(t *testing.T) {
			s := openTestStore(t)
			if _, err := s.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if err := s.migratePolicyEventReplay(context.Background()); err == nil {
				t.Fatal("malformed v9 accepted")
			}
		})
	}
}

func TestPolicyReplayMigrationConcurrentCallsRecordOneVersion(t *testing.T) {
	s := openTestStore(t)
	makeReplayV8(t, s)
	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.migratePolicyEventReplay(context.Background()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var versions int
	if err := s.db.QueryRow(`select count(*) from schema_migrations where version=10`).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("versions=%d err=%v", versions, err)
	}
}

func TestPolicyReplayMigrationFailureRollsBack(t *testing.T) {
	s := openTestStore(t)
	makeReplayV8(t, s)
	if _, err := s.db.Exec(`create trigger policy_events_replay_after_insert after insert on policy_events begin select 1; end`); err != nil {
		t.Fatal(err)
	}
	if err := s.migratePolicyEventReplay(context.Background()); err == nil {
		t.Fatal("preexisting trigger did not fail migration")
	}
	var table, version int
	if err := s.db.QueryRow(`select count(*) from sqlite_master where type='table' and name='policy_event_replay'`).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`select max(version) from schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if table != 0 || version != 8 {
		t.Fatalf("partial migration table=%d version=%d", table, version)
	}
}

func TestLoadPolicyEventReplaySnapshotExcludesConcurrentInsert(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.sqlite")
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
	insertReplayTestAccount(t, a, "codex", "acct")
	insertReplayTestEvent(t, a, "first", "codex", "acct", "2026-07-12T00:00:00Z")
	captured := make(chan struct{})
	resume := make(chan struct{})
	a.loadPolicyReplayFault = func(stage string) error {
		if stage != "after_snapshot" {
			return fmt.Errorf("unexpected loader stage %q", stage)
		}
		close(captured)
		<-resume
		return nil
	}
	type result struct {
		page PolicyReplayPage
		err  error
	}
	results := make(chan result, 1)
	go func() {
		page, err := a.LoadPolicyEventReplay(context.Background(), "codex", "acct", 0, 0, 10)
		results <- result{page: page, err: err}
	}()
	<-captured
	insertReplayTestEvent(t, b, "second", "codex", "acct", "2026-07-12T00:00:01Z")
	close(resume)
	got := <-results
	if got.err != nil {
		t.Fatal(got.err)
	}
	if len(got.page.Events) != 1 || got.page.Events[0].PolicyEventID != "first" || got.page.HighWater != 1 {
		t.Fatalf("in-flight page=%#v", got.page)
	}
	a.loadPolicyReplayFault = nil
	page, err := a.LoadPolicyEventReplay(context.Background(), "codex", "acct", 0, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[1].PolicyEventID != "second" || page.HighWater != 2 {
		t.Fatalf("next page=%#v", page)
	}
	pinned, err := a.LoadPolicyEventReplay(context.Background(), "codex", "acct", 0, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if pinned.HighWater != 1 || len(pinned.Events) != 1 || pinned.Events[0].PolicyEventID != "first" {
		t.Fatalf("pinned page=%#v", pinned)
	}
}
