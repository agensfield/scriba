package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/agensfield/scriba/internal/resetwatch"
)

var (
	ErrInvalidSource   = errors.New("invalid auth source")
	ErrSourceMissing   = errors.New("auth source missing")
	ErrSourceDisabled  = errors.New("auth source disabled")
	ErrSourcePollStale = errors.New("stale auth source poll attempt")
)

const (
	SourceFailureNone     = ""
	SourceFailureLegacy   = "legacy"
	SourceFailureAuth     = "auth"
	SourceFailureNetwork  = "network"
	SourceFailureProvider = "provider"
	SourceFailureInternal = "internal"

	SourceErrorNone            = ""
	SourceErrorUnauthorized    = "unauthorized"
	SourceErrorRateLimited     = "rate_limited"
	SourceErrorTimeout         = "timeout"
	SourceErrorUnavailable     = "unavailable"
	SourceErrorInvalidResponse = "invalid_response"
	SourceErrorInternal        = "internal"
	SourceErrorAuthUnavailable = "auth_unavailable"
	SourceErrorAuthRejected    = "auth_rejected"
	SourceErrorNoResetWindows  = "no_reset_windows"
	SourceErrorRequestFailed   = "request_failed"
	SourceErrorPersistence     = "persistence_failed"
	SourceErrorSourceDisabled  = "source_disabled"
)

type SourceSpec struct {
	Ref      string
	Enabled  bool
	Priority int
}

type SourceHealth struct {
	SourceRef                                   string
	Enabled                                     bool
	Priority                                    int
	AccountRef                                  string
	LastAttemptAt, LastSuccessAt, LastFailureAt *time.Time
	ConsecutiveFailures                         int
	FailureKind, LastErrorCode, AlertState      string
	UpdatedAt                                   time.Time
}

