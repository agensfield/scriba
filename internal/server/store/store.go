package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/resetwatch"
	_ "modernc.org/sqlite"
)

const SchemaVersion = 2

type Store struct {
	db   *sql.DB
	path string
}

type Delivery struct {
	ID                string
	EventID           string
	Target            string
	Status            string
	Attempts          int
	LastAttemptAt     *time.Time
	NextAttemptAt     *time.Time
	DeliveredAt       *time.Time
	ProviderMessageID string
	LastError         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type PruneResult struct {
	Cutoff              time.Time
	DeletedObservations int64
	DeletedWindows      int64
	Checkpointed        bool
	Vacuumed            bool
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("store path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db, path: path}
	if err := store.Migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `pragma busy_timeout = 5000;`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `pragma foreign_keys = on;`); err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx, `pragma journal_mode = wal;`)
	_, err := s.db.ExecContext(ctx, schemaSQL)
	if err != nil {
		return err
	}
	if err := s.migrateNotificationDeliveries(ctx); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
insert into schema_migrations (version, applied_at)
values (?, ?)
on conflict(version) do nothing`, SchemaVersion, formatTime(time.Now()))
	return err
}

func (s *Store) migrateNotificationDeliveries(ctx context.Context) error {
	columns, err := s.tableColumns(ctx, "notification_deliveries")
	if err != nil {
		return err
	}
	for _, migration := range []struct {
		name string
		sql  string
	}{
		{name: "next_attempt_at", sql: `alter table notification_deliveries add column next_attempt_at text`},
		{name: "provider_message_id", sql: `alter table notification_deliveries add column provider_message_id text`},
	} {
		if !columns[migration.name] {
			if _, err := s.db.ExecContext(ctx, migration.sql); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) tableColumns(ctx context.Context, table string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `pragma table_info(`+table+`)`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx, `select max(version) from schema_migrations`).Scan(&version)
	return version, err
}

func (s *Store) ApplyDecision(ctx context.Context, obs resetwatch.Observation, decision resetwatch.Decision) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := upsertAccount(ctx, tx, obs); err != nil {
		return 0, err
	}
	if err := saveObservation(ctx, tx, obs); err != nil {
		return 0, err
	}
	if err := upsertWindowStates(ctx, tx, decision.States); err != nil {
		return 0, err
	}
	inserted, err := insertResetEvents(ctx, tx, decision.Events)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

func (s *Store) LoadWindowStates(ctx context.Context, accountRef string) (map[string]resetwatch.WindowState, error) {
	rows, err := s.db.QueryContext(ctx, `
select account_ref, label, stable_reset_at, last_seen_reset_at, last_observed_at, last_snapshot_json
from limit_windows
where account_ref = ?`, accountRef)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	states := map[string]resetwatch.WindowState{}
	for rows.Next() {
		var state resetwatch.WindowState
		var stable, seen, observed, snapshot string
		if err := rows.Scan(&state.AccountRef, &state.Label, &stable, &seen, &observed, &snapshot); err != nil {
			return nil, err
		}
		state.StableResetAt = parseDBTime(stable)
		state.LastSeenResetAt = parseDBTime(seen)
		state.LastObservedAt = parseDBTime(observed)
		state.LastSnapshotJSON = []byte(snapshot)
		states[resetwatch.StateKey(state.AccountRef, state.Label)] = state
	}
	return states, rows.Err()
}

func (s *Store) InsertResetEvents(ctx context.Context, events []resetwatch.Event) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	inserted, err := insertResetEvents(ctx, tx, events)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

func (s *Store) LoadResetEvent(ctx context.Context, id string) (resetwatch.Event, bool, error) {
	var event resetwatch.Event
	var secondaryJSON, prev, current, detected, prevSnapshot, currentSnapshot string
	err := s.db.QueryRowContext(ctx, `
select id, provider_id, account_ref, account_label, account_email, account_plan,
  primary_trigger_label, secondary_trigger_labels_json, reset_kind,
  previous_reset_at, current_reset_at, previous_snapshot_json, current_snapshot_json,
  joke_id, detected_at
from reset_events
where id = ?`, id).Scan(
		&event.ID, &event.ProviderID, &event.Account.Ref, &event.Account.Label, &event.Account.Email, &event.Account.Plan,
		&event.PrimaryTriggerLabel, &secondaryJSON, &event.ResetKind,
		&prev, &current, &prevSnapshot, &currentSnapshot,
		&event.JokeID, &detected,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return event, false, nil
	}
	if err != nil {
		return event, false, err
	}
	_ = json.Unmarshal([]byte(secondaryJSON), &event.SecondaryTriggerLabels)
	event.PreviousResetAt = parseDBTime(prev)
	event.CurrentResetAt = parseDBTime(current)
	event.PreviousSnapshotJSON = []byte(prevSnapshot)
	event.CurrentSnapshotJSON = []byte(currentSnapshot)
	event.DetectedAt = parseDBTime(detected)
	return event, true, nil
}

func (s *Store) LoadLastResetEvent(ctx context.Context) (resetwatch.Event, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `select id from reset_events order by detected_at desc limit 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return resetwatch.Event{}, false, nil
	}
	if err != nil {
		return resetwatch.Event{}, false, err
	}
	return s.LoadResetEvent(ctx, id)
}

