package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const ProfileSchemaVersion = 11

const profileSchemaSQL = `
create table profiles (
 profile_ref text not null primary key check(length(profile_ref) between 1 and 32),
 provider_id text not null check(length(provider_id) between 1 and 64),
 label text not null check(length(label) between 1 and 128),
 enabled integer not null check(enabled in (0,1)),
 is_default integer not null check(is_default in (0,1) and (is_default=0 or enabled=1)),
 created_at text not null,
 updated_at text not null
);
create unique index profiles_one_default on profiles(is_default) where is_default=1;
create unique index profiles_provider_identity on profiles(profile_ref,provider_id);
create table profile_accounts (
 profile_ref text not null,
 provider_id text not null,
 account_ref text not null,
 is_current integer not null check(is_current in (0,1)),
 first_seen_at text not null,
 last_seen_at text not null,
 primary key(profile_ref,provider_id,account_ref),
 foreign key(profile_ref,provider_id) references profiles(profile_ref,provider_id) on delete cascade,
 foreign key(provider_id,account_ref) references accounts(provider_id,account_ref)
);
create unique index profile_accounts_current on profile_accounts(profile_ref) where is_current=1;
create unique index profile_accounts_owner on profile_accounts(provider_id,account_ref);
create table profile_poll_health (
 profile_ref text not null primary key,
 last_attempt_at text,
 last_success_at text,
 last_failure_at text,
 consecutive_failures integer not null check(consecutive_failures>=0),
 failure_kind text not null check(failure_kind in ('','legacy','auth','network','provider','internal')),
 last_error_code text not null check(length(last_error_code)<=64),
 alert_state text not null check(alert_state in ('ok','failing')),
 updated_at text not null,
 foreign key(profile_ref) references profiles(profile_ref) on delete cascade
);`