func validSourceRef(v string) bool {
	if len(v) != 24 || len(v) < 4 || v[:4] != "src-" {
		return false
	}
	for _, c := range []byte(v[4:]) {
		if !((c >= 'a' && c <= 'f') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

func (s *Store) SyncAuthSources(ctx context.Context, specs []SourceSpec) error {
	seen := make(map[string]bool, len(specs))
	for _, spec := range specs {
		if !validSourceRef(spec.Ref) || spec.Priority < 0 || seen[spec.Ref] {
			return ErrInvalidSource
		}
		seen[spec.Ref] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := formatTime(time.Now())
	for _, spec := range specs {
		_, err = tx.ExecContext(ctx, `insert into auth_sources(source_ref,enabled,priority,credentials_available,created_at,updated_at) values(?,?,?,0,?,?) on conflict(source_ref) do update set enabled=excluded.enabled,priority=excluded.priority,credentials_available=case when excluded.enabled=1 then auth_sources.credentials_available else 0 end,account_ref=case when excluded.enabled=1 then auth_sources.account_ref else null end,updated_at=excluded.updated_at`, spec.Ref, spec.Enabled, spec.Priority, now, now)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `insert into auth_source_poll_health(source_ref,consecutive_failures,failure_kind,last_error_code,alert_state,updated_at) values(?,0,'','','ok',?) on conflict(source_ref) do nothing`, spec.Ref, now)
		if err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `select source_ref from auth_sources`)
	if err != nil {
		return err
	}
	var removed []string
	for rows.Next() {
		var ref string
		if err = rows.Scan(&ref); err != nil {
			_ = rows.Close()
			return err
		}
		if !seen[ref] {
			removed = append(removed, ref)
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, ref := range removed {
		if _, err = tx.ExecContext(ctx, `update auth_sources set enabled=0,credentials_available=0,account_ref=null,updated_at=? where source_ref=?`, now, ref); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ObserveAuthSource(ctx context.Context, sourceRef string, account resetwatch.Account, checkedAt time.Time) error {
	if !validSourceRef(sourceRef) || checkedAt.IsZero() {
		return ErrInvalidSource
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = validateEnabledSource(ctx, tx, sourceRef); err != nil {
		return err
	}
	if account.Ref == "" {
		_, err = tx.ExecContext(ctx, `update auth_sources set account_ref=null,credentials_available=0,last_identity_check=?,updated_at=? where source_ref=?`, formatTime(checkedAt), formatTime(checkedAt), sourceRef)
	} else {
		if err = ensureDiscoveredAccount(ctx, tx, resetwatch.ProviderCodex, account, checkedAt); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `update auth_sources set account_ref=?,credentials_available=1,last_identity_check=?,updated_at=? where source_ref=?`, account.Ref, formatTime(checkedAt), formatTime(checkedAt), sourceRef)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func ensureDiscoveredAccount(ctx context.Context, tx *sql.Tx, provider string, account resetwatch.Account, at time.Time) error {
	_, err := tx.ExecContext(ctx, `insert into accounts(account_ref,provider_id,label,email,plan,updated_at,alias,first_seen_at) values(?,?,?,?,?,?,'',?) on conflict(account_ref) do update set provider_id=excluded.provider_id,label=case when excluded.label<>'' then excluded.label else accounts.label end,email=case when excluded.email<>'' then excluded.email else accounts.email end,plan=case when excluded.plan<>'' then excluded.plan else accounts.plan end,updated_at=excluded.updated_at`, account.Ref, provider, account.Label, account.Email, account.Plan, formatTime(at), formatTime(at))
	return err
}

func (s *Store) ListSourceHealth(ctx context.Context) ([]SourceHealth, error) {
	rows, err := s.db.QueryContext(ctx, `select src.source_ref,src.enabled,src.priority,coalesce(src.account_ref,''),h.last_attempt_at,h.last_success_at,h.last_failure_at,h.consecutive_failures,h.failure_kind,h.last_error_code,h.alert_state,h.updated_at from auth_sources src join auth_source_poll_health h on h.source_ref=src.source_ref order by src.priority,src.source_ref`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SourceHealth
	for rows.Next() {
		var h SourceHealth
		var attempt, success, failure sql.NullString
		var updated string
		if err = rows.Scan(&h.SourceRef, &h.Enabled, &h.Priority, &h.AccountRef, &attempt, &success, &failure, &h.ConsecutiveFailures, &h.FailureKind, &h.LastErrorCode, &h.AlertState, &updated); err != nil {
			return nil, err
		}
		if h.LastAttemptAt, err = parseOptionalSourceTime(attempt); err != nil {
			return nil, err
		}
		if h.LastSuccessAt, err = parseOptionalSourceTime(success); err != nil {
			return nil, err
		}
		if h.LastFailureAt, err = parseOptionalSourceTime(failure); err != nil {
			return nil, err
		}
		h.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func validFailure(kind, code string) bool {
	kinds := map[string]bool{"": true, "legacy": true, "auth": true, "network": true, "provider": true, "internal": true}
	codes := map[string]bool{"": true, "unauthorized": true, "rate_limited": true, "timeout": true, "unavailable": true, "invalid_response": true, "internal": true, "auth_unavailable": true, "auth_rejected": true, "no_reset_windows": true, "request_failed": true, "persistence_failed": true, "source_disabled": true}
	return kinds[kind] && codes[code] && (kind != "" || code == "")
}

func validateEnabledSource(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ref string) error {
	if !validSourceRef(ref) {
		return ErrInvalidSource
	}
	var enabled int
	err := q.QueryRowContext(ctx, `select enabled from auth_sources where source_ref=?`, ref).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSourceMissing
	}
	if err != nil {
		return err
	}
	if enabled != 1 {
		return ErrSourceDisabled
	}
	return nil
}

func parseOptionalSourceTime(raw sql.NullString) (*time.Time, error) {
	if !raw.Valid {
		return nil, nil
	}
	v, err := time.Parse(time.RFC3339Nano, raw.String)
	if err != nil {
		return nil, err
	}
	return &v, nil
}
func latestSourceTime(values ...*time.Time) *time.Time {
	var latest *time.Time
	for _, v := range values {
		if v != nil && (latest == nil || v.After(*latest)) {
			copy := *v
			latest = &copy
		}
	}
	return latest
}
func monotonicSourceTime(current, candidate time.Time) string {
	if current.After(candidate) {
		return formatTime(current)
	}
	return formatTime(candidate)
}

func (s *Store) RecordSourcePollAttempt(ctx context.Context, ref string, attempt time.Time) error {
	if attempt.IsZero() {
		return ErrInvalidSource
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = validateEnabledSource(ctx, tx, ref); err != nil {
		return err
	}
	var ra, rs, rf sql.NullString
	var ru string
	if err = tx.QueryRowContext(ctx, `select last_attempt_at,last_success_at,last_failure_at,updated_at from auth_source_poll_health where source_ref=?`, ref).Scan(&ra, &rs, &rf, &ru); err != nil {
		return err
	}
	prev, e := parseOptionalSourceTime(ra)
	if e != nil {
		return e
	}
	success, e := parseOptionalSourceTime(rs)
	if e != nil {
		return e
	}
	failure, e := parseOptionalSourceTime(rf)
	if e != nil {
		return e
	}
	updated, e := time.Parse(time.RFC3339Nano, ru)
	if e != nil {
		return e
	}
	terminal := latestSourceTime(success, failure)
	if terminal != nil && !attempt.After(*terminal) {
		return ErrSourcePollStale
	}
	if prev != nil {
		pending := terminal == nil || prev.After(*terminal)
		if pending && attempt.Equal(*prev) {
			return nil
		}
		if !attempt.After(*prev) {
			return ErrSourcePollStale
		}
	}
	if _, err = tx.ExecContext(ctx, `update auth_source_poll_health set last_attempt_at=?,updated_at=? where source_ref=?`, formatTime(attempt), monotonicSourceTime(updated, attempt), ref); err != nil {
		return err
	}
	return tx.Commit()
}

func fencedSourceAttempt(ctx context.Context, tx *sql.Tx, ref string, attempt time.Time) (string, time.Time, error) {
	if attempt.IsZero() {
		return "", time.Time{}, ErrSourcePollStale
	}
	if err := validateEnabledSource(ctx, tx, ref); err != nil {
		return "", time.Time{}, err
	}
	var ra, rs, rf sql.NullString
	var ru string
	if err := tx.QueryRowContext(ctx, `select last_attempt_at,last_success_at,last_failure_at,updated_at from auth_source_poll_health where source_ref=?`, ref).Scan(&ra, &rs, &rf, &ru); err != nil {
		return "", time.Time{}, err
	}
	if !ra.Valid {
		return "", time.Time{}, ErrSourcePollStale
	}
	parsed, err := time.Parse(time.RFC3339Nano, ra.String)
	if err != nil {
		return "", time.Time{}, err
	}
	if !parsed.Equal(attempt) {
		return "", time.Time{}, ErrSourcePollStale
	}
	success, err := parseOptionalSourceTime(rs)
	if err != nil {
		return "", time.Time{}, err
	}
	failure, err := parseOptionalSourceTime(rf)
	if err != nil {
		return "", time.Time{}, err
	}
	terminal := latestSourceTime(success, failure)
	if terminal != nil && !attempt.After(*terminal) {
		return "", time.Time{}, ErrSourcePollStale
	}
	updated, err := time.Parse(time.RFC3339Nano, ru)
	return ra.String, updated, err
}

func (s *Store) RecordSourcePollSuccess(ctx context.Context, ref string, attempt, completed time.Time) error {
	return s.completeSourcePoll(ctx, ref, attempt, completed, "", "")
}
func (s *Store) RecordSourcePollFailure(ctx context.Context, ref string, attempt, completed time.Time, kind, code string) error {
	if !validFailure(kind, code) || kind == "" {
		return errors.New("invalid auth source failure classification")
	}
	return s.completeSourcePoll(ctx, ref, attempt, completed, kind, code)
}
func (s *Store) completeSourcePoll(ctx context.Context, ref string, attempt, completed time.Time, kind, code string) error {
	if completed.IsZero() || completed.Before(attempt) {
		return ErrSourcePollStale
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	raw, updated, err := fencedSourceAttempt(ctx, tx, ref, attempt)
	if err != nil {
		return err
	}
	var r sql.Result
	if kind == "" {
		r, err = tx.ExecContext(ctx, `update auth_source_poll_health set last_success_at=?,consecutive_failures=0,failure_kind='',last_error_code='',updated_at=? where source_ref=? and last_attempt_at=?`, formatTime(completed), monotonicSourceTime(updated, completed), ref, raw)
	} else {
		r, err = tx.ExecContext(ctx, `update auth_source_poll_health set last_failure_at=?,consecutive_failures=consecutive_failures+1,failure_kind=?,last_error_code=?,updated_at=? where source_ref=? and last_attempt_at=?`, formatTime(completed), kind, code, monotonicSourceTime(updated, completed), ref, raw)
	}
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrSourcePollStale
	}
	return tx.Commit()
}

func (s *Store) AbortSourcePollAttempt(ctx context.Context, ref string, attempt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	raw, updated, err := fencedSourceAttempt(ctx, tx, ref, attempt)
	if err != nil {
		return err
	}
	var success, failure sql.NullString
	if err = tx.QueryRowContext(ctx, `select last_success_at,last_failure_at from auth_source_poll_health where source_ref=?`, ref).Scan(&success, &failure); err != nil {
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
	r, err := tx.ExecContext(ctx, `update auth_source_poll_health set last_attempt_at=?,updated_at=? where source_ref=? and last_attempt_at=?`, restore, monotonicSourceTime(updated, time.Now()), ref, raw)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrSourcePollStale
	}
	return tx.Commit()
}

func (s *Store) CompareAndSwapSourceAlertState(ctx context.Context, ref, from, to string) (bool, error) {
	if (from != "ok" && from != "failing") || (to != "ok" && to != "failing") || from == to {
		return false, errors.New("invalid auth source alert transition")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = validateEnabledSource(ctx, tx, ref); err != nil {
		return false, err
	}
	var raw string
	if err = tx.QueryRowContext(ctx, `select updated_at from auth_source_poll_health where source_ref=?`, ref).Scan(&raw); err != nil {
		return false, err
	}
	updated, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return false, err
	}
	r, err := tx.ExecContext(ctx, `update auth_source_poll_health set alert_state=?,updated_at=? where source_ref=? and alert_state=?`, to, monotonicSourceTime(updated, time.Now()), ref, from)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return n == 1, nil
}
