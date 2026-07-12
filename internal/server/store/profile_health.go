package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrProfilePollStale = errors.New("stale profile poll attempt")

const (
	ProfileFailureNone          = ""
	ProfileFailureLegacy        = "legacy"
	ProfileFailureAuth          = "auth"
	ProfileFailureNetwork       = "network"
	ProfileFailureProvider      = "provider"
	ProfileFailureInternal      = "internal"
	ProfileErrorNone            = ""
	ProfileErrorUnauthorized    = "unauthorized"
	ProfileErrorRateLimited     = "rate_limited"
	ProfileErrorTimeout         = "timeout"
	ProfileErrorUnavailable     = "unavailable"
	ProfileErrorInvalidResponse = "invalid_response"
	ProfileErrorInternal        = "internal"
)

type ProfileHealth struct {
	ProfileRef, ProviderID, Label               string
	Enabled, IsDefault                          bool
	LastAttemptAt, LastSuccessAt, LastFailureAt *time.Time
	ConsecutiveFailures                         int
	FailureKind, LastErrorCode, AlertState      string
	UpdatedAt                                   time.Time
}

func validFailure(kind, code string) bool {
	kinds := map[string]bool{"": true, "legacy": true, "auth": true, "network": true, "provider": true, "internal": true}
	codes := map[string]bool{"": true, "unauthorized": true, "rate_limited": true, "timeout": true, "unavailable": true, "invalid_response": true, "internal": true}
	return kinds[kind] && codes[code] && (kind != "" || code == "")
}

func (s *Store) validateEnabledProfile(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ref string) error {
	if !validProfileRef(ref) {
		return ErrInvalidProfile
	}
	var enabled int
	err := q.QueryRowContext(ctx, `select enabled from profiles where profile_ref=?`, ref).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProfileMissing
	}
	if err != nil {
		return err
	}
	if enabled != 1 {
		return ErrProfileDisabled
	}
	return nil
}

