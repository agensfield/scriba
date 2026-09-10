package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const accountSchemaSQL = `
create table auth_sources (
 source_ref text not null primary key check(length(source_ref)=24),
 enabled integer not null check(enabled in (0,1)),
 priority integer not null check(priority>=0),
 account_ref text,
 credentials_available integer not null check(credentials_available in (0,1)),
 last_identity_check text,
 created_at text not null,
 updated_at text not null,
 foreign key(account_ref) references accounts(account_ref),
 check(credentials_available=0 or (enabled=1 and account_ref is not null))
);
create index auth_sources_priority on auth_sources(enabled,priority,source_ref);
create table auth_source_poll_health (
 source_ref text not null primary key,
 last_attempt_at text,
 last_success_at text,
 last_failure_at text,
 consecutive_failures integer not null check(consecutive_failures>=0),
 failure_kind text not null check(failure_kind in ('','legacy','auth','network','provider','internal')),
 last_error_code text not null check(length(last_error_code)<=64),
 alert_state text not null check(alert_state in ('ok','failing')),
 updated_at text not null,
 foreign key(source_ref) references auth_sources(source_ref) on delete cascade
);
create table legacy_poll_health_evidence (
 legacy_ref text not null primary key,
 last_attempt_at text,
 last_success_at text,
 last_failure_at text,
 consecutive_failures integer not null,
 failure_kind text not null,
 last_error_code text not null,
 alert_state text not null,
 archived_at text not null
);`

const accountOutboxSchemaSQL = `create table notification_outbox_v13 (
 id text primary key, event_kind text not null, source text not null,
 account_ref text, event_id text not null, target text not null,
 payload_version integer not null check(payload_version>0), payload_json text not null check(json_valid(payload_json)),
 status text not null check(status in ('pending','leased','delivered','dead_letter')),
 attempts integer not null default 0 check(attempts>=0), available_at text not null,
 lease_token text, lease_expires_at text, delivered_at text, provider_message_id text,
 last_error text, dead_lettered_at text, created_at text not null, updated_at text not null,
 unique(event_kind,event_id,target),
 foreign key(account_ref) references accounts(account_ref),
 check((status='leased')=(lease_token is not null and lease_expires_at is not null)),
 check((status='delivered')=(delivered_at is not null)),
 check((status='dead_letter')=(dead_lettered_at is not null))
);`

func (s *Store) migrateAccounts(ctx context.Context) (retErr error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	var version sql.NullInt64
	if err = conn.QueryRowContext(ctx, `select max(version) from schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version.Valid && version.Int64 >= SchemaVersion {
		return validateAccountSchema(ctx, conn)
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
	if version.Valid && version.Int64 >= SchemaVersion {
		if err = validateAccountSchema(ctx, conn); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `commit`)
		return err
	}
	columns, err := tableColumnsConn(ctx, conn, "accounts")
	if err != nil {
		return err
	}
	if !columns["alias"] {
		if _, err = conn.ExecContext(ctx, `alter table accounts add column alias text not null default ''`); err != nil {
			return err
		}
	}
	if !columns["first_seen_at"] {
		if _, err = conn.ExecContext(ctx, `alter table accounts add column first_seen_at text not null default ''`); err != nil {
			return err
		}
	}
	if _, err = conn.ExecContext(ctx, `update accounts set first_seen_at=coalesce((select min(seen_at) from (select observed_at as seen_at from limit_observations where provider_id=accounts.provider_id and account_ref=accounts.account_ref union all select first_seen_at from profile_accounts where provider_id=accounts.provider_id and account_ref=accounts.account_ref)),updated_at) where first_seen_at=''`); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `create unique index accounts_alias_unique on accounts(alias) where alias<>''`); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, accountSchemaSQL); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `insert into legacy_poll_health_evidence(legacy_ref,last_attempt_at,last_success_at,last_failure_at,consecutive_failures,failure_kind,last_error_code,alert_state,archived_at) select profile_ref,last_attempt_at,last_success_at,last_failure_at,consecutive_failures,failure_kind,last_error_code,alert_state,? from profile_poll_health`, formatTime(time.Now())); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, accountOutboxSchemaSQL); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `insert into notification_outbox_v13(id,event_kind,source,account_ref,event_id,target,payload_version,payload_json,status,attempts,available_at,lease_token,lease_expires_at,delivered_at,provider_message_id,last_error,dead_lettered_at,created_at,updated_at) select id,event_kind,source,account_ref,event_id,target,payload_version,payload_json,status,attempts,available_at,lease_token,lease_expires_at,delivered_at,provider_message_id,last_error,dead_lettered_at,created_at,updated_at from notification_outbox`); err != nil {
		return fmt.Errorf("copy outbox: %w", err)
	}
	if s.accountMigrationFault != nil {
		if err = s.accountMigrationFault("after_v13_outbox_copy"); err != nil {
			return err
		}
	}
	for _, stmt := range []string{`drop table notification_outbox`, `alter table notification_outbox_v13 rename to notification_outbox`, `create index idx_notification_outbox_claim on notification_outbox(status,available_at,lease_expires_at,created_at)`, `drop table profile_accounts`, `drop table profile_poll_health`, `drop table profiles`} {
		if _, err = conn.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if _, err = conn.ExecContext(ctx, `insert into schema_migrations(version,applied_at) values(?,?)`, SchemaVersion, formatTime(time.Now())); err != nil {
		return err
	}
	if err = validateAccountSchema(ctx, conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `commit`)
	return err
}

func tableColumnsConn(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, table string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `pragma table_info(`+table+`)`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var cid, nn, pk int
		var name, typ string
		var d any
		if err = rows.Scan(&cid, &name, &typ, &nn, &d, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func validateAccountSchema(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) error {
	for table, want := range map[string][]string{"auth_sources": {"source_ref", "enabled", "priority", "account_ref", "credentials_available", "last_identity_check", "created_at", "updated_at"}, "auth_source_poll_health": {"source_ref", "last_attempt_at", "last_success_at", "last_failure_at", "consecutive_failures", "failure_kind", "last_error_code", "alert_state", "updated_at"}, "legacy_poll_health_evidence": {"legacy_ref", "last_attempt_at", "last_success_at", "last_failure_at", "consecutive_failures", "failure_kind", "last_error_code", "alert_state", "archived_at"}, "notification_outbox": {"id", "event_kind", "source", "account_ref", "event_id", "target", "payload_version", "payload_json", "status", "attempts", "available_at", "lease_token", "lease_expires_at", "delivered_at", "provider_message_id", "last_error", "dead_lettered_at", "created_at", "updated_at"}} {
		rows, err := q.QueryContext(ctx, `pragma table_info(`+table+`)`)
		if err != nil {
			return err
		}
		var got []string
		for rows.Next() {
			var cid, nn, pk int
			var name, typ string
			var d any
			if err = rows.Scan(&cid, &name, &typ, &nn, &d, &pk); err != nil {
				_ = rows.Close()
				return err
			}
			got = append(got, name)
		}
		_ = rows.Close()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			return fmt.Errorf("invalid account schema %s columns", table)
		}
	}
	cols, err := tableColumnsConn(ctx, q, "accounts")
	if err != nil {
		return err
	}
	if !cols["alias"] || !cols["first_seen_at"] {
		return fmt.Errorf("invalid account metadata schema")
	}
	return nil
}
