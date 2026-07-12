package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const OutboxSchemaVersion = 7

const outboxSchemaSQL = `
create table if not exists notification_outbox (
 id text primary key, event_kind text not null, source text not null,
 profile_ref text, account_ref text, event_id text not null, target text not null,
 payload_version integer not null check(payload_version>0), payload_json text not null check(json_valid(payload_json)),
 status text not null check(status in ('pending','leased','delivered','dead_letter')),
 attempts integer not null default 0 check(attempts>=0), available_at text not null,
 lease_token text, lease_expires_at text, delivered_at text, provider_message_id text,
 last_error text, dead_lettered_at text, created_at text not null, updated_at text not null,
 unique(event_kind,event_id,target),
 check((status='leased')=(lease_token is not null and lease_expires_at is not null)),
 check((status='delivered')=(delivered_at is not null)),
 check((status='dead_letter')=(dead_lettered_at is not null))
);
create index if not exists idx_notification_outbox_claim on notification_outbox(status,available_at,lease_expires_at,created_at);`

func (s *Store) migrateNotificationOutbox(ctx context.Context) (retErr error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var version sql.NullInt64
	if err = conn.QueryRowContext(ctx, `select max(version) from schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version.Valid && version.Int64 >= OutboxSchemaVersion {
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
	if version.Valid && version.Int64 >= OutboxSchemaVersion {
		_, err = conn.ExecContext(ctx, `commit`)
		return err
	}
	if _, err = conn.ExecContext(ctx, outboxSchemaSQL); err != nil {
		return err
	}
	for _, x := range []struct{ table, id, kind, event, account, payload string }{
		{"notification_deliveries", "event_id", "reset", "reset_events", "e.account_ref", `json_object('version',1,'kind','reset','account_label',e.account_label,'account_email',e.account_email,'account_plan',e.account_plan,'primary_trigger_label',e.primary_trigger_label,'secondary_trigger_labels',json(e.secondary_trigger_labels_json),'reset_kind',e.reset_kind,'previous_reset_at',e.previous_reset_at,'current_reset_at',e.current_reset_at,'previous_snapshot',json(e.previous_snapshot_json),'current_snapshot',json(e.current_snapshot_json),'joke_id',e.joke_id,'detected_at',e.detected_at)`},
		{"limit_warning_deliveries", "warning_id", "limit_warning", "limit_warning_events", "e.account_ref", `json_object('version',1,'kind','limit_warning','account_label',e.account_label,'account_email',e.account_email,'account_plan',e.account_plan,'label',e.label,'threshold_remaining',e.threshold_remaining,'used_percent',e.used_percent,'remaining_percent',e.remaining_percent,'reset_at',e.reset_at,'snapshot',json(e.snapshot_json),'detected_at',e.detected_at)`},
		{"reset_grant_warning_deliveries", "warning_id", "reset_grant_warning", "reset_grant_warning_events", "e.account_ref", `json_object('version',1,'kind','reset_grant_warning','account_label',e.account_label,'account_email',e.account_email,'account_plan',e.account_plan,'credit_id',e.credit_id,'credit_title',e.credit_title,'threshold_days',e.threshold_days,'expires_at',e.expires_at,'snapshot',json(e.snapshot_json),'detected_at',e.detected_at)`},
		{"reset_grant_deliveries", "event_id", "reset_grant", "reset_grant_events", "e.account_ref", `json_object('version',1,'kind','reset_grant','account_label',e.account_label,'account_email',e.account_email,'account_plan',e.account_plan,'credit_id',e.credit_id,'credit_title',e.credit_title,'reset_type',e.reset_type,'granted_at',e.granted_at,'expires_at',e.expires_at,'available_count',e.available_count,'snapshot',json(e.snapshot_json),'detected_at',e.detected_at)`},
		{"radar_alert_deliveries", "alert_id", "radar_alert", "radar_alert_events", "null", `json_object('version',1,'kind','radar_alert','milestone',e.milestone,'probability_24h',e.probability_24h,'probability_48h',e.probability_48h,'level',e.level,'expected_window',e.expected_window,'reasoning_summary',e.reasoning_summary,'checked_at',e.checked_at,'detected_at',e.detected_at,'snapshot',json(e.snapshot_json))`},
	} {
		var expected int
		if err = conn.QueryRowContext(ctx, fmt.Sprintf(`select count(*) from %s`, x.table)).Scan(&expected); err != nil {
			return err
		}
		q := fmt.Sprintf(`insert into notification_outbox(id,event_kind,source,account_ref,event_id,target,payload_version,payload_json,status,attempts,available_at,delivered_at,provider_message_id,last_error,dead_lettered_at,created_at,updated_at)
select 'legacy:'||d.id,?,'legacy-v6',%s,d.%s,d.target,1,%s,case when d.status='delivered' then 'delivered' when d.attempts>=? then 'dead_letter' else 'pending' end,d.attempts,coalesce(d.next_attempt_at,d.created_at),d.delivered_at,d.provider_message_id,d.last_error,case when d.status!='delivered' and d.attempts>=? then d.updated_at end,d.created_at,d.updated_at from %s d join %s e on e.id=d.%s on conflict(event_kind,event_id,target) do nothing`, x.account, x.id, x.payload, x.table, x.event, x.id)
		if _, err = conn.ExecContext(ctx, q, x.kind, OutboxMaxAttempts, OutboxMaxAttempts); err != nil {
			return fmt.Errorf("backfill %s: %w", x.table, err)
		}
		var actual int
		if err = conn.QueryRowContext(ctx, `select count(*) from notification_outbox where event_kind=? and source='legacy-v6'`, x.kind).Scan(&actual); err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("backfill %s: expected %d eligible deliveries, got %d", x.table, expected, actual)
		}
	}
	if _, err = conn.ExecContext(ctx, `insert into schema_migrations(version,applied_at) values(?,?)`, OutboxSchemaVersion, formatTime(time.Now())); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `commit`); err != nil {
		return err
	}
	return nil
}
