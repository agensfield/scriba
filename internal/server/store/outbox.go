package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/radar"
	"github.com/agensfield/scriba/internal/resetwatch"
)

const OutboxMaxAttempts = 8

type OutboxMessage struct {
	ID, EventKind, Source, ProfileRef, AccountRef, EventID, Target string
	PayloadVersion                                                 int
	PayloadJSON, Status                                            string
	Attempts                                                       int
	AvailableAt                                                    time.Time
	LeaseToken                                                     string
	LeaseExpiresAt, DeliveredAt, DeadLetteredAt                    *time.Time
	ProviderMessageID, LastError                                   string
	CreatedAt, UpdatedAt                                           time.Time
}

type OutboxEnqueue struct {
	ID, EventKind, Source, ProfileRef, AccountRef, EventID, Target string
	PayloadVersion                                                 int
	PayloadJSON                                                    string
	AvailableAt                                                    time.Time
}

type OutboxStats struct{ Pending, Leased, Delivered, DeadLetter int }

func OutboxID(kind, eventID, target string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + eventID + "\x00" + target))
	return "outbox_" + hex.EncodeToString(sum[:16])
}

func EnqueueOutbox(ctx context.Context, tx *sql.Tx, in OutboxEnqueue, now time.Time) error {
	if tx == nil {
		return errors.New("outbox enqueue requires transaction")
	}
	if in.EventKind == "" || in.Source == "" || in.EventID == "" || in.Target == "" || in.PayloadVersion < 1 || in.PayloadJSON == "" {
		return errors.New("invalid outbox envelope")
	}
	if in.AvailableAt.IsZero() {
		in.AvailableAt = now
	}
	if in.ID == "" {
		in.ID = OutboxID(in.EventKind, in.EventID, in.Target)
	}
	r, err := tx.ExecContext(ctx, `insert into notification_outbox
(id,event_kind,source,profile_ref,account_ref,event_id,target,payload_version,payload_json,status,attempts,available_at,created_at,updated_at)
values(?,?,?,?,?,?,?,?,?,'pending',0,?,?,?) on conflict(event_kind,event_id,target) do nothing`, in.ID, in.EventKind, in.Source, nullString(in.ProfileRef), nullString(in.AccountRef), in.EventID, in.Target, in.PayloadVersion, in.PayloadJSON, formatTime(in.AvailableAt), formatTime(now), formatTime(now))
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 1 {
		return nil
	}
	var id, source, profile, account, payload string
	var version int
	err = tx.QueryRowContext(ctx, `select id,source,coalesce(profile_ref,''),coalesce(account_ref,''),payload_version,payload_json from notification_outbox where event_kind=? and event_id=? and target=?`, in.EventKind, in.EventID, in.Target).Scan(&id, &source, &profile, &account, &version, &payload)
	if err != nil {
		return err
	}
	if id != in.ID || source != in.Source || profile != in.ProfileRef || account != in.AccountRef || version != in.PayloadVersion || payload != in.PayloadJSON {
		return errors.New("conflicting outbox semantic duplicate")
	}
	return nil
}

func (s *Store) ClaimOutbox(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]OutboxMessage, error) {
	return s.ClaimOutboxForTarget(ctx, "telegram:1", now, lease, limit)
}

