package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/resetwatch"
)

var (
	ErrInvalidAccountSelector = errors.New("invalid account selector")
	ErrAccountMissing         = errors.New("account missing")
	ErrInvalidAccountAlias    = errors.New("invalid account alias")
)

type Account struct {
	ID                   string    `json:"id"`
	Ref                  string    `json:"-"`
	ProviderID           string    `json:"providerId"`
	Alias                string    `json:"alias,omitempty"`
	Email                string    `json:"email,omitempty"`
	Plan                 string    `json:"plan,omitempty"`
	FirstSeenAt          time.Time `json:"firstSeenAt"`
	LastSeenAt           time.Time `json:"lastSeenAt,omitempty"`
	CredentialsAvailable bool      `json:"credentialsAvailable"`
	SourceRefs           []string  `json:"-"`
}

func AccountID(provider, ref string) string {
	sum := sha256.Sum256([]byte(provider + "\x00" + ref))
	return "acct-" + hex.EncodeToString(sum[:10])
}

func (a Account) DisplayName() string {
	if a.Alias != "" {
		return a.Alias
	}
	if a.Email != "" {
		return a.Email
	}
	return a.ID
}

func validAccountID(v string) bool {
	if len(v) != 25 || !strings.HasPrefix(v, "acct-") {
		return false
	}
	_, err := hex.DecodeString(v[5:])
	return err == nil && strings.ToLower(v) == v
}

func validAlias(v string) bool {
	if len(v) < 1 || len(v) > 32 || v == "current" || validAccountID(v) || strings.HasPrefix(v, "acct-") {
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

func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx, `
select a.provider_id,a.account_ref,a.alias,a.email,a.plan,a.first_seen_at,
 coalesce((select max(o.observed_at) from limit_observations o where o.provider_id=a.provider_id and o.account_ref=a.account_ref),''),
 exists(select 1 from auth_sources src where src.account_ref=a.account_ref and src.enabled=1 and src.credentials_available=1)
from accounts a
order by coalesce((select max(o.observed_at) from limit_observations o where o.provider_id=a.provider_id and o.account_ref=a.account_ref),a.first_seen_at) desc,a.provider_id,a.account_ref`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Account
	for rows.Next() {
		var a Account
		var first, last string
		if err = rows.Scan(&a.ProviderID, &a.Ref, &a.Alias, &a.Email, &a.Plan, &first, &last, &a.CredentialsAvailable); err != nil {
			return nil, err
		}
		a.ID = AccountID(a.ProviderID, a.Ref)
		a.FirstSeenAt = parseDBTime(first)
		if last != "" {
			a.LastSeenAt = parseDBTime(last)
		}
		a.SourceRefs, err = s.accountSourceRefs(ctx, a.Ref)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) accountSourceRefs(ctx context.Context, accountRef string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `select source_ref from auth_sources where account_ref=? order by priority,source_ref`, accountRef)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var refs []string
	for rows.Next() {
		var ref string
		if err = rows.Scan(&ref); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func (s *Store) ResolveAccount(ctx context.Context, selector string) (Account, bool, error) {
	if selector == "" || selector == "current" {
		var provider, ref string
		err := s.db.QueryRowContext(ctx, `select a.provider_id,a.account_ref from auth_sources src join accounts a on a.account_ref=src.account_ref where src.enabled=1 and src.credentials_available=1 order by src.priority,src.source_ref limit 1`).Scan(&provider, &ref)
		if errors.Is(err, sql.ErrNoRows) {
			err = s.db.QueryRowContext(ctx, `select provider_id,account_ref from accounts order by coalesce((select max(observed_at) from limit_observations o where o.provider_id=accounts.provider_id and o.account_ref=accounts.account_ref),first_seen_at) desc,provider_id,account_ref limit 1`).Scan(&provider, &ref)
		}
		if errors.Is(err, sql.ErrNoRows) {
			return Account{}, false, nil
		}
		if err != nil {
			return Account{}, false, err
		}
		return s.accountByRef(ctx, provider, ref)
	}
	if !validAccountID(selector) && !validAlias(selector) {
		return Account{}, false, ErrInvalidAccountSelector
	}
	var provider, ref string
	var err error
	if validAccountID(selector) {
		rows, queryErr := s.db.QueryContext(ctx, `select provider_id,account_ref from accounts order by provider_id,account_ref`)
		if queryErr != nil {
			return Account{}, false, queryErr
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			if err = rows.Scan(&provider, &ref); err != nil {
				return Account{}, false, err
			}
			if AccountID(provider, ref) == selector {
				return s.accountByRef(ctx, provider, ref)
			}
		}
		return Account{}, false, rows.Err()
	}
	err = s.db.QueryRowContext(ctx, `select provider_id,account_ref from accounts where alias=?`, selector).Scan(&provider, &ref)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, false, nil
	}
	if err != nil {
		return Account{}, false, err
	}
	return s.accountByRef(ctx, provider, ref)
}

func (s *Store) accountByRef(ctx context.Context, provider, ref string) (Account, bool, error) {
	var a Account
	var first, last string
	err := s.db.QueryRowContext(ctx, `select provider_id,account_ref,alias,email,plan,first_seen_at,coalesce((select max(observed_at) from limit_observations where provider_id=accounts.provider_id and account_ref=accounts.account_ref),'') from accounts where provider_id=? and account_ref=?`, provider, ref).Scan(&a.ProviderID, &a.Ref, &a.Alias, &a.Email, &a.Plan, &first, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, false, nil
	}
	if err != nil {
		return Account{}, false, err
	}
	a.ID, a.FirstSeenAt = AccountID(a.ProviderID, a.Ref), parseDBTime(first)
	if last != "" {
		a.LastSeenAt = parseDBTime(last)
	}
	a.SourceRefs, err = s.accountSourceRefs(ctx, a.Ref)
	if err != nil {
		return Account{}, false, err
	}
	err = s.db.QueryRowContext(ctx, `select exists(select 1 from auth_sources where account_ref=? and enabled=1 and credentials_available=1)`, a.Ref).Scan(&a.CredentialsAvailable)
	return a, err == nil, err
}

func (s *Store) SetAccountAlias(ctx context.Context, selector, alias string) error {
	if !validAlias(alias) {
		return ErrInvalidAccountAlias
	}
	a, ok, err := s.ResolveAccount(ctx, selector)
	if err != nil {
		return err
	}
	if !ok {
		return ErrAccountMissing
	}
	r, err := s.db.ExecContext(ctx, `update accounts set alias=?,updated_at=? where provider_id=? and account_ref=?`, alias, formatTime(time.Now()), a.ProviderID, a.Ref)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return ErrInvalidAccountAlias
		}
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrAccountMissing
	}
	return nil
}

func (s *Store) LoadLatestObservationForAccount(ctx context.Context, selector string) (resetwatch.Observation, bool, error) {
	a, ok, err := s.ResolveAccount(ctx, selector)
	if err != nil || !ok {
		return resetwatch.Observation{}, false, err
	}
	return s.loadLatestObservation(ctx, a.ProviderID, a.Ref)
}
