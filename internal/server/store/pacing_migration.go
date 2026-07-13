package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const PacingAlertSchemaVersion = 12

const pacingAlertSchemaSQL = `
create table pacing_alert_states (
 provider_id text not null,
 account_ref text not null,
 window_key text not null check(length(window_key)>0),
 reset_at text not null check(length(reset_at)>0),
 alerted integer not null check(alerted in (0,1)),
 last_risk text not null check(last_risk in ('unknown','low','elevated','high','critical')),
 last_observed_at text not null check(length(last_observed_at)>0),
 created_at text not null,
 updated_at text not null,
 primary key(provider_id,account_ref,window_key),
 foreign key(provider_id,account_ref) references accounts(provider_id,account_ref)
);
create table pacing_warning_events (
 id text primary key check(length(id)>0),
 provider_id text not null,
 account_ref text not null,
 account_label text not null,
 window_key text not null check(length(window_key)>0),
 label text not null check(length(label)>0),
 risk text not null check(risk='high'),
 confidence text not null check(confidence in ('none','low','medium','high')),
 used_percent real not null check(used_percent between 0 and 100),
 remaining_percent real not null check(remaining_percent between 0 and 100),
 pace_per_hour real not null check(pace_per_hour>=0),
 safe_per_hour real not null check(safe_per_hour>=0),
 projected_exhaustion_at text not null check(length(projected_exhaustion_at)>0),
 reset_at text not null check(length(reset_at)>0),
 detected_at text not null check(length(detected_at)>0),
 created_at text not null,
 foreign key(provider_id,account_ref) references accounts(provider_id,account_ref)
);
create index idx_pacing_warning_events_account_detected on pacing_warning_events(account_ref,detected_at);
create index idx_pacing_warning_events_retention on pacing_warning_events(detected_at);`

func (s *Store) migratePacingAlerts(ctx context.Context) (retErr error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	var version sql.NullInt64
	if err = conn.QueryRowContext(ctx, `select max(version) from schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version.Valid && version.Int64 >= PacingAlertSchemaVersion {
		return validatePacingAlertSchema(ctx, conn)
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
	if version.Valid && version.Int64 >= PacingAlertSchemaVersion {
		if err = validatePacingAlertSchema(ctx, conn); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `commit`)
		return err
	}
	if _, err = conn.ExecContext(ctx, pacingAlertSchemaSQL); err != nil {
		return err
	}
	if err = validatePacingAlertSchema(ctx, conn); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `insert into schema_migrations values(?,?)`, PacingAlertSchemaVersion, formatTime(time.Now())); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `commit`)
	return err
}

func validatePacingAlertSchema(ctx context.Context, q profileSchemaQuerier) error {
	for table, fragments := range map[string][]string{
		"pacing_alert_states":   {"primary key(provider_id,account_ref,window_key)", "alerted in (0,1)", "foreign key(provider_id,account_ref) references accounts(provider_id,account_ref)"},
		"pacing_warning_events": {"id text primary key", "risk='high'", "foreign key(provider_id,account_ref) references accounts(provider_id,account_ref)"},
	} {
		var definition string
		if err := q.QueryRowContext(ctx, `select sql from sqlite_master where type='table' and name=?`, table).Scan(&definition); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("invalid pacing schema: missing table %s", table)
		} else if err != nil {
			return err
		}
		normalized := strings.ToLower(strings.NewReplacer(" ", "", "\n", "", "\t", "", "\r", "").Replace(definition))
		for _, fragment := range fragments {
			want := strings.ToLower(strings.ReplaceAll(fragment, " ", ""))
			if !strings.Contains(normalized, want) {
				return fmt.Errorf("invalid pacing schema: table %s missing constraint %s", table, fragment)
			}
		}
	}
	return nil
}
