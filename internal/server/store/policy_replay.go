package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const PolicyReplaySchemaVersion = 10

const policyReplayV9TriggerSQL = `create trigger policy_events_replay_after_insert
after insert on policy_events
begin
 insert into policy_event_replay(policy_event_id) values(new.id);
end`

const policyReplayV9SchemaSQL = `
create table policy_event_replay (
 replay_seq integer primary key autoincrement,
 policy_event_id text not null unique,
 foreign key(policy_event_id) references policy_events(id) on delete cascade
);
` + policyReplayV9TriggerSQL + `;
insert into policy_event_replay(policy_event_id)
select id from policy_events order by detected_at collate binary,id collate binary;`

const policyReplayTriggerSQL = `create trigger policy_events_replay_after_insert
after insert on policy_events
begin
 insert into policy_event_replay(policy_event_id,provider_id,account_ref) values(new.id,new.provider_id,new.account_ref);
end`

const policyReplayV10MigrationSQL = `
create temp table policy_replay_high_water(value integer not null);
insert into policy_replay_high_water select coalesce((select seq from sqlite_sequence where name='policy_event_replay'),0);
drop trigger policy_events_replay_after_insert;
alter table policy_event_replay rename to policy_event_replay_v9;
create table policy_event_replay (
 replay_seq integer primary key autoincrement,
 policy_event_id text unique,
 provider_id text not null,
 account_ref text not null,
 foreign key(policy_event_id) references policy_events(id) on delete set null
);
` + policyReplayTriggerSQL + `;
insert into policy_event_replay(replay_seq,policy_event_id,provider_id,account_ref)
select old.replay_seq,old.policy_event_id,p.provider_id,p.account_ref from policy_event_replay_v9 old join policy_events p on p.id=old.policy_event_id order by old.replay_seq;
drop table policy_event_replay_v9;
delete from sqlite_sequence where name in ('policy_event_replay','policy_event_replay_v9');
insert into sqlite_sequence(name,seq) select 'policy_event_replay',value from policy_replay_high_water;
drop table policy_replay_high_water;`

func (s *Store) migratePolicyEventReplay(ctx context.Context) error {
	if err := s.migratePolicyEventReplayV9(ctx); err != nil {
		return err
	}
	return s.migratePolicyEventReplayV10(ctx)
}

func (s *Store) migratePolicyEventReplayV9(ctx context.Context) (retErr error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	var version sql.NullInt64
	if err = conn.QueryRowContext(ctx, `select max(version) from schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version.Valid && version.Int64 >= 9 {
		return nil
	}
	if _, err = conn.ExecContext(ctx, `begin immediate`); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			_, _ = conn.ExecContext(context.Background(), `rollback`)
		}
	}()
	if err = conn.QueryRowContext(ctx, `select max(version) from schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version.Valid && version.Int64 >= 9 {
		_, err = conn.ExecContext(ctx, `commit`)
		return err
	}
	if _, err = conn.ExecContext(ctx, policyReplayV9SchemaSQL); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `insert into schema_migrations(version,applied_at) values(9,?)`, formatTime(time.Now())); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `commit`)
	return err
}

func (s *Store) migratePolicyEventReplayV10(ctx context.Context) (retErr error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	var version sql.NullInt64
	if err = conn.QueryRowContext(ctx, `select max(version) from schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version.Valid && version.Int64 >= PolicyReplaySchemaVersion {
		return validatePolicyReplaySchema(ctx, conn)
	}
	if _, err = conn.ExecContext(ctx, `begin immediate`); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			_, _ = conn.ExecContext(context.Background(), `rollback`)
		}
	}()
	if err = conn.QueryRowContext(ctx, `select max(version) from schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version.Valid && version.Int64 >= PolicyReplaySchemaVersion {
		if err = validatePolicyReplaySchema(ctx, conn); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `commit`)
		return err
	}
	if _, err = conn.ExecContext(ctx, policyReplayV10MigrationSQL); err != nil {
		return err
	}
	if err = validatePolicyReplaySchema(ctx, conn); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `insert into schema_migrations(version,applied_at) values(?,?)`, PolicyReplaySchemaVersion, formatTime(time.Now())); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `commit`)
	return err
}

