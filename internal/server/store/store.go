package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/radar"
	"github.com/agensfield/scriba/internal/resetwatch"
	_ "modernc.org/sqlite"
)

const SchemaVersion = 11

const deliverySendLease = 10 * time.Minute

const maxOpenConnections = 4

type Store struct {
	db                    *sql.DB
	path                  string
	applyCodexPollFault   func(string) error
	loadPolicyReplayFault func(string) error
	profileMigrationFault func(string) error
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
	DeletedEvents       int64
	DeletedDeliveries   int64
	DeletedReplayRows   int64
	DeletedInboxRows    int64
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
	if _, err := os.Stat(path); err == nil {
		if err := checkSchemaCompatibilityPath(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := secureSQLiteFile(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteDSN(path, ""))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxOpenConnections)
	store := &Store{db: db, path: path}
	if err := store.Migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func sqliteDSN(path, extraQuery string) string {
	u := &url.URL{Scheme: "file", Path: path}
	query := u.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Set("_txlock", "immediate")
	if extraQuery != "" {
		extra, _ := url.ParseQuery(extraQuery)
		for key, values := range extra {
			for _, value := range values {
				query.Add(key, value)
			}
		}
	}
	u.RawQuery = query.Encode()
	return u.String()
}

func sqliteReadOnlyDSN(path string) string {
	u := &url.URL{Scheme: "file", Path: path}
	query := u.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "query_only(1)")
	u.RawQuery = query.Encode()
	return u.String()
}

// OpenExisting opens an existing server database without creating directories
// or running migrations. It is intended for pre-upgrade backup operations.
func OpenExisting(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("store path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("store path is not a regular file: %s", path)
	}
	dsn := sqliteDSN(path, "mode=rw")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxOpenConnections)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := checkSchemaCompatibility(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, path: path}, nil
}

func checkSchemaCompatibilityPath(path string) error {
	db, err := sql.Open("sqlite", sqliteDSN(path, "mode=ro"))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return checkSchemaCompatibility(context.Background(), db)
}

func checkSchemaCompatibility(ctx context.Context, db *sql.DB) error {
	var exists int
	if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table' and name = 'schema_migrations'`).Scan(&exists); err != nil {
		return fmt.Errorf("inspect schema version: %w", err)
	}
	if exists == 0 {
		return nil
	}
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, `select max(version) from schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("inspect schema version: %w", err)
	}
	if version.Valid && version.Int64 > SchemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version.Int64, SchemaVersion)
	}
	return nil
}

func secureSQLiteFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- caller-selected local state path.
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Migrate(ctx context.Context) error {
	if err := checkSchemaCompatibility(ctx, s.db); err != nil {
		return err
	}
	var journalMode string
	if err := s.db.QueryRowContext(ctx, `pragma journal_mode = wal;`).Scan(&journalMode); err != nil {
		return fmt.Errorf("enable WAL: %w", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("enable WAL: journal mode is %q", journalMode)
	}
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
on conflict(version) do nothing`, 6, formatTime(time.Now()))
	if err != nil {
		return err
	}
	if err := s.migrateNotificationOutbox(ctx); err != nil {
		return err
	}
	if err := s.migratePolicy(ctx); err != nil {
		return err
	}
	if err := s.migratePolicyEventReplay(ctx); err != nil {
		return err
	}
	if err := s.migrateProfiles(ctx); err != nil {
		return err
	}
	return s.ensureRetentionIndexes(ctx)
}