func (s *Store) ClaimOutboxForTarget(ctx context.Context, target string, now time.Time, lease time.Duration, limit int) ([]OutboxMessage, error) {
	if target == "" {
		return nil, errors.New("outbox target is required")
	}
	if lease <= 0 {
		return nil, errors.New("outbox lease must be positive")
	}
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("outbox claim limit must be between 1 and 1000")
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `update notification_outbox set status='dead_letter',dead_lettered_at=?,lease_token=null,lease_expires_at=null,updated_at=? where status='leased' and lease_expires_at<=? and attempts>=?`, formatTime(now), formatTime(now), formatTime(now), OutboxMaxAttempts); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `update notification_outbox set status='leased',attempts=attempts+1,lease_token=?,lease_expires_at=?,updated_at=? where id in
(select id from notification_outbox where target=? and attempts < ? and ((status='pending' and available_at<=?) or (status='leased' and lease_expires_at<=?)) order by available_at,created_at,id limit ?)
returning id,event_kind,source,coalesce(profile_ref,''),coalesce(account_ref,''),event_id,target,payload_version,payload_json,status,attempts,available_at,coalesce(lease_token,''),lease_expires_at,delivered_at,coalesce(provider_message_id,''),coalesce(last_error,''),dead_lettered_at,created_at,updated_at`, token, formatTime(now.Add(lease)), formatTime(now), target, OutboxMaxAttempts, formatTime(now), formatTime(now), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []OutboxMessage
	for rows.Next() {
		m, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) FinishOutboxSuccess(ctx context.Context, id, token, providerID string, now time.Time) (bool, error) {
	r, err := s.db.ExecContext(ctx, `update notification_outbox set status='delivered',delivered_at=?,provider_message_id=?,lease_token=null,lease_expires_at=null,last_error=null,updated_at=? where id=? and status='leased' and lease_token=?`, formatTime(now), nullString(providerID), formatTime(now), id, token)
	return changed(r, err)
}

func (s *Store) FinishOutboxFailure(ctx context.Context, claim OutboxMessage, message string, now time.Time) (bool, error) {
	if claim.Attempts < 1 || claim.LeaseToken == "" {
		return false, errors.New("invalid outbox claim")
	}
	r, err := s.db.ExecContext(ctx, `update notification_outbox set status=case when attempts>=? then 'dead_letter' else 'pending' end,available_at=case when attempts>=? then available_at else ? end,dead_lettered_at=case when attempts>=? then ? else null end,last_error=?,lease_token=null,lease_expires_at=null,updated_at=? where id=? and status='leased' and lease_token=? and attempts=?`, OutboxMaxAttempts, OutboxMaxAttempts, formatTime(now.Add(OutboxBackoff(claim.Attempts))), OutboxMaxAttempts, formatTime(now), message, formatTime(now), claim.ID, claim.LeaseToken, claim.Attempts)
	return changed(r, err)
}

func OutboxBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := time.Minute * time.Duration(1<<min(attempt-1, 6))
	return d
}
func (s *Store) OutboxStats(ctx context.Context) (OutboxStats, error) {
	var v OutboxStats
	err := s.db.QueryRowContext(ctx, `select count(*) filter(where status='pending'),count(*) filter(where status='leased'),count(*) filter(where status='delivered'),count(*) filter(where status='dead_letter') from notification_outbox`).Scan(&v.Pending, &v.Leased, &v.Delivered, &v.DeadLetter)
	return v, err
}
func (s *Store) ReconcileExpiredOutbox(ctx context.Context, now time.Time) (int64, error) {
	r, e := s.db.ExecContext(ctx, `update notification_outbox set status=case when attempts>=? then 'dead_letter' else 'pending' end,dead_lettered_at=case when attempts>=? then ? end,available_at=case when attempts>=? then available_at else ? end,lease_token=null,lease_expires_at=null,updated_at=? where status='leased' and lease_expires_at<=?`, OutboxMaxAttempts, OutboxMaxAttempts, formatTime(now), OutboxMaxAttempts, formatTime(now), formatTime(now), formatTime(now))
	if e != nil {
		return 0, e
	}
	return r.RowsAffected()
}

type scanner interface{ Scan(...any) error }

func scanOutbox(r scanner) (OutboxMessage, error) {
	var m OutboxMessage
	var a, c, u string
	var ln, dn, ddn sql.NullString
	err := r.Scan(&m.ID, &m.EventKind, &m.Source, &m.ProfileRef, &m.AccountRef, &m.EventID, &m.Target, &m.PayloadVersion, &m.PayloadJSON, &m.Status, &m.Attempts, &a, &m.LeaseToken, &ln, &dn, &m.ProviderMessageID, &m.LastError, &ddn, &c, &u)
	if err != nil {
		return m, err
	}
	m.AvailableAt = parseDBTime(a)
	m.CreatedAt = parseDBTime(c)
	m.UpdatedAt = parseDBTime(u)
	m.LeaseExpiresAt = parseNullableTime(ln)
	m.DeliveredAt = parseNullableTime(dn)
	m.DeadLetteredAt = parseNullableTime(ddn)
	return m, nil
}
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func changed(r sql.Result, e error) (bool, error) {
	if e != nil {
		return false, e
	}
	n, e := r.RowsAffected()
	return n == 1, e
}