func validatePolicyReplaySchema(ctx context.Context, db policySchemaQuerier) error {
	rows, err := db.QueryContext(ctx, `select sql from sqlite_master where type='table' and name='policy_event_replay'`)
	if err != nil {
		return fmt.Errorf("validate policy replay table: %w", err)
	}
	var tableSQL string
	if !rows.Next() {
		_ = rows.Close()
		return fmt.Errorf("invalid policy replay schema: missing table policy_event_replay")
	}
	if err := rows.Scan(&tableSQL); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	normalize := func(v string) string {
		return strings.NewReplacer(" ", "", "\n", "", "\t", "", "\"", "", "`", "").Replace(strings.ToLower(v))
	}
	table := normalize(tableSQL)
	for _, want := range []string{"replay_seq integer primary key autoincrement", "policy_event_id text unique", "provider_id text not null", "account_ref text not null", "foreign key(policy_event_id) references policy_events(id) on delete set null"} {
		if !strings.Contains(table, normalize(want)) {
			return fmt.Errorf("invalid policy replay schema: table missing constraint %s", want)
		}
	}
	rows, err = db.QueryContext(ctx, `select sql from sqlite_master where type='trigger' and name='policy_events_replay_after_insert'`)
	if err != nil {
		return fmt.Errorf("validate policy replay trigger: %w", err)
	}
	var triggerSQL string
	if !rows.Next() {
		_ = rows.Close()
		return fmt.Errorf("invalid policy replay schema: missing trigger policy_events_replay_after_insert")
	}
	if err := rows.Scan(&triggerSQL); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	if normalize(triggerSQL) != normalize(policyReplayTriggerSQL) {
		return fmt.Errorf("invalid policy replay schema: noncanonical trigger policy_events_replay_after_insert")
	}
	rows, err = db.QueryContext(ctx, `select count(*) from policy_events p left join policy_event_replay r on r.policy_event_id=p.id where r.policy_event_id is null`)
	if err != nil {
		return err
	}
	var missing int
	if rows.Next() {
		err = rows.Scan(&missing)
	} else {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	if missing != 0 {
		return fmt.Errorf("invalid policy replay schema: %d policy events are unmapped", missing)
	}
	rows, err = db.QueryContext(ctx, `select count(*) from policy_event_replay r join policy_events p on p.id=r.policy_event_id where r.provider_id<>p.provider_id or r.account_ref<>p.account_ref`)
	if err != nil {
		return err
	}
	var mismatched int
	if rows.Next() {
		err = rows.Scan(&mismatched)
	} else {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	if mismatched != 0 {
		return fmt.Errorf("invalid policy replay schema: %d ownership mismatches", mismatched)
	}
	return nil
}

const maxPolicyReplayPageSize = 100

type PolicyReplayEvent struct {
	ReplaySeq      int64
	PolicyEventID  string
	ProviderID     string
	AccountRef     string
	EventKind      string
	RuleKind       string
	PayloadVersion int
	PayloadJSON    string
	DetectedAt     time.Time
}

type PolicyReplayPage struct {
	Events          []PolicyReplayEvent
	NextCursor      int64
	HighWater       int64
	OldestAvailable int64
	PrunedThrough   int64
}

func (s *Store) LoadPolicyEventReplay(ctx context.Context, providerID, accountRef string, after, ceiling int64, limit int) (PolicyReplayPage, error) {
	if providerID == "" || accountRef == "" {
		return PolicyReplayPage{}, fmt.Errorf("policy replay provider and account are required")
	}
	if after < 0 {
		return PolicyReplayPage{}, fmt.Errorf("policy replay cursor must not be negative")
	}
	if ceiling < 0 {
		return PolicyReplayPage{}, fmt.Errorf("policy replay ceiling must not be negative")
	}
	if limit <= 0 || limit > maxPolicyReplayPageSize {
		return PolicyReplayPage{}, fmt.Errorf("policy replay limit must be between 1 and %d", maxPolicyReplayPageSize)
	}
	page := PolicyReplayPage{Events: make([]PolicyReplayEvent, 0, limit), NextCursor: after}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PolicyReplayPage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.QueryRowContext(ctx, `select coalesce((select seq from sqlite_sequence where name='policy_event_replay'),0),coalesce(min(replay_seq) filter(where provider_id=? and account_ref=? and policy_event_id is not null),0),coalesce(max(replay_seq) filter(where provider_id=? and account_ref=? and policy_event_id is null),0) from policy_event_replay`, providerID, accountRef, providerID, accountRef).Scan(&page.HighWater, &page.OldestAvailable, &page.PrunedThrough); err != nil {
		return PolicyReplayPage{}, err
	}
	if ceiling > 0 && ceiling < page.HighWater {
		page.HighWater = ceiling
	}
	if s.loadPolicyReplayFault != nil {
		if err := s.loadPolicyReplayFault("after_snapshot"); err != nil {
			return PolicyReplayPage{}, err
		}
	}
	rows, err := tx.QueryContext(ctx, `select r.replay_seq,coalesce(p.id,''),r.provider_id,r.account_ref,coalesce(p.event_kind,''),coalesce(p.rule_kind,''),coalesce(p.payload_version,0),coalesce(p.payload_json,''),coalesce(p.detected_at,'') from policy_event_replay r left join policy_events p on p.id=r.policy_event_id where r.provider_id=? and r.account_ref=? and r.replay_seq>? and r.replay_seq<=? order by r.replay_seq asc limit ?`, providerID, accountRef, after, page.HighWater, limit)
	if err != nil {
		return PolicyReplayPage{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var event PolicyReplayEvent
		var detected string
		if err := rows.Scan(&event.ReplaySeq, &event.PolicyEventID, &event.ProviderID, &event.AccountRef, &event.EventKind, &event.RuleKind, &event.PayloadVersion, &event.PayloadJSON, &detected); err != nil {
			return PolicyReplayPage{}, err
		}
		event.DetectedAt = parseDBTime(detected)
		page.Events = append(page.Events, event)
		page.NextCursor = event.ReplaySeq
	}
	if err := rows.Err(); err != nil {
		return PolicyReplayPage{}, err
	}
	if err := rows.Close(); err != nil {
		return PolicyReplayPage{}, err
	}
	if len(page.Events) == 0 {
		page.NextCursor = page.HighWater
	}
	if err := tx.Commit(); err != nil {
		return PolicyReplayPage{}, err
	}
	return page, nil
}

func (s *Store) LoadLatestPolicyEventReplay(ctx context.Context, providerID, accountRef string, ceiling int64, limit int) (PolicyReplayPage, error) {
	if providerID == "" || accountRef == "" || ceiling < 0 || limit <= 0 || limit > maxPolicyReplayPageSize {
		return PolicyReplayPage{}, fmt.Errorf("invalid latest policy replay request")
	}
	page := PolicyReplayPage{Events: make([]PolicyReplayEvent, 0, limit), HighWater: ceiling, NextCursor: ceiling}
	rows, err := s.db.QueryContext(ctx, `select r.replay_seq,coalesce(p.id,''),r.provider_id,r.account_ref,coalesce(p.event_kind,''),coalesce(p.rule_kind,''),coalesce(p.payload_version,0),coalesce(p.payload_json,''),coalesce(p.detected_at,'') from policy_event_replay r left join policy_events p on p.id=r.policy_event_id where r.provider_id=? and r.account_ref=? and r.replay_seq<=? order by r.replay_seq desc limit ?`, providerID, accountRef, ceiling, limit)
	if err != nil {
		return PolicyReplayPage{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var event PolicyReplayEvent
		var detected string
		if err := rows.Scan(&event.ReplaySeq, &event.PolicyEventID, &event.ProviderID, &event.AccountRef, &event.EventKind, &event.RuleKind, &event.PayloadVersion, &event.PayloadJSON, &detected); err != nil {
			return PolicyReplayPage{}, err
		}
		event.DetectedAt = parseDBTime(detected)
		page.Events = append(page.Events, event)
	}
	if err := rows.Err(); err != nil {
		return PolicyReplayPage{}, err
	}
	for left, right := 0, len(page.Events)-1; left < right; left, right = left+1, right-1 {
		page.Events[left], page.Events[right] = page.Events[right], page.Events[left]
	}
	return page, nil
}