func (s *Store) LoadLatestObservation(ctx context.Context) (resetwatch.Observation, bool, error) {
	var obs resetwatch.Observation
	var observationID, observedAt, snapshot string
	err := s.db.QueryRowContext(ctx, `
select o.id, o.provider_id, o.account_ref, a.label, a.email, a.plan, o.observed_at, o.snapshot_json
from limit_observations o
join accounts a on a.account_ref = o.account_ref
order by o.observed_at desc, o.created_at desc
limit 1`).Scan(
		&observationID, &obs.ProviderID, &obs.Account.Ref, &obs.Account.Label, &obs.Account.Email, &obs.Account.Plan, &observedAt, &snapshot,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return obs, false, nil
	}
	if err != nil {
		return obs, false, err
	}
	obs.ObservedAt = parseDBTime(observedAt)
	obs.SnapshotJSON = []byte(snapshot)
	windows, err := s.loadObservedWindows(ctx, observationID)
	if err != nil {
		return obs, false, err
	}
	obs.Windows = windows
	return obs, true, nil
}

func (s *Store) PruneObservations(ctx context.Context, cutoff time.Time, compact bool) (PruneResult, error) {
	result := PruneResult{Cutoff: cutoff.UTC()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	cutoffValue := formatTime(cutoff)
	if err := tx.QueryRowContext(ctx, `
select count(*)
from observed_windows
where observation_id in (select id from limit_observations where observed_at < ?)`, cutoffValue).Scan(&result.DeletedWindows); err != nil {
		return result, err
	}
	deleted, err := tx.ExecContext(ctx, `delete from limit_observations where observed_at < ?`, cutoffValue)
	if err != nil {
		return result, err
	}
	result.DeletedObservations, _ = deleted.RowsAffected()
	if err := tx.Commit(); err != nil {
		return result, err
	}
	if compact && (result.DeletedObservations > 0 || result.DeletedWindows > 0) {
		if err := s.Checkpoint(ctx); err != nil {
			return result, err
		}
		result.Checkpointed = true
		if err := s.Vacuum(ctx); err != nil {
			return result, err
		}
		result.Vacuumed = true
	}
	return result, nil
}

func (s *Store) Checkpoint(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `pragma wal_checkpoint(truncate)`)
	return err
}

func (s *Store) Vacuum(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `vacuum`)
	return err
}