func parseOptionalProfileTime(raw sql.NullString) (*time.Time, error) {
	if !raw.Valid {
		return nil, nil
	}
	v, err := time.Parse(time.RFC3339Nano, raw.String)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func latestProfileTime(values ...*time.Time) *time.Time {
	var latest *time.Time
	for _, value := range values {
		if value != nil && (latest == nil || value.After(*latest)) {
			copy := *value
			latest = &copy
		}
	}
	return latest
}

func monotonicProfileTime(current time.Time, candidate time.Time) string {
	if current.After(candidate) {
		return formatTime(current)
	}
	return formatTime(candidate)
}

func (s *Store) ListProfileHealth(ctx context.Context) ([]ProfileHealth, error) {
	rows, err := s.db.QueryContext(ctx, `select p.profile_ref,p.provider_id,p.label,p.enabled,p.is_default,h.last_attempt_at,h.last_success_at,h.last_failure_at,h.consecutive_failures,h.failure_kind,h.last_error_code,h.alert_state,h.updated_at from profiles p join profile_poll_health h on h.profile_ref=p.profile_ref order by p.is_default desc,p.profile_ref collate binary`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ProfileHealth
	for rows.Next() {
		var h ProfileHealth
		var enabled, def int
		var attempt, success, failure sql.NullString
		var updated string
		if err = rows.Scan(&h.ProfileRef, &h.ProviderID, &h.Label, &enabled, &def, &attempt, &success, &failure, &h.ConsecutiveFailures, &h.FailureKind, &h.LastErrorCode, &h.AlertState, &updated); err != nil {
			return nil, err
		}
		h.Enabled = enabled == 1
		h.IsDefault = def == 1
		if h.LastAttemptAt, err = parseOptionalProfileTime(attempt); err != nil {
			return nil, err
		}
		if h.LastSuccessAt, err = parseOptionalProfileTime(success); err != nil {
			return nil, err
		}
		if h.LastFailureAt, err = parseOptionalProfileTime(failure); err != nil {
			return nil, err
		}
		if h.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) RecordProfilePollAttempt(ctx context.Context, ref string, attempt time.Time) error {
	if attempt.IsZero() {
		return ErrInvalidProfile
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = s.validateEnabledProfile(ctx, tx, ref); err != nil {
		return err
	}
	var rawAttempt, rawSuccess, rawFailure sql.NullString
	var rawUpdated string
	if err = tx.QueryRowContext(ctx, `select last_attempt_at,last_success_at,last_failure_at,updated_at from profile_poll_health where profile_ref=?`, ref).Scan(&rawAttempt, &rawSuccess, &rawFailure, &rawUpdated); err != nil {
		return err
	}
	previousAttempt, err := parseOptionalProfileTime(rawAttempt)
	if err != nil {
		return err
	}
	lastSuccess, err := parseOptionalProfileTime(rawSuccess)
	if err != nil {
		return err
	}
	lastFailure, err := parseOptionalProfileTime(rawFailure)
	if err != nil {
		return err
	}
	updated, err := time.Parse(time.RFC3339Nano, rawUpdated)
	if err != nil {
		return err
	}
	terminal := latestProfileTime(lastSuccess, lastFailure)
	if terminal != nil && !attempt.After(*terminal) {
		return ErrProfilePollStale
	}
	if previousAttempt != nil {
		pending := terminal == nil || previousAttempt.After(*terminal)
		if pending && attempt.Equal(*previousAttempt) {
			return nil
		}
		if !attempt.After(*previousAttempt) {
			return ErrProfilePollStale
		}
	}
	if _, err = tx.ExecContext(ctx, `update profile_poll_health set last_attempt_at=?,updated_at=? where profile_ref=?`, formatTime(attempt), monotonicProfileTime(updated, attempt), ref); err != nil {
		return err
	}
	return tx.Commit()
}

func fencedAttempt(ctx context.Context, tx *sql.Tx, ref string, attempt time.Time) (string, time.Time, error) {
	if attempt.IsZero() {
		return "", time.Time{}, ErrProfilePollStale
	}
	if err := (&Store{}).validateEnabledProfile(ctx, tx, ref); err != nil {
		return "", time.Time{}, err
	}
	var rawAttempt, rawSuccess, rawFailure sql.NullString
	var rawUpdated string
	if err := tx.QueryRowContext(ctx, `select last_attempt_at,last_success_at,last_failure_at,updated_at from profile_poll_health where profile_ref=?`, ref).Scan(&rawAttempt, &rawSuccess, &rawFailure, &rawUpdated); err != nil {
		return "", time.Time{}, err
	}
	if !rawAttempt.Valid {
		return "", time.Time{}, ErrProfilePollStale
	}
	parsed, err := time.Parse(time.RFC3339Nano, rawAttempt.String)
	if err != nil {
		return "", time.Time{}, err
	}
	if !parsed.Equal(attempt) {
		return "", time.Time{}, ErrProfilePollStale
	}
	lastSuccess, err := parseOptionalProfileTime(rawSuccess)
	if err != nil {
		return "", time.Time{}, err
	}
	lastFailure, err := parseOptionalProfileTime(rawFailure)
	if err != nil {
		return "", time.Time{}, err
	}
	terminal := latestProfileTime(lastSuccess, lastFailure)
	if terminal != nil && !attempt.After(*terminal) {
		return "", time.Time{}, ErrProfilePollStale
	}
	updated, err := time.Parse(time.RFC3339Nano, rawUpdated)
	if err != nil {
		return "", time.Time{}, err
	}
	return rawAttempt.String, updated, nil
}

func (s *Store) RecordProfilePollSuccess(ctx context.Context, ref string, attempt, completed time.Time) error {
	if completed.IsZero() || completed.Before(attempt) {
		return ErrProfilePollStale
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	raw, updated, err := fencedAttempt(ctx, tx, ref, attempt)
	if err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, `update profile_poll_health set last_success_at=?,consecutive_failures=0,failure_kind='',last_error_code='',updated_at=? where profile_ref=? and last_attempt_at=?`, formatTime(completed), monotonicProfileTime(updated, completed), ref, raw)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrProfilePollStale
	}
	return tx.Commit()
}

func (s *Store) RecordProfilePollFailure(ctx context.Context, ref string, attempt, completed time.Time, kind, code string) error {
	if completed.IsZero() || completed.Before(attempt) {
		return ErrProfilePollStale
	}
	if !validFailure(kind, code) || kind == "" {
		return errors.New("invalid profile failure classification")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	raw, updated, err := fencedAttempt(ctx, tx, ref, attempt)
	if err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, `update profile_poll_health set last_failure_at=?,consecutive_failures=consecutive_failures+1,failure_kind=?,last_error_code=?,updated_at=? where profile_ref=? and last_attempt_at=?`, formatTime(completed), kind, code, monotonicProfileTime(updated, completed), ref, raw)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrProfilePollStale
	}
	return tx.Commit()
}

func (s *Store) AbortProfilePollAttempt(ctx context.Context, ref string, attempt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	raw, updated, err := fencedAttempt(ctx, tx, ref, attempt)
	if err != nil {
		return err
	}
	var success, failure sql.NullString
	if err = tx.QueryRowContext(ctx, `select last_success_at,last_failure_at from profile_poll_health where profile_ref=?`, ref).Scan(&success, &failure); err != nil {
		return err
	}
	var restore any
	var latest time.Time
	for _, v := range []sql.NullString{success, failure} {
		if !v.Valid {
			continue
		}
		parsed, e := time.Parse(time.RFC3339Nano, v.String)
		if e != nil {
			return e
		}
		if restore == nil || parsed.After(latest) {
			restore = v.String
			latest = parsed
		}
	}
	r, err := tx.ExecContext(ctx, `update profile_poll_health set last_attempt_at=?,updated_at=? where profile_ref=? and last_attempt_at=?`, restore, monotonicProfileTime(updated, time.Now()), ref, raw)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrProfilePollStale
	}
	return tx.Commit()
}

func (s *Store) CompareAndSwapProfileAlertState(ctx context.Context, ref, from, to string) (bool, error) {
	if (from != "ok" && from != "failing") || (to != "ok" && to != "failing") || from == to {
		return false, errors.New("invalid profile alert transition")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = s.validateEnabledProfile(ctx, tx, ref); err != nil {
		return false, err
	}
	var rawUpdated string
	if err = tx.QueryRowContext(ctx, `select updated_at from profile_poll_health where profile_ref=?`, ref).Scan(&rawUpdated); err != nil {
		return false, err
	}
	updated, err := time.Parse(time.RFC3339Nano, rawUpdated)
	if err != nil {
		return false, err
	}
	r, err := tx.ExecContext(ctx, `update profile_poll_health set alert_state=?,updated_at=? where profile_ref=? and alert_state=?`, to, monotonicProfileTime(updated, time.Now()), ref, from)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return n == 1, nil
}