func (s *Store) migrateProfiles(ctx context.Context) (retErr error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	var version sql.NullInt64
	if err = conn.QueryRowContext(ctx, `select max(version) from schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version.Valid && version.Int64 >= ProfileSchemaVersion {
		return validateProfileSchema(ctx, conn)
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
	if version.Valid && version.Int64 >= ProfileSchemaVersion {
		if err = validateProfileSchema(ctx, conn); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `commit`)
		return err
	}
	if _, err = conn.ExecContext(ctx, profileSchemaSQL); err != nil {
		return err
	}
	now := formatTime(time.Now())
	if _, err = conn.ExecContext(ctx, `insert into profiles values('default','codex','Default',1,1,?,?)`, now, now); err != nil {
		return err
	}
	if err = migrateProfileAccounts(ctx, conn); err != nil {
		return err
	}
	attempt := sanitizedLegacyTime(ctx, conn, "poll_attempt_at")
	success := sanitizedLegacyTime(ctx, conn, "poll_success_at")
	failure := sanitizedLegacyTime(ctx, conn, "poll_failure_at")
	failures := sanitizedLegacyFailureCount(ctx, conn)
	alert := "ok"
	var legacyAlert string
	if scanErr := conn.QueryRowContext(ctx, `select value from server_settings where key='health_alert_state'`).Scan(&legacyAlert); scanErr == nil && legacyAlert == "failing" && failures > 0 {
		alert = "failing"
	}
	failureKind := ""
	if failures > 0 {
		failureKind = "legacy"
	}
	if _, err = conn.ExecContext(ctx, `insert into profile_poll_health values('default',?,?,?,?,?,?,?,?)`, attempt, success, failure, failures, failureKind, "", alert, now); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `update notification_outbox set profile_ref='default' where profile_ref is null and account_ref is not null and exists(select 1 from profile_accounts p where p.profile_ref='default' and p.account_ref=notification_outbox.account_ref)`); err != nil {
		return err
	}
	var invalidOutbox int
	if err = conn.QueryRowContext(ctx, `select count(*) from notification_outbox o where (o.account_ref is null and o.profile_ref is not null) or (o.account_ref is not null and ((select count(*) from profile_accounts p where p.account_ref=o.account_ref)<>1 or o.profile_ref is null or o.profile_ref<>(select p.profile_ref from profile_accounts p where p.account_ref=o.account_ref)))`).Scan(&invalidOutbox); err != nil {
		return err
	}
	if invalidOutbox != 0 {
		return fmt.Errorf("profile migration found %d invalid outbox ownership rows", invalidOutbox)
	}
	if s.profileMigrationFault != nil {
		if err = s.profileMigrationFault("after_outbox_backfill"); err != nil {
			return err
		}
	}
	if err = validateProfileSchema(ctx, conn); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `insert into schema_migrations values(?,?)`, ProfileSchemaVersion, now); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `commit`)
	return err
}

func migrateProfileAccounts(ctx context.Context, conn *sql.Conn) error {
	type account struct {
		ref, updated string
		first, last  time.Time
	}
	rows, err := conn.QueryContext(ctx, `select account_ref,updated_at from accounts where provider_id='codex'`)
	if err != nil {
		return err
	}
	var accounts []account
	for rows.Next() {
		var a account
		if err = rows.Scan(&a.ref, &a.updated); err != nil {
			_ = rows.Close()
			return err
		}
		parsed, e := time.Parse(time.RFC3339Nano, a.updated)
		if e != nil {
			_ = rows.Close()
			return e
		}
		a.first, a.last = parsed, parsed
		accounts = append(accounts, a)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	current := ""
	var latest time.Time
	for i := range accounts {
		obs, queryErr := conn.QueryContext(ctx, `select observed_at from limit_observations where provider_id='codex' and account_ref=?`, accounts[i].ref)
		if queryErr != nil {
			return queryErr
		}
		seen := false
		for obs.Next() {
			var raw string
			if err = obs.Scan(&raw); err != nil {
				_ = obs.Close()
				return err
			}
			parsed, e := time.Parse(time.RFC3339Nano, raw)
			if e != nil {
				_ = obs.Close()
				return e
			}
			if !seen || parsed.Before(accounts[i].first) {
				accounts[i].first = parsed
			}
			if parsed.After(accounts[i].last) {
				accounts[i].last = parsed
			}
			seen = true
		}
		if err = obs.Err(); err != nil {
			_ = obs.Close()
			return err
		}
		_ = obs.Close()
		if current == "" || accounts[i].last.After(latest) || (accounts[i].last.Equal(latest) && accounts[i].ref < current) {
			current, latest = accounts[i].ref, accounts[i].last
		}
	}
	for _, a := range accounts {
		isCurrent := 0
		if a.ref == current {
			isCurrent = 1
		}
		if _, err = conn.ExecContext(ctx, `insert into profile_accounts values('default','codex',?,?,?,?)`, a.ref, isCurrent, formatTime(a.first), formatTime(a.last)); err != nil {
			return err
		}
	}
	return nil
}

func sanitizedLegacyTime(ctx context.Context, q profileSchemaQuerier, key string) any {
	var value string
	if err := q.QueryRowContext(ctx, `select value from server_settings where key=?`, key).Scan(&value); err != nil {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return formatTime(parsed)
}

func sanitizedLegacyFailureCount(ctx context.Context, q profileSchemaQuerier) int {
	var value string
	if err := q.QueryRowContext(ctx, `select value from server_settings where key='poll_failure_count'`).Scan(&value); err != nil {
		return 0
	}
	count, err := strconv.Atoi(value)
	if err != nil || count < 0 {
		return 0
	}
	return count
}

type profileSchemaQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func validateProfileSchema(ctx context.Context, q profileSchemaQuerier) error {
	norm := func(v string) string {
		var b strings.Builder
		quote := rune(0)
		for _, r := range v {
			if quote == 0 && (r == '\'' || r == '"' || r == '`') {
				quote = r
				b.WriteRune(r)
				continue
			}
			if quote != 0 && r == quote {
				quote = 0
				b.WriteRune(r)
				continue
			}
			if quote == 0 && (r == ' ' || r == '\n' || r == '\t' || r == '\r') {
				continue
			}
			if quote == 0 && r >= 'A' && r <= 'Z' {
				r += 'a' - 'A'
			}
			b.WriteRune(r)
		}
		return b.String()
	}
	expectedColumns := map[string][]struct {
		name, typ   string
		notNull, pk int
	}{
		"profiles":            {{"profile_ref", "TEXT", 1, 1}, {"provider_id", "TEXT", 1, 0}, {"label", "TEXT", 1, 0}, {"enabled", "INTEGER", 1, 0}, {"is_default", "INTEGER", 1, 0}, {"created_at", "TEXT", 1, 0}, {"updated_at", "TEXT", 1, 0}},
		"profile_accounts":    {{"profile_ref", "TEXT", 1, 1}, {"provider_id", "TEXT", 1, 2}, {"account_ref", "TEXT", 1, 3}, {"is_current", "INTEGER", 1, 0}, {"first_seen_at", "TEXT", 1, 0}, {"last_seen_at", "TEXT", 1, 0}},
		"profile_poll_health": {{"profile_ref", "TEXT", 1, 1}, {"last_attempt_at", "TEXT", 0, 0}, {"last_success_at", "TEXT", 0, 0}, {"last_failure_at", "TEXT", 0, 0}, {"consecutive_failures", "INTEGER", 1, 0}, {"failure_kind", "TEXT", 1, 0}, {"last_error_code", "TEXT", 1, 0}, {"alert_state", "TEXT", 1, 0}, {"updated_at", "TEXT", 1, 0}},
	}
	for table, wants := range expectedColumns {
		rows, err := q.QueryContext(ctx, `pragma table_info(`+table+`)`)
		if err != nil {
			return err
		}
		var got []struct {
			name, typ   string
			notNull, pk int
		}
		for rows.Next() {
			var cid int
			var dflt any
			var c struct {
				name, typ   string
				notNull, pk int
			}
			if err := rows.Scan(&cid, &c.name, &c.typ, &c.notNull, &dflt, &c.pk); err != nil {
				_ = rows.Close()
				return err
			}
			got = append(got, c)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
		if fmt.Sprint(got) != fmt.Sprint(wants) {
			return fmt.Errorf("invalid profile schema %s columns: got %v", table, got)
		}
	}
	for table := range expectedColumns {
		var sqlText string
		if err := q.QueryRowContext(ctx, `select sql from sqlite_master where type='table' and name=?`, table).Scan(&sqlText); err != nil {
			return fmt.Errorf("invalid profile schema %s: %w", table, err)
		}
		start := strings.Index(profileSchemaSQL, "create table "+table+" (")
		if start < 0 {
			return fmt.Errorf("missing canonical profile schema %s", table)
		}
		end := strings.Index(profileSchemaSQL[start:], "\n);")
		if end < 0 {
			return fmt.Errorf("missing canonical profile schema terminator %s", table)
		}
		expected := profileSchemaSQL[start : start+end+2]
		if norm(sqlText) != norm(expected) {
			return fmt.Errorf("invalid profile schema %s definition", table)
		}
	}
	for name, want := range map[string]string{"profiles_one_default": "create unique index profiles_one_default on profiles(is_default) where is_default=1", "profiles_provider_identity": "create unique index profiles_provider_identity on profiles(profile_ref,provider_id)", "profile_accounts_current": "create unique index profile_accounts_current on profile_accounts(profile_ref) where is_current=1", "profile_accounts_owner": "create unique index profile_accounts_owner on profile_accounts(provider_id,account_ref)"} {
		var got string
		if err := q.QueryRowContext(ctx, `select sql from sqlite_master where type='index' and name=?`, name).Scan(&got); err != nil || norm(got) != norm(want) {
			return fmt.Errorf("invalid profile schema index %s", name)
		}
	}
	return nil
}