func (s *Store) loadObservedWindows(ctx context.Context, observationID string) ([]resetwatch.Window, error) {
	rows, err := s.db.QueryContext(ctx, `
select label, used_percent, reset_at, period_duration_ms
from observed_windows
where observation_id = ?
order by label`, observationID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var windows []resetwatch.Window
	for rows.Next() {
		var window resetwatch.Window
		var used sql.NullFloat64
		var resetAt string
		var period sql.NullInt64
		if err := rows.Scan(&window.Label, &used, &resetAt, &period); err != nil {
			return nil, err
		}
		if used.Valid {
			window.UsedPercent = &used.Float64
		}
		window.ResetAt = parseDBTime(resetAt)
		if period.Valid {
			window.PeriodDurationMs = &period.Int64
		}
		windows = append(windows, window)
	}
	return windows, rows.Err()
}

func (s *Store) EnsureDelivery(ctx context.Context, eventID, target string) (Delivery, error) {
	now := formatTime(time.Now())
	id := DeliveryID(eventID, target)
	_, err := s.db.ExecContext(ctx, `
insert into notification_deliveries (id, event_id, target, status, attempts, created_at, updated_at)
values (?, ?, ?, 'pending', 0, ?, ?)
on conflict(event_id, target) do nothing`, id, eventID, target, now, now)
	if err != nil {
		return Delivery{}, err
	}
	delivery, ok, err := s.LoadDelivery(ctx, eventID, target)
	if err != nil {
		return Delivery{}, err
	}
	if !ok {
		return Delivery{}, errors.New("delivery not found after insert")
	}
	return delivery, nil
}

func (s *Store) LoadDelivery(ctx context.Context, eventID, target string) (Delivery, bool, error) {
	return scanDelivery(s.db.QueryRowContext(ctx, `
select id, event_id, target, status, attempts, last_attempt_at, next_attempt_at, delivered_at, provider_message_id, last_error, created_at, updated_at
from notification_deliveries
where event_id = ? and target = ?`, eventID, target))
}

func (s *Store) PendingDeliveries(ctx context.Context, target string, limit int) ([]Delivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
select id, event_id, target, status, attempts, last_attempt_at, next_attempt_at, delivered_at, provider_message_id, last_error, created_at, updated_at
from notification_deliveries
where target = ? and status != 'delivered' and (next_attempt_at is null or next_attempt_at = '' or next_attempt_at <= ?)
order by coalesce(next_attempt_at, created_at), created_at
limit ?`, target, formatTime(time.Now()), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var deliveries []Delivery
	for rows.Next() {
		delivery, err := scanDeliveryRows(rows)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

func (s *Store) MarkDeliveryAttempt(ctx context.Context, eventID, target string, delivered bool, message string, providerMessageID string) error {
	now := formatTime(time.Now())
	status := "failed"
	var deliveredAt any
	var nextAttemptAt any
	var lastError any = message
	if delivered {
		status = "delivered"
		deliveredAt = now
		nextAttemptAt = nil
		lastError = nil
	} else {
		var attempts int
		_ = s.db.QueryRowContext(ctx, `select attempts from notification_deliveries where event_id = ? and target = ?`, eventID, target).Scan(&attempts)
		nextAttemptAt = formatTime(time.Now().Add(deliveryBackoff(attempts + 1)))
	}
	_, err := s.db.ExecContext(ctx, `
update notification_deliveries
set status = ?, attempts = attempts + 1, last_attempt_at = ?, next_attempt_at = ?, delivered_at = coalesce(?, delivered_at), provider_message_id = coalesce(nullif(?, ''), provider_message_id), last_error = ?, updated_at = ?
where event_id = ? and target = ?`, status, now, nextAttemptAt, deliveredAt, providerMessageID, lastError, now, eventID, target)
	return err
}

func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `select value from server_settings where key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
insert into server_settings (key, value, updated_at)
values (?, ?, ?)
on conflict(key) do update set value = excluded.value, updated_at = excluded.updated_at`, key, value, formatTime(time.Now()))
	return err
}

func (s *Store) GetTelegramOffset(ctx context.Context, botRef string) (int64, bool, error) {
	var value int64
	err := s.db.QueryRowContext(ctx, `select last_update_id from telegram_offsets where bot_ref = ?`, botRef).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return value, err == nil, err
}

func (s *Store) SetTelegramOffset(ctx context.Context, botRef string, updateID int64) error {
	_, err := s.db.ExecContext(ctx, `
insert into telegram_offsets (bot_ref, last_update_id, updated_at)
values (?, ?, ?)
on conflict(bot_ref) do update set last_update_id = excluded.last_update_id, updated_at = excluded.updated_at`, botRef, updateID, formatTime(time.Now()))
	return err
}

func ObservationID(obs resetwatch.Observation) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		providerID(obs.ProviderID),
		obs.Account.Ref,
		obs.ObservedAt.UTC().Format(time.RFC3339Nano),
		string(obs.SnapshotJSON),
	}, "\x00")))
	return "obs_" + hex.EncodeToString(sum[:16])
}

func DeliveryID(eventID, target string) string {
	sum := sha256.Sum256([]byte(eventID + "\x00" + target))
	return "delivery_" + hex.EncodeToString(sum[:16])
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDelivery(row rowScanner) (Delivery, bool, error) {
	delivery, err := scanDeliveryValues(row)
	if errors.Is(err, sql.ErrNoRows) {
		return delivery, false, nil
	}
	return delivery, err == nil, err
}

func scanDeliveryRows(rows *sql.Rows) (Delivery, error) {
	return scanDeliveryValues(rows)
}

func scanDeliveryValues(row rowScanner) (Delivery, error) {
	var delivery Delivery
	var lastAttempt, nextAttempt, delivered, providerMessageID, created, updated sql.NullString
	var lastError sql.NullString
	err := row.Scan(
		&delivery.ID, &delivery.EventID, &delivery.Target, &delivery.Status, &delivery.Attempts,
		&lastAttempt, &nextAttempt, &delivered, &providerMessageID, &lastError, &created, &updated,
	)
	if err != nil {
		return delivery, err
	}
	delivery.LastAttemptAt = parseNullableTime(lastAttempt)
	delivery.NextAttemptAt = parseNullableTime(nextAttempt)
	delivery.DeliveredAt = parseNullableTime(delivered)
	if providerMessageID.Valid {
		delivery.ProviderMessageID = providerMessageID.String
	}
	if lastError.Valid {
		delivery.LastError = lastError.String
	}
	delivery.CreatedAt = parseDBTime(created.String)
	delivery.UpdatedAt = parseDBTime(updated.String)
	return delivery, nil
}

func deliveryBackoff(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return 30 * time.Second
	case attempts == 2:
		return 2 * time.Minute
	case attempts == 3:
		return 5 * time.Minute
	case attempts == 4:
		return 15 * time.Minute
	default:
		return time.Hour
	}
}

func upsertAccount(ctx context.Context, tx *sql.Tx, obs resetwatch.Observation) error {
	_, err := tx.ExecContext(ctx, `
insert into accounts (account_ref, provider_id, label, email, plan, updated_at)
values (?, ?, ?, ?, ?, ?)
on conflict(account_ref) do update set
  provider_id = excluded.provider_id,
  label = excluded.label,
  email = excluded.email,
  plan = excluded.plan,
  updated_at = excluded.updated_at`,
		obs.Account.Ref, providerID(obs.ProviderID), obs.Account.Label, obs.Account.Email, obs.Account.Plan, formatTime(time.Now()))
	return err
}

func saveObservation(ctx context.Context, tx *sql.Tx, obs resetwatch.Observation) error {
	id := ObservationID(obs)
	_, err := tx.ExecContext(ctx, `
insert into limit_observations (id, provider_id, account_ref, observed_at, snapshot_json, created_at)
values (?, ?, ?, ?, ?, ?)
on conflict(id) do nothing`, id, providerID(obs.ProviderID), obs.Account.Ref, formatTime(obs.ObservedAt), string(obs.SnapshotJSON), formatTime(time.Now()))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `delete from observed_windows where observation_id = ?`, id)
	if err != nil {
		return err
	}
	for _, window := range obs.Windows {
		_, err := tx.ExecContext(ctx, `
insert into observed_windows (observation_id, label, used_percent, reset_at, period_duration_ms)
values (?, ?, ?, ?, ?)`, id, window.Label, nullableFloat(window.UsedPercent), formatTime(window.ResetAt), nullableInt64(window.PeriodDurationMs))
		if err != nil {
			return err
		}
	}
	return nil
}

func upsertWindowStates(ctx context.Context, tx *sql.Tx, states []resetwatch.WindowState) error {
	now := formatTime(time.Now())
	for _, state := range states {
		_, err := tx.ExecContext(ctx, `
insert into limit_windows (account_ref, label, stable_reset_at, last_seen_reset_at, last_observed_at, last_snapshot_json, updated_at)
values (?, ?, ?, ?, ?, ?, ?)
on conflict(account_ref, label) do update set
  stable_reset_at = excluded.stable_reset_at,
  last_seen_reset_at = excluded.last_seen_reset_at,
  last_observed_at = excluded.last_observed_at,
  last_snapshot_json = excluded.last_snapshot_json,
  updated_at = excluded.updated_at`,
			state.AccountRef, state.Label, formatTime(state.StableResetAt), formatTime(state.LastSeenResetAt), formatTime(state.LastObservedAt), string(state.LastSnapshotJSON), now)
		if err != nil {
			return err
		}
	}
	return nil
}

func insertResetEvents(ctx context.Context, tx *sql.Tx, events []resetwatch.Event) (int, error) {
	now := formatTime(time.Now())
	inserted := 0
	for _, event := range events {
		if err := upsertEventAccount(ctx, tx, event); err != nil {
			return inserted, err
		}
		secondaryJSON, err := json.Marshal(event.SecondaryTriggerLabels)
		if err != nil {
			return inserted, err
		}
		result, err := tx.ExecContext(ctx, `
insert into reset_events (
  id, provider_id, account_ref, account_label, account_email, account_plan,
  primary_trigger_label, secondary_trigger_labels_json, reset_kind,
  previous_reset_at, current_reset_at, previous_snapshot_json, current_snapshot_json,
  joke_id, detected_at, created_at
) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
on conflict(id) do nothing`,
			event.ID, providerID(event.ProviderID), event.Account.Ref, event.Account.Label, event.Account.Email, event.Account.Plan,
			event.PrimaryTriggerLabel, string(secondaryJSON), event.ResetKind,
			formatTime(event.PreviousResetAt), formatTime(event.CurrentResetAt), string(event.PreviousSnapshotJSON), string(event.CurrentSnapshotJSON),
			event.JokeID, formatTime(event.DetectedAt), now)
		if err != nil {
			return inserted, err
		}
		if affected, _ := result.RowsAffected(); affected > 0 {
			inserted++
		}
	}
	return inserted, nil
}

func upsertEventAccount(ctx context.Context, tx *sql.Tx, event resetwatch.Event) error {
	_, err := tx.ExecContext(ctx, `
insert into accounts (account_ref, provider_id, label, email, plan, updated_at)
values (?, ?, ?, ?, ?, ?)
on conflict(account_ref) do update set
  provider_id = excluded.provider_id,
  label = excluded.label,
  email = excluded.email,
  plan = excluded.plan,
  updated_at = excluded.updated_at`,
		event.Account.Ref, providerID(event.ProviderID), event.Account.Label, event.Account.Email, event.Account.Plan, formatTime(time.Now()))
	return err
}

func providerID(input string) string {
	if input == "" {
		return resetwatch.ProviderCodex
	}
	return input
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseDBTime(input string) time.Time {
	if input == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, input)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, input)
	}
	return t.UTC()
}

func parseNullableTime(input sql.NullString) *time.Time {
	if !input.Valid || input.String == "" {
		return nil
	}
	t := parseDBTime(input.String)
	return &t
}

func nullableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
