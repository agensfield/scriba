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
	accounts, _, err := listAccountsQuery(ctx, s.db)
	return accounts, err
}

type accountQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listAccountsQuery(ctx context.Context, q accountQueryer) ([]Account, int, error) {
	rows, err := q.QueryContext(ctx, `
with current_account as (
 select account_ref from auth_sources
 where enabled=1 and credentials_available=1
 order by priority,source_ref limit 1
)
select a.provider_id,a.account_ref,a.alias,a.email,a.plan,a.first_seen_at,
 coalesce((select max(o.observed_at) from limit_observations o where o.provider_id=a.provider_id and o.account_ref=a.account_ref),''),
 exists(select 1 from current_account current where current.account_ref=a.account_ref),
 src.source_ref,coalesce(src.enabled,0),coalesce(src.credentials_available,0)
from accounts a
	left join auth_sources src on src.account_ref=a.account_ref
order by coalesce((select max(o.observed_at) from limit_observations o where o.provider_id=a.provider_id and o.account_ref=a.account_ref),a.first_seen_at) desc,a.provider_id,a.account_ref,src.priority,src.source_ref`)
	if err != nil {
		return nil, -1, err
	}
	defer func() { _ = rows.Close() }()
	var out []Account
	currentIndex := -1
	for rows.Next() {
		var a Account
		var first, last string
		var source sql.NullString
		var isCurrent, sourceEnabled, sourceAvailable bool
		if err = rows.Scan(&a.ProviderID, &a.Ref, &a.Alias, &a.Email, &a.Plan, &first, &last, &isCurrent, &source, &sourceEnabled, &sourceAvailable); err != nil {
			return nil, -1, err
		}
		if len(out) == 0 || out[len(out)-1].ProviderID != a.ProviderID || out[len(out)-1].Ref != a.Ref {
			a.ID = AccountID(a.ProviderID, a.Ref)
			a.FirstSeenAt = parseDBTime(first)
			if last != "" {
				a.LastSeenAt = parseDBTime(last)
			}
			out = append(out, a)
			if isCurrent {
				currentIndex = len(out) - 1
			}
		}
		current := &out[len(out)-1]
		if source.Valid {
			current.SourceRefs = append(current.SourceRefs, source.String)
			current.CredentialsAvailable = current.CredentialsAvailable || (sourceEnabled && sourceAvailable)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, -1, err
	}
	return out, currentIndex, nil
}

func (s *Store) ResolveAccount(ctx context.Context, selector string) (Account, bool, error) {
	if !validAccountID(selector) && !validAlias(selector) {
		if selector != "" && selector != "current" {
			return Account{}, false, ErrInvalidAccountSelector
		}
	}
	accounts, currentIndex, err := listAccountsQuery(ctx, s.db)
	if err != nil {
		return Account{}, false, err
	}
	selected := -1
	if selector == "" || selector == "current" {
		selected = currentIndex
		if selected < 0 && len(accounts) > 0 {
			selected = 0
		}
	} else {
		for i := range accounts {
			if accounts[i].ID == selector || accounts[i].Alias == selector {
				selected = i
				break
			}
		}
	}
	if selected < 0 {
		return Account{}, false, nil
	}
	return accounts[selected], true, nil
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