func enqueueEvent(ctx context.Context, tx *sql.Tx, kind, id, account, target string, event any) error {
	if target == "" {
		return nil
	}
	payload, err := EncodeOutboxPayload(kind, event)
	if err != nil {
		return err
	}
	return EnqueueOutbox(ctx, tx, OutboxEnqueue{EventKind: kind, Source: "scriba-v7", AccountRef: account, EventID: id, Target: target, PayloadVersion: 1, PayloadJSON: payload}, time.Now())
}

func firstTarget(targets []string) string {
	if len(targets) == 0 {
		return ""
	}
	return targets[0]
}

// EncodeOutboxPayload returns the stable v1 wire representation. Maps are used
// deliberately: encoding/json sorts their keys, making retries byte-identical.
func EncodeOutboxPayload(kind string, event any) (string, error) {
	m := map[string]any{"version": 1, "kind": kind}
	account := func(a resetwatch.Account) {
		m["account_label"], m["account_email"], m["account_plan"] = a.Label, a.Email, a.Plan
	}
	raw := func(b []byte) (json.RawMessage, error) {
		var v json.RawMessage
		if !json.Valid(b) {
			return nil, errors.New("invalid event snapshot JSON")
		}
		v = append(v, b...)
		return v, nil
	}
	switch e := event.(type) {
	case resetwatch.Event:
		account(e.Account)
		p, err := raw(e.PreviousSnapshotJSON)
		if err != nil {
			return "", err
		}
		c, err := raw(e.CurrentSnapshotJSON)
		if err != nil {
			return "", err
		}
		m["primary_trigger_label"], m["secondary_trigger_labels"], m["reset_kind"], m["previous_reset_at"], m["current_reset_at"], m["previous_snapshot"], m["current_snapshot"], m["joke_id"], m["detected_at"] = e.PrimaryTriggerLabel, e.SecondaryTriggerLabels, e.ResetKind, formatTime(e.PreviousResetAt), formatTime(e.CurrentResetAt), p, c, e.JokeID, formatTime(e.DetectedAt)
	case resetwatch.WarningEvent:
		account(e.Account)
		s, err := raw(e.SnapshotJSON)
		if err != nil {
			return "", err
		}
		m["label"], m["threshold_remaining"], m["used_percent"], m["remaining_percent"], m["reset_at"], m["snapshot"], m["detected_at"] = e.Label, e.ThresholdRemaining, e.UsedPercent, e.RemainingPercent, formatTime(e.ResetAt), s, formatTime(e.DetectedAt)
	case resetwatch.GrantExpiryWarning:
		account(e.Account)
		s, err := raw(e.SnapshotJSON)
		if err != nil {
			return "", err
		}
		m["credit_id"], m["credit_title"], m["threshold_days"], m["expires_at"], m["snapshot"], m["detected_at"] = e.CreditID, e.CreditTitle, e.ThresholdDays, formatTime(e.ExpiresAt), s, formatTime(e.DetectedAt)
	case resetwatch.ResetGrantEvent:
		account(e.Account)
		s, err := raw(e.SnapshotJSON)
		if err != nil {
			return "", err
		}
		m["credit_id"], m["credit_title"], m["reset_type"], m["granted_at"], m["expires_at"], m["available_count"], m["snapshot"], m["detected_at"] = e.CreditID, e.CreditTitle, e.ResetType, formatTime(e.GrantedAt), formatTime(e.ExpiresAt), e.AvailableCount, s, formatTime(e.DetectedAt)
	case radar.ProbabilityAlert:
		s, err := raw(e.SnapshotJSON)
		if err != nil {
			return "", err
		}
		m["milestone"], m["probability_24h"], m["probability_48h"], m["level"], m["expected_window"], m["reasoning_summary"], m["checked_at"], m["detected_at"], m["snapshot"] = e.Milestone, e.Probability24H, e.Probability48H, e.Level, e.ExpectedWindow, e.ReasoningSummary, e.CheckedAt, formatTime(e.DetectedAt), s
	default:
		return "", fmt.Errorf("unsupported outbox payload %T", event)
	}
	b, err := json.Marshal(m)
	return string(b), err
}