func (s *Store) ensureRetentionIndexes(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
create index if not exists idx_limit_observations_retention on limit_observations(observed_at);
create index if not exists idx_notification_outbox_retention on notification_outbox(status,updated_at);
create index if not exists idx_telegram_updates_retention on telegram_updates(status,updated_at);
create index if not exists idx_reset_events_retention on reset_events(detected_at);
create index if not exists idx_limit_warning_events_retention on limit_warning_events(detected_at);
create index if not exists idx_reset_grant_warning_events_retention on reset_grant_warning_events(detected_at);
create index if not exists idx_reset_grant_events_retention on reset_grant_events(detected_at);
create index if not exists idx_radar_alert_events_retention on radar_alert_events(detected_at);
create index if not exists idx_policy_events_retention on policy_events(detected_at);`)
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

func (s *Store) ApplyDecision(ctx context.Context, obs resetwatch.Observation, decision resetwatch.Decision, targets ...string) (int, error) {
	targets, err := notificationTargets("", targets)
	if err != nil {
		return 0, err
	}
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
	inserted, err := insertResetEvents(ctx, tx, decision.Events, targets)
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

func (s *Store) InsertResetEvents(ctx context.Context, events []resetwatch.Event, targets ...string) (int, error) {
	targets, err := notificationTargets("", targets)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	inserted, err := insertResetEvents(ctx, tx, events, targets)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

func (s *Store) InsertWarningEvents(ctx context.Context, warnings []resetwatch.WarningEvent, targets ...string) ([]resetwatch.WarningEvent, error) {
	targets, err := notificationTargets("", targets)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	inserted := make([]resetwatch.WarningEvent, 0, len(warnings))
	for _, warning := range warnings {
		if err := upsertWarningAccount(ctx, tx, warning); err != nil {
			return nil, err
		}
		result, err := tx.ExecContext(ctx, `
insert into limit_warning_events (
  id, provider_id, account_ref, account_label, account_email, account_plan,
  label, threshold_remaining, used_percent, remaining_percent, reset_at,
  snapshot_json, detected_at, created_at
) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
on conflict(id) do nothing`,
			warning.ID, providerID(warning.ProviderID), warning.Account.Ref, warning.Account.Label, warning.Account.Email, warning.Account.Plan,
			warning.Label, warning.ThresholdRemaining, warning.UsedPercent, warning.RemainingPercent, formatTime(warning.ResetAt),
			string(warning.SnapshotJSON), formatTime(warning.DetectedAt), formatTime(time.Now()))
		if err != nil {
			return nil, err
		}
		if affected, _ := result.RowsAffected(); affected > 0 {
			if err := enqueueEventTargets(ctx, tx, "limit_warning", warning.ID, warning.Account.Ref, targets, warning); err != nil {
				return nil, err
			}
			inserted = append(inserted, warning)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return inserted, nil
}

func (s *Store) LoadWarningEvent(ctx context.Context, id string) (resetwatch.WarningEvent, bool, error) {
	var warning resetwatch.WarningEvent
	var resetAt, snapshot, detected string
	err := s.db.QueryRowContext(ctx, `
select id, provider_id, account_ref, account_label, account_email, account_plan,
  label, threshold_remaining, used_percent, remaining_percent, reset_at,
  snapshot_json, detected_at
from limit_warning_events
where id = ?`, id).Scan(
		&warning.ID, &warning.ProviderID, &warning.Account.Ref, &warning.Account.Label, &warning.Account.Email, &warning.Account.Plan,
		&warning.Label, &warning.ThresholdRemaining, &warning.UsedPercent, &warning.RemainingPercent, &resetAt,
		&snapshot, &detected,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return warning, false, nil
	}
	if err != nil {
		return warning, false, err
	}
	warning.ResetAt = parseDBTime(resetAt)
	warning.SnapshotJSON = []byte(snapshot)
	warning.DetectedAt = parseDBTime(detected)
	return warning, true, nil
}

func (s *Store) InsertGrantExpiryWarningEvents(ctx context.Context, warnings []resetwatch.GrantExpiryWarning, targets ...string) ([]resetwatch.GrantExpiryWarning, error) {
	targets, err := notificationTargets("", targets)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	inserted := make([]resetwatch.GrantExpiryWarning, 0, len(warnings))
	for _, warning := range warnings {
		if err := upsertGrantWarningAccount(ctx, tx, warning); err != nil {
			return nil, err
		}
		result, err := tx.ExecContext(ctx, `
insert into reset_grant_warning_events (
  id, provider_id, account_ref, account_label, account_email, account_plan,
  credit_id, credit_title, threshold_days, expires_at, snapshot_json, detected_at, created_at
) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
on conflict(id) do nothing`,
			warning.ID, providerID(warning.ProviderID), warning.Account.Ref, warning.Account.Label, warning.Account.Email, warning.Account.Plan,
			warning.CreditID, warning.CreditTitle, warning.ThresholdDays, formatTime(warning.ExpiresAt),
			string(warning.SnapshotJSON), formatTime(warning.DetectedAt), formatTime(time.Now()))
		if err != nil {
			return nil, err
		}
		if affected, _ := result.RowsAffected(); affected > 0 {
			if err := enqueueEventTargets(ctx, tx, "reset_grant_warning", warning.ID, warning.Account.Ref, targets, warning); err != nil {
				return nil, err
			}
			inserted = append(inserted, warning)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return inserted, nil
}

func (s *Store) LoadGrantExpiryWarningEvent(ctx context.Context, id string) (resetwatch.GrantExpiryWarning, bool, error) {
	var warning resetwatch.GrantExpiryWarning
	var expiresAt, snapshot, detected string
	err := s.db.QueryRowContext(ctx, `
select id, provider_id, account_ref, account_label, account_email, account_plan,
  credit_id, credit_title, threshold_days, expires_at, snapshot_json, detected_at
from reset_grant_warning_events
where id = ?`, id).Scan(
		&warning.ID, &warning.ProviderID, &warning.Account.Ref, &warning.Account.Label, &warning.Account.Email, &warning.Account.Plan,
		&warning.CreditID, &warning.CreditTitle, &warning.ThresholdDays, &expiresAt, &snapshot, &detected,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return warning, false, nil
	}
	if err != nil {
		return warning, false, err
	}
	warning.ExpiresAt = parseDBTime(expiresAt)
	warning.SnapshotJSON = []byte(snapshot)
	warning.DetectedAt = parseDBTime(detected)
	return warning, true, nil
}

func (s *Store) InsertResetGrantEvents(ctx context.Context, obs resetwatch.Observation, events []resetwatch.ResetGrantEvent, targets ...string) ([]resetwatch.ResetGrantEvent, error) {
	targets, err := notificationTargets("", targets)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := upsertAccount(ctx, tx, obs); err != nil {
		return nil, err
	}
	trackExisting, previousCount, err := resetGrantTrackingState(ctx, tx, obs.Account.Ref)
	if err != nil {
		return nil, err
	}
	availableCount := 0
	if obs.ResetGrants.AvailableCount != nil {
		availableCount = *obs.ResetGrants.AvailableCount
	} else {
		availableCount = len(events)
	}
	inserted := make([]resetwatch.ResetGrantEvent, 0, len(events))
	for _, event := range events {
		result, err := tx.ExecContext(ctx, `
insert into reset_grant_events (
  id, provider_id, account_ref, account_label, account_email, account_plan,
  credit_id, credit_title, reset_type, granted_at, expires_at, available_count,
  snapshot_json, detected_at, created_at
) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
on conflict(id) do nothing`,
			event.ID, providerID(event.ProviderID), event.Account.Ref, event.Account.Label, event.Account.Email, event.Account.Plan,
			event.CreditID, event.CreditTitle, event.ResetType, formatTime(event.GrantedAt), formatTime(event.ExpiresAt),
			event.AvailableCount, string(event.SnapshotJSON), formatTime(event.DetectedAt), formatTime(time.Now()))
		if err != nil {
			return nil, err
		}
		if affected, _ := result.RowsAffected(); affected > 0 && trackExisting && availableCount > previousCount {
			if err := enqueueEventTargets(ctx, tx, "reset_grant", event.ID, event.Account.Ref, targets, event); err != nil {
				return nil, err
			}
			inserted = append(inserted, event)
		}
	}
	if err := upsertResetGrantTrackingState(ctx, tx, obs, availableCount); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return inserted, nil
}

func (s *Store) LoadResetGrantEvent(ctx context.Context, id string) (resetwatch.ResetGrantEvent, bool, error) {
	var event resetwatch.ResetGrantEvent
	var grantedAt, expiresAt, snapshot, detected string
	err := s.db.QueryRowContext(ctx, `
select id, provider_id, account_ref, account_label, account_email, account_plan,
  credit_id, credit_title, reset_type, granted_at, expires_at, available_count,
  snapshot_json, detected_at
from reset_grant_events
where id = ?`, id).Scan(
		&event.ID, &event.ProviderID, &event.Account.Ref, &event.Account.Label, &event.Account.Email, &event.Account.Plan,
		&event.CreditID, &event.CreditTitle, &event.ResetType, &grantedAt, &expiresAt, &event.AvailableCount,
		&snapshot, &detected,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return event, false, nil
	}
	if err != nil {
		return event, false, err
	}
	event.GrantedAt = parseDBTime(grantedAt)
	event.ExpiresAt = parseDBTime(expiresAt)
	event.SnapshotJSON = []byte(snapshot)
	event.DetectedAt = parseDBTime(detected)
	return event, true, nil
}

func (s *Store) InsertRadarAlertEvent(ctx context.Context, alert radar.ProbabilityAlert, targets ...string) (bool, error) {
	targets, err := notificationTargets("", targets)
	if err != nil {
		return false, err
	}
	now := formatTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
insert into radar_alert_events (
  id, milestone, probability_24h, probability_48h, level, expected_window,
  reasoning_summary, checked_at, detected_at, snapshot_json, created_at
) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
on conflict(id) do nothing`,
		alert.ID, alert.Milestone, alert.Probability24H, alert.Probability48H, alert.Level, alert.ExpectedWindow,
		alert.ReasoningSummary, alert.CheckedAt, formatTime(alert.DetectedAt), string(alert.SnapshotJSON), now)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	if affected > 0 {
		for _, target := range targets {
			if err := enqueueEvent(ctx, tx, "radar_alert", alert.ID, "", target, alert); err != nil {
				return false, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (s *Store) LoadRadarAlertEvent(ctx context.Context, id string) (radar.ProbabilityAlert, bool, error) {
	var alert radar.ProbabilityAlert
	var detected, snapshot string
	err := s.db.QueryRowContext(ctx, `
select id, milestone, probability_24h, probability_48h, level, expected_window,
  reasoning_summary, checked_at, detected_at, snapshot_json
from radar_alert_events
where id = ?`, id).Scan(
		&alert.ID, &alert.Milestone, &alert.Probability24H, &alert.Probability48H, &alert.Level, &alert.ExpectedWindow,
		&alert.ReasoningSummary, &alert.CheckedAt, &detected, &snapshot,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return alert, false, nil
	}
	if err != nil {
		return alert, false, err
	}
	alert.DetectedAt = parseDBTime(detected)
	alert.SnapshotJSON = []byte(snapshot)
	return alert, true, nil
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
	return s.loadLatestObservation(ctx, "", "")
}

func (s *Store) LoadLatestObservationForProvider(ctx context.Context, providerID string) (resetwatch.Observation, bool, error) {
	if strings.TrimSpace(providerID) == "" {
		return resetwatch.Observation{}, false, errors.New("provider id is required")
	}
	return s.loadLatestObservation(ctx, providerID, "")
}

// LoadLatestObservationForProfile resolves one enabled profile's current
// provider account, then loads only that account's newest observation.
func (s *Store) LoadLatestObservationForProfile(ctx context.Context, profileRef string) (resetwatch.Observation, bool, error) {
	if !validProfileRef(profileRef) {
		return resetwatch.Observation{}, false, ErrInvalidProfile
	}
	var providerID, accountRef string
	err := s.db.QueryRowContext(ctx, `
select p.provider_id,pa.account_ref
from profiles p
join profile_accounts pa on pa.profile_ref=p.profile_ref and pa.provider_id=p.provider_id and pa.is_current=1
where p.profile_ref=? and p.enabled=1`, profileRef).Scan(&providerID, &accountRef)
	if errors.Is(err, sql.ErrNoRows) {
		return resetwatch.Observation{}, false, nil
	}
	if err != nil {
		return resetwatch.Observation{}, false, err
	}
	return s.loadLatestObservation(ctx, providerID, accountRef)
}

func (s *Store) loadLatestObservation(ctx context.Context, providerID, accountRef string) (resetwatch.Observation, bool, error) {
	var obs resetwatch.Observation
	var observationID, observedAt, snapshot string
	clauses := []string{}
	args := []any{}
	if providerID != "" {
		clauses = append(clauses, "o.provider_id = ?")
		args = append(args, providerID)
	}
	if accountRef != "" {
		clauses = append(clauses, "o.account_ref = ?")
		args = append(args, accountRef)
	}
	where := ""
	if len(clauses) > 0 {
		where = "where " + strings.Join(clauses, " and ")
	}
	err := s.db.QueryRowContext(ctx, `
select o.id, o.provider_id, o.account_ref, a.label, a.email, a.plan, o.observed_at, o.snapshot_json
from limit_observations o
join accounts a on a.account_ref = o.account_ref
`+where+`
order by o.observed_at desc, o.created_at desc, o.id desc
limit 1`, args...).Scan(
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
	obs.ResetGrants = resetwatch.ResetGrantsFromSnapshotJSON(obs.SnapshotJSON)
	return obs, true, nil
}

func (s *Store) PruneObservations(ctx context.Context, cutoff time.Time, compact bool) (PruneResult, error) {
	result := PruneResult{Cutoff: cutoff.UTC()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	candidateBound := formatTime(cutoff.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour))
	observationIDs, err := expiredRowIDs(ctx, tx, cutoff, `select id,observed_at from limit_observations where observed_at < ?`, candidateBound)
	if err != nil {
		return result, err
	}
	result.DeletedWindows, err = deleteRowsByID(ctx, tx, `delete from observed_windows where observation_id in`, observationIDs)
	if err != nil {
		return result, err
	}
	result.DeletedObservations, err = deleteRowsByID(ctx, tx, `delete from limit_observations where id in`, observationIDs)
	if err != nil {
		return result, err
	}

	// Queue work is retained until it reaches a terminal state. Terminal rows use
	// updated_at so a recently retried or acknowledged item cannot be removed by
	// an old creation timestamp.
	outboxIDs, err := expiredRowIDs(ctx, tx, cutoff, `select id,updated_at from notification_outbox where status in ('delivered','dead_letter') and updated_at < ?`, candidateBound)
	if err != nil {
		return result, err
	}
	result.DeletedDeliveries, err = deleteRowsByID(ctx, tx, `delete from notification_outbox where id in`, outboxIDs)
	if err != nil {
		return result, err
	}
	inboxIDs, err := expiredRowIDs(ctx, tx, cutoff, `select cast(rowid as text),updated_at from telegram_updates where status in ('processed','dead') and updated_at < ?`, candidateBound)
	if err != nil {
		return result, err
	}
	result.DeletedInboxRows, err = deleteRowsByID(ctx, tx, `delete from telegram_updates where rowid in`, inboxIDs)
	if err != nil {
		return result, err
	}

	// The canonical outbox is authoritative after schema v7. Legacy delivery
	// ledgers and their typed source events may be removed only when no active
	// canonical delivery still refers to the event.
	for _, pair := range []struct {
		deliveryTable string
		deliveryKey   string
		eventTable    string
		eventKind     string
	}{
		{"notification_deliveries", "event_id", "reset_events", "reset"},
		{"limit_warning_deliveries", "warning_id", "limit_warning_events", "limit_warning"},
		{"reset_grant_warning_deliveries", "warning_id", "reset_grant_warning_events", "reset_grant_warning"},
		{"reset_grant_deliveries", "event_id", "reset_grant_events", "reset_grant"},
		{"radar_alert_deliveries", "alert_id", "radar_alert_events", "radar_alert"},
	} {
		// #nosec G201 -- identifiers and event kinds come from the closed literal list above.
		query := fmt.Sprintf(`select e.id,e.detected_at from %s e where e.detected_at < ? and not exists(select 1 from notification_outbox o where o.event_kind=? and o.event_id=e.id and o.status in ('pending','leased'))`, pair.eventTable)
		eventIDs, loadErr := expiredRowIDs(ctx, tx, cutoff, query, candidateBound, pair.eventKind)
		if loadErr != nil {
			return result, loadErr
		}
		// #nosec G201 -- identifiers come from the closed literal list above.
		count, deleteErr := deleteRowsByID(ctx, tx, fmt.Sprintf(`delete from %s where %s in`, pair.deliveryTable, pair.deliveryKey), eventIDs)
		if deleteErr != nil {
			return result, deleteErr
		}
		result.DeletedDeliveries += count
		// #nosec G201 -- identifier comes from the closed literal list above.
		count, deleteErr = deleteRowsByID(ctx, tx, fmt.Sprintf(`delete from %s where id in`, pair.eventTable), eventIDs)
		if deleteErr != nil {
			return result, deleteErr
		}
		result.DeletedEvents += count
	}

	// Deleting policy events turns replay mappings into tombstones. Keep only the
	// newest tombstone per account as its explicit prune floor. This bounds the
	// ledger without confusing valid cursors when other accounts own intervening
	// values in the global replay sequence.
	policyIDs, err := expiredRowIDs(ctx, tx, cutoff, `select id,detected_at from policy_events where detected_at < ? and not exists(select 1 from notification_outbox o where o.event_kind=policy_events.event_kind and o.event_id=policy_events.semantic_event_id and o.status in ('pending','leased'))`, candidateBound)
	if err != nil {
		return result, err
	}
	count, err := deleteRowsByID(ctx, tx, `delete from policy_events where id in`, policyIDs)
	if err != nil {
		return result, err
	}
	result.DeletedEvents += count
	deleted, err := tx.ExecContext(ctx, `delete from policy_event_replay as old where policy_event_id is null and exists(select 1 from policy_event_replay newer where newer.provider_id=old.provider_id and newer.account_ref=old.account_ref and newer.policy_event_id is null and newer.replay_seq>old.replay_seq)`)
	if err != nil {
		return result, err
	}
	result.DeletedReplayRows, _ = deleted.RowsAffected()
	if err := tx.Commit(); err != nil {
		return result, err
	}
	if compact && (result.DeletedObservations > 0 || result.DeletedWindows > 0 || result.DeletedEvents > 0 || result.DeletedDeliveries > 0 || result.DeletedReplayRows > 0 || result.DeletedInboxRows > 0) {
		if err := s.Checkpoint(ctx); err != nil {
			return result, fmt.Errorf("retention committed but checkpoint failed: %w", err)
		}
		result.Checkpointed = true
		if err := s.Vacuum(ctx); err != nil {
			return result, fmt.Errorf("retention committed and checkpointed but vacuum failed: %w", err)
		}
		result.Vacuumed = true
	}
	return result, nil
}

func expiredRowIDs(ctx context.Context, tx *sql.Tx, cutoff time.Time, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return nil, fmt.Errorf("parse retained row timestamp %q: %w", raw, err)
		}
		if at.Before(cutoff) {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

func deleteRowsByID(ctx context.Context, tx *sql.Tx, query string, ids []string) (int64, error) {
	var total int64
	const batchSize = 200
	for start := 0; start < len(ids); start += batchSize {
		end := min(start+batchSize, len(ids))
		args := make([]any, end-start)
		placeholders := make([]string, end-start)
		for i, id := range ids[start:end] {
			args[i] = id
			placeholders[i] = "?"
		}
		result, err := tx.ExecContext(ctx, query+" ("+strings.Join(placeholders, ",")+")", args...)
		if err != nil {
			return total, err
		}
		count, _ := result.RowsAffected()
		total += count
	}
	return total, nil
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

func (s *Store) EnsureWarningDelivery(ctx context.Context, warningID, target string) (Delivery, error) {
	now := formatTime(time.Now())
	id := WarningDeliveryID(warningID, target)
	_, err := s.db.ExecContext(ctx, `
insert into limit_warning_deliveries (id, warning_id, target, status, attempts, created_at, updated_at)
values (?, ?, ?, 'pending', 0, ?, ?)
on conflict(warning_id, target) do nothing`, id, warningID, target, now, now)
	if err != nil {
		return Delivery{}, err
	}
	delivery, ok, err := s.LoadWarningDelivery(ctx, warningID, target)
	if err != nil {
		return Delivery{}, err
	}
	if !ok {
		return Delivery{}, errors.New("warning delivery not found after insert")
	}
	return delivery, nil
}

func (s *Store) EnsureGrantExpiryWarningDelivery(ctx context.Context, warningID, target string) (Delivery, error) {
	now := formatTime(time.Now())
	id := GrantExpiryWarningDeliveryID(warningID, target)
	_, err := s.db.ExecContext(ctx, `
insert into reset_grant_warning_deliveries (id, warning_id, target, status, attempts, created_at, updated_at)
values (?, ?, ?, 'pending', 0, ?, ?)
on conflict(warning_id, target) do nothing`, id, warningID, target, now, now)
	if err != nil {
		return Delivery{}, err
	}
	delivery, ok, err := s.LoadGrantExpiryWarningDelivery(ctx, warningID, target)
	if err != nil {
		return Delivery{}, err
	}
	if !ok {
		return Delivery{}, errors.New("reset grant warning delivery not found after insert")
	}
	return delivery, nil
}

func (s *Store) EnsureResetGrantDelivery(ctx context.Context, eventID, target string) (Delivery, error) {
	now := formatTime(time.Now())
	id := ResetGrantDeliveryID(eventID, target)
	_, err := s.db.ExecContext(ctx, `
insert into reset_grant_deliveries (id, event_id, target, status, attempts, created_at, updated_at)
values (?, ?, ?, 'pending', 0, ?, ?)
on conflict(event_id, target) do nothing`, id, eventID, target, now, now)
	if err != nil {
		return Delivery{}, err
	}
	delivery, ok, err := s.LoadResetGrantDelivery(ctx, eventID, target)
	if err != nil {
		return Delivery{}, err
	}
	if !ok {
		return Delivery{}, errors.New("reset grant delivery not found after insert")
	}
	return delivery, nil
}

func (s *Store) EnsureRadarAlertDelivery(ctx context.Context, alertID, target string) (Delivery, error) {
	now := formatTime(time.Now())
	id := RadarAlertDeliveryID(alertID, target)
	_, err := s.db.ExecContext(ctx, `
insert into radar_alert_deliveries (id, alert_id, target, status, attempts, created_at, updated_at)
values (?, ?, ?, 'pending', 0, ?, ?)
on conflict(alert_id, target) do nothing`, id, alertID, target, now, now)
	if err != nil {
		return Delivery{}, err
	}
	delivery, ok, err := s.LoadRadarAlertDelivery(ctx, alertID, target)
	if err != nil {
		return Delivery{}, err
	}
	if !ok {
		return Delivery{}, errors.New("radar alert delivery not found after insert")
	}
	return delivery, nil
}

func (s *Store) LoadDelivery(ctx context.Context, eventID, target string) (Delivery, bool, error) {
	return scanDelivery(s.db.QueryRowContext(ctx, `
select id, event_id, target, status, attempts, last_attempt_at, next_attempt_at, delivered_at, provider_message_id, last_error, created_at, updated_at
from notification_deliveries
where event_id = ? and target = ?`, eventID, target))
}

func (s *Store) LoadWarningDelivery(ctx context.Context, warningID, target string) (Delivery, bool, error) {
	return scanDelivery(s.db.QueryRowContext(ctx, `
select id, warning_id, target, status, attempts, last_attempt_at, next_attempt_at, delivered_at, provider_message_id, last_error, created_at, updated_at
from limit_warning_deliveries
	where warning_id = ? and target = ?`, warningID, target))
}

func (s *Store) LoadGrantExpiryWarningDelivery(ctx context.Context, warningID, target string) (Delivery, bool, error) {
	return scanDelivery(s.db.QueryRowContext(ctx, `
select id, warning_id, target, status, attempts, last_attempt_at, next_attempt_at, delivered_at, provider_message_id, last_error, created_at, updated_at
from reset_grant_warning_deliveries
where warning_id = ? and target = ?`, warningID, target))
}

func (s *Store) LoadResetGrantDelivery(ctx context.Context, eventID, target string) (Delivery, bool, error) {
	return scanDelivery(s.db.QueryRowContext(ctx, `
select id, event_id, target, status, attempts, last_attempt_at, next_attempt_at, delivered_at, provider_message_id, last_error, created_at, updated_at
from reset_grant_deliveries
where event_id = ? and target = ?`, eventID, target))
}

func (s *Store) LoadRadarAlertDelivery(ctx context.Context, alertID, target string) (Delivery, bool, error) {
	return scanDelivery(s.db.QueryRowContext(ctx, `
select id, alert_id, target, status, attempts, last_attempt_at, next_attempt_at, delivered_at, provider_message_id, last_error, created_at, updated_at
from radar_alert_deliveries
where alert_id = ? and target = ?`, alertID, target))
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

func (s *Store) PendingWarningDeliveries(ctx context.Context, target string, limit int) ([]Delivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
select id, warning_id, target, status, attempts, last_attempt_at, next_attempt_at, delivered_at, provider_message_id, last_error, created_at, updated_at
from limit_warning_deliveries
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

func (s *Store) PendingGrantExpiryWarningDeliveries(ctx context.Context, target string, limit int) ([]Delivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
select id, warning_id, target, status, attempts, last_attempt_at, next_attempt_at, delivered_at, provider_message_id, last_error, created_at, updated_at
from reset_grant_warning_deliveries
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

func (s *Store) PendingResetGrantDeliveries(ctx context.Context, target string, limit int) ([]Delivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
select id, event_id, target, status, attempts, last_attempt_at, next_attempt_at, delivered_at, provider_message_id, last_error, created_at, updated_at
from reset_grant_deliveries
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

func (s *Store) PendingRadarAlertDeliveries(ctx context.Context, target string, limit int) ([]Delivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
select id, alert_id, target, status, attempts, last_attempt_at, next_attempt_at, delivered_at, provider_message_id, last_error, created_at, updated_at
from radar_alert_deliveries
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

func (s *Store) MarkDeliverySending(ctx context.Context, eventID, target string) error {
	return s.markDeliverySending(ctx, "notification_deliveries", "event_id", eventID, target)
}

func (s *Store) MarkWarningDeliveryAttempt(ctx context.Context, warningID, target string, delivered bool, message string, providerMessageID string) error {
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
		_ = s.db.QueryRowContext(ctx, `select attempts from limit_warning_deliveries where warning_id = ? and target = ?`, warningID, target).Scan(&attempts)
		nextAttemptAt = formatTime(time.Now().Add(deliveryBackoff(attempts + 1)))
	}
	_, err := s.db.ExecContext(ctx, `
update limit_warning_deliveries
set status = ?, attempts = attempts + 1, last_attempt_at = ?, next_attempt_at = ?, delivered_at = coalesce(?, delivered_at), provider_message_id = coalesce(nullif(?, ''), provider_message_id), last_error = ?, updated_at = ?
where warning_id = ? and target = ?`, status, now, nextAttemptAt, deliveredAt, providerMessageID, lastError, now, warningID, target)
	return err
}

func (s *Store) MarkWarningDeliverySending(ctx context.Context, warningID, target string) error {
	return s.markDeliverySending(ctx, "limit_warning_deliveries", "warning_id", warningID, target)
}

func (s *Store) MarkGrantExpiryWarningDeliveryAttempt(ctx context.Context, warningID, target string, delivered bool, message string, providerMessageID string) error {
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
		_ = s.db.QueryRowContext(ctx, `select attempts from reset_grant_warning_deliveries where warning_id = ? and target = ?`, warningID, target).Scan(&attempts)
		nextAttemptAt = formatTime(time.Now().Add(deliveryBackoff(attempts + 1)))
	}
	_, err := s.db.ExecContext(ctx, `
update reset_grant_warning_deliveries
set status = ?, attempts = attempts + 1, last_attempt_at = ?, next_attempt_at = ?, delivered_at = coalesce(?, delivered_at), provider_message_id = coalesce(nullif(?, ''), provider_message_id), last_error = ?, updated_at = ?
where warning_id = ? and target = ?`, status, now, nextAttemptAt, deliveredAt, providerMessageID, lastError, now, warningID, target)
	return err
}

func (s *Store) MarkGrantExpiryWarningDeliverySending(ctx context.Context, warningID, target string) error {
	return s.markDeliverySending(ctx, "reset_grant_warning_deliveries", "warning_id", warningID, target)
}

func (s *Store) MarkResetGrantDeliveryAttempt(ctx context.Context, eventID, target string, delivered bool, message string, providerMessageID string) error {
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
		_ = s.db.QueryRowContext(ctx, `select attempts from reset_grant_deliveries where event_id = ? and target = ?`, eventID, target).Scan(&attempts)
		nextAttemptAt = formatTime(time.Now().Add(deliveryBackoff(attempts + 1)))
	}
	_, err := s.db.ExecContext(ctx, `
update reset_grant_deliveries
set status = ?, attempts = attempts + 1, last_attempt_at = ?, next_attempt_at = ?, delivered_at = coalesce(?, delivered_at), provider_message_id = coalesce(nullif(?, ''), provider_message_id), last_error = ?, updated_at = ?
where event_id = ? and target = ?`, status, now, nextAttemptAt, deliveredAt, providerMessageID, lastError, now, eventID, target)
	return err
}

func (s *Store) MarkResetGrantDeliverySending(ctx context.Context, eventID, target string) error {
	return s.markDeliverySending(ctx, "reset_grant_deliveries", "event_id", eventID, target)
}

func (s *Store) MarkRadarAlertDeliveryAttempt(ctx context.Context, alertID, target string, delivered bool, message string, providerMessageID string) error {
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
		_ = s.db.QueryRowContext(ctx, `select attempts from radar_alert_deliveries where alert_id = ? and target = ?`, alertID, target).Scan(&attempts)
		nextAttemptAt = formatTime(time.Now().Add(deliveryBackoff(attempts + 1)))
	}
	_, err := s.db.ExecContext(ctx, `
update radar_alert_deliveries
set status = ?, attempts = attempts + 1, last_attempt_at = ?, next_attempt_at = ?, delivered_at = coalesce(?, delivered_at), provider_message_id = coalesce(nullif(?, ''), provider_message_id), last_error = ?, updated_at = ?
where alert_id = ? and target = ?`, status, now, nextAttemptAt, deliveredAt, providerMessageID, lastError, now, alertID, target)
	return err
}

func (s *Store) MarkRadarAlertDeliverySending(ctx context.Context, alertID, target string) error {
	return s.markDeliverySending(ctx, "radar_alert_deliveries", "alert_id", alertID, target)
}

func (s *Store) markDeliverySending(ctx context.Context, table, idColumn, eventID, target string) error {
	now := formatTime(time.Now())
	nextAttemptAt := formatTime(time.Now().Add(deliverySendLease))
	query, ok := deliverySendingQuery(table, idColumn)
	if !ok {
		return errors.New("unknown delivery table")
	}
	_, err := s.db.ExecContext(ctx, query, now, nextAttemptAt, now, eventID, target)
	return err
}

func deliverySendingQuery(table, idColumn string) (string, bool) {
	switch {
	case table == "notification_deliveries" && idColumn == "event_id":
		return `
update notification_deliveries
set status = 'sending', last_attempt_at = ?, next_attempt_at = ?, last_error = null, updated_at = ?
where event_id = ? and target = ? and status != 'delivered'`, true
	case table == "limit_warning_deliveries" && idColumn == "warning_id":
		return `
update limit_warning_deliveries
set status = 'sending', last_attempt_at = ?, next_attempt_at = ?, last_error = null, updated_at = ?
where warning_id = ? and target = ? and status != 'delivered'`, true
	case table == "reset_grant_warning_deliveries" && idColumn == "warning_id":
		return `
update reset_grant_warning_deliveries
set status = 'sending', last_attempt_at = ?, next_attempt_at = ?, last_error = null, updated_at = ?
where warning_id = ? and target = ? and status != 'delivered'`, true
	case table == "reset_grant_deliveries" && idColumn == "event_id":
		return `
update reset_grant_deliveries
set status = 'sending', last_attempt_at = ?, next_attempt_at = ?, last_error = null, updated_at = ?
where event_id = ? and target = ? and status != 'delivered'`, true
	case table == "radar_alert_deliveries" && idColumn == "alert_id":
		return `
update radar_alert_deliveries
set status = 'sending', last_attempt_at = ?, next_attempt_at = ?, last_error = null, updated_at = ?
where alert_id = ? and target = ? and status != 'delivered'`, true
	default:
		return "", false
	}
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

func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `delete from server_settings where key = ?`, key)
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
	on conflict(bot_ref) do update set last_update_id = excluded.last_update_id, updated_at = excluded.updated_at
	where excluded.last_update_id > telegram_offsets.last_update_id`, botRef, updateID, formatTime(time.Now()))
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

func WarningDeliveryID(warningID, target string) string {
	sum := sha256.Sum256([]byte(warningID + "\x00" + target))
	return "warning_delivery_" + hex.EncodeToString(sum[:16])
}

func GrantExpiryWarningDeliveryID(warningID, target string) string {
	sum := sha256.Sum256([]byte(warningID + "\x00" + target))
	return "grant_warning_delivery_" + hex.EncodeToString(sum[:16])
}

func ResetGrantDeliveryID(eventID, target string) string {
	sum := sha256.Sum256([]byte(eventID + "\x00" + target))
	return "grant_delivery_" + hex.EncodeToString(sum[:16])
}

func RadarAlertDeliveryID(alertID, target string) string {
	sum := sha256.Sum256([]byte(alertID + "\x00" + target))
	return "radar_delivery_" + hex.EncodeToString(sum[:16])
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

func insertResetEvents(ctx context.Context, tx *sql.Tx, events []resetwatch.Event, targets []string) (int, error) {
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
			if err := enqueueEventTargets(ctx, tx, "reset", event.ID, event.Account.Ref, targets, event); err != nil {
				return inserted, err
			}
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

func upsertWarningAccount(ctx context.Context, tx *sql.Tx, warning resetwatch.WarningEvent) error {
	_, err := tx.ExecContext(ctx, `
insert into accounts (account_ref, provider_id, label, email, plan, updated_at)
values (?, ?, ?, ?, ?, ?)
on conflict(account_ref) do update set
  provider_id = excluded.provider_id,
  label = excluded.label,
  email = excluded.email,
  plan = excluded.plan,
  updated_at = excluded.updated_at`,
		warning.Account.Ref, providerID(warning.ProviderID), warning.Account.Label, warning.Account.Email, warning.Account.Plan, formatTime(time.Now()))
	return err
}

func upsertGrantWarningAccount(ctx context.Context, tx *sql.Tx, warning resetwatch.GrantExpiryWarning) error {
	if warning.Account.Ref == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
insert into accounts (account_ref, provider_id, label, email, plan, updated_at)
values (?, ?, ?, ?, ?, ?)
on conflict(account_ref) do update set
  provider_id = excluded.provider_id,
  label = excluded.label,
  email = excluded.email,
  plan = excluded.plan,
  updated_at = excluded.updated_at`,
		warning.Account.Ref, providerID(warning.ProviderID), warning.Account.Label, warning.Account.Email, warning.Account.Plan, formatTime(time.Now()))
	return err
}

func resetGrantTrackingState(ctx context.Context, tx *sql.Tx, accountRef string) (bool, int, error) {
	if accountRef == "" {
		return false, 0, nil
	}
	var availableCount int
	err := tx.QueryRowContext(ctx, `select available_count from reset_grant_tracking_state where account_ref = ?`, accountRef).Scan(&availableCount)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, nil
	}
	return err == nil, availableCount, err
}

func upsertResetGrantTrackingState(ctx context.Context, tx *sql.Tx, obs resetwatch.Observation, availableCount int) error {
	if obs.Account.Ref == "" {
		return nil
	}
	now := formatTime(time.Now())
	_, err := tx.ExecContext(ctx, `
insert into reset_grant_tracking_state (account_ref, provider_id, available_count, last_observed_at, created_at, updated_at)
values (?, ?, ?, ?, ?, ?)
on conflict(account_ref) do update set
  provider_id = excluded.provider_id,
  available_count = excluded.available_count,
  last_observed_at = excluded.last_observed_at,
  updated_at = excluded.updated_at`,
		obs.Account.Ref, providerID(obs.ProviderID), availableCount, formatTime(obs.ObservedAt), now, now)
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
