package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	ErrInvalidProfile          = errors.New("invalid profile")
	ErrProfileMissing          = errors.New("profile missing")
	ErrProfileDisabled         = errors.New("profile disabled")
	ErrProfileProviderMismatch = errors.New("profile provider mismatch")
	ErrProfileAccountOwned     = errors.New("provider account is owned by another profile")
	ErrProfileAccountUnbound   = errors.New("provider account is not bound to a profile")
)

func validProfileRef(v string) bool {
	if len(v) < 1 || len(v) > 32 {
		return false
	}
	previousDash := true
	for _, c := range []byte(v) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			previousDash = false
			continue
		}
		if c != '-' || previousDash {
			return false
		}
		previousDash = true
	}
	return !previousDash
}

type ProfileSpec struct {
	ProfileRef, ProviderID, Label string
	Enabled, IsDefault            bool
}

func (s *Store) SyncProfiles(ctx context.Context, specs []ProfileSpec) error {
	if len(specs) == 0 {
		return errors.New("at least one profile is required")
	}
	seen := map[string]bool{}
	defaults := 0
	for _, p := range specs {
		if !validProfileRef(p.ProfileRef) || len(p.ProviderID) < 1 || len(p.ProviderID) > 64 || len(p.Label) < 1 || len(p.Label) > 128 || seen[p.ProfileRef] {
			return ErrInvalidProfile
		}
		seen[p.ProfileRef] = true
		if p.Enabled && p.IsDefault {
			defaults++
		}
		if p.IsDefault && !p.Enabled {
			return errors.New("default profile must be enabled")
		}
	}
	if defaults != 1 {
		return errors.New("exactly one enabled default profile is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := formatTime(time.Now())
	if _, err = tx.ExecContext(ctx, `update profiles set is_default=0`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `update profiles set enabled=0,is_default=0,updated_at=?`, now); err != nil {
		return err
	}
	for _, p := range specs {
		var provider string
		scan := tx.QueryRowContext(ctx, `select provider_id from profiles where profile_ref=?`, p.ProfileRef).Scan(&provider)
		if scan == nil && provider != p.ProviderID {
			return ErrProfileProviderMismatch
		}
		if scan != nil && !errors.Is(scan, sql.ErrNoRows) {
			return scan
		}
		_, err = tx.ExecContext(ctx, `insert into profiles(profile_ref,provider_id,label,enabled,is_default,created_at,updated_at) values(?,?,?,?,?,?,?) on conflict(profile_ref) do update set provider_id=excluded.provider_id,label=excluded.label,enabled=excluded.enabled,is_default=excluded.is_default,updated_at=excluded.updated_at`, p.ProfileRef, p.ProviderID, p.Label, p.Enabled, p.IsDefault, now, now)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `insert into profile_poll_health(profile_ref,consecutive_failures,failure_kind,last_error_code,alert_state,updated_at) values(?,0,'','','ok',?) on conflict(profile_ref) do nothing`, p.ProfileRef, now)
		if err != nil {
			return err
		}
	}
	var count int
	if err = tx.QueryRowContext(ctx, `select count(*) from profiles where enabled=1 and is_default=1`).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("profile sync produced %d defaults", count)
	}
	return tx.Commit()
}

func bindProfileAccount(ctx context.Context, tx *sql.Tx, profile, provider, account string, at time.Time) error {
	if !validProfileRef(profile) {
		return ErrInvalidProfile
	}
	var enabled int
	var configuredProvider string
	if err := tx.QueryRowContext(ctx, `select provider_id,enabled from profiles where profile_ref=?`, profile).Scan(&configuredProvider, &enabled); errors.Is(err, sql.ErrNoRows) {
		return ErrProfileMissing
	} else if err != nil {
		return err
	}
	if enabled != 1 {
		return ErrProfileDisabled
	}
	if configuredProvider != provider {
		return ErrProfileProviderMismatch
	}
	var owner string
	err := tx.QueryRowContext(ctx, `select profile_ref from profile_accounts where provider_id=? and account_ref=?`, provider, account).Scan(&owner)
	if err == nil && owner != profile {
		return ErrProfileAccountOwned
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	stamp := formatTime(at)
	var existingLast string
	if scanErr := tx.QueryRowContext(ctx, `select last_seen_at from profile_accounts where profile_ref=? and provider_id=? and account_ref=?`, profile, provider, account).Scan(&existingLast); scanErr == nil {
		parsed, parseErr := time.Parse(time.RFC3339Nano, existingLast)
		if parseErr != nil {
			return parseErr
		}
		if parsed.After(at) {
			stamp = existingLast
		}
	} else if !errors.Is(scanErr, sql.ErrNoRows) {
		return scanErr
	}
	_, err = tx.ExecContext(ctx, `insert into profile_accounts(profile_ref,provider_id,account_ref,is_current,first_seen_at,last_seen_at) values(?,?,?,0,?,?) on conflict(profile_ref,provider_id,account_ref) do update set last_seen_at=excluded.last_seen_at`, profile, provider, account, formatTime(at), stamp)
	if err != nil {
		var durableOwner string
		if ownerErr := tx.QueryRowContext(ctx, `select profile_ref from profile_accounts where provider_id=? and account_ref=?`, provider, account).Scan(&durableOwner); ownerErr == nil && durableOwner != profile {
			return ErrProfileAccountOwned
		}
		return err
	}
	rows, err := tx.QueryContext(ctx, `select account_ref,last_seen_at from profile_accounts where profile_ref=?`, profile)
	if err != nil {
		return err
	}
	current := ""
	var latest time.Time
	for rows.Next() {
		var ref, raw string
		if err = rows.Scan(&ref, &raw); err != nil {
			_ = rows.Close()
			return err
		}
		parsed, e := time.Parse(time.RFC3339Nano, raw)
		if e != nil {
			_ = rows.Close()
			return e
		}
		if current == "" || parsed.After(latest) || (parsed.Equal(latest) && ref < current) {
			current, latest = ref, parsed
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	if _, err = tx.ExecContext(ctx, `update profile_accounts set is_current=0 where profile_ref=?`, profile); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `update profile_accounts set is_current=1 where profile_ref=? and account_ref=?`, profile, current)
	return err
}

func resolveDefaultProfile(ctx context.Context, tx *sql.Tx, provider string) (string, error) {
	var p string
	err := tx.QueryRowContext(ctx, `select profile_ref from profiles where enabled=1 and is_default=1 and provider_id=?`, provider).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrProfileMissing
	}
	return p, err
}