func DecodeOutboxPayload(message OutboxMessage) (any, error) {
	if message.PayloadVersion != 1 {
		return nil, errors.New("unsupported outbox payload version")
	}
	var v map[string]any
	d := json.NewDecoder(strings.NewReader(message.PayloadJSON))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if v["version"] != json.Number("1") {
		return nil, errors.New("unsupported outbox payload version")
	}
	kind, ok := v["kind"].(string)
	if !ok || kind == "" {
		return nil, errors.New("invalid outbox payload kind")
	}
	if kind != message.EventKind {
		return nil, errors.New("outbox payload kind mismatch")
	}
	str := func(k string) string { x, _ := v[k].(string); return x }
	num := func(k string) float64 { x, _ := v[k].(json.Number); f, _ := x.Float64(); return f }
	integer := func(k string) int { return int(num(k)) }
	snapshot := func(k string) []byte { b, _ := json.Marshal(v[k]); return b }
	account := resetwatch.Account{Label: str("account_label"), Email: str("account_email"), Plan: str("account_plan"), Ref: message.AccountRef}
	switch kind {
	case "reset":
		var labels []string
		if b, err := json.Marshal(v["secondary_trigger_labels"]); err == nil {
			_ = json.Unmarshal(b, &labels)
		}
		return resetwatch.Event{ID: message.EventID, Account: account, PrimaryTriggerLabel: str("primary_trigger_label"), SecondaryTriggerLabels: labels, ResetKind: str("reset_kind"), PreviousResetAt: parseDBTime(str("previous_reset_at")), CurrentResetAt: parseDBTime(str("current_reset_at")), PreviousSnapshotJSON: snapshot("previous_snapshot"), CurrentSnapshotJSON: snapshot("current_snapshot"), JokeID: str("joke_id"), DetectedAt: parseDBTime(str("detected_at"))}, nil
	case "limit_warning":
		return resetwatch.WarningEvent{ID: message.EventID, Account: account, Label: str("label"), ThresholdRemaining: integer("threshold_remaining"), UsedPercent: num("used_percent"), RemainingPercent: num("remaining_percent"), ResetAt: parseDBTime(str("reset_at")), SnapshotJSON: snapshot("snapshot"), DetectedAt: parseDBTime(str("detected_at"))}, nil
	case "reset_grant_warning":
		return resetwatch.GrantExpiryWarning{ID: message.EventID, Account: account, CreditID: str("credit_id"), CreditTitle: str("credit_title"), ThresholdDays: integer("threshold_days"), ExpiresAt: parseDBTime(str("expires_at")), SnapshotJSON: snapshot("snapshot"), DetectedAt: parseDBTime(str("detected_at"))}, nil
	case "reset_grant":
		return resetwatch.ResetGrantEvent{ID: message.EventID, Account: account, CreditID: str("credit_id"), CreditTitle: str("credit_title"), ResetType: str("reset_type"), GrantedAt: parseDBTime(str("granted_at")), ExpiresAt: parseDBTime(str("expires_at")), AvailableCount: integer("available_count"), SnapshotJSON: snapshot("snapshot"), DetectedAt: parseDBTime(str("detected_at"))}, nil
	case "radar_alert":
		return radar.ProbabilityAlert{ID: message.EventID, Milestone: integer("milestone"), Probability24H: num("probability_24h"), Probability48H: num("probability_48h"), Level: str("level"), ExpectedWindow: str("expected_window"), ReasoningSummary: str("reasoning_summary"), CheckedAt: str("checked_at"), DetectedAt: parseDBTime(str("detected_at")), SnapshotJSON: snapshot("snapshot")}, nil
	default:
		return nil, fmt.Errorf("unsupported outbox event kind %q", kind)
	}
}
