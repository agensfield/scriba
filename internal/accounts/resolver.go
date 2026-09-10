package accounts

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	remotecodex "github.com/agensfield/scriba/internal/remote/codex"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
)

var (
	ErrAccountNotFound        = errors.New("account not found")
	ErrCredentialsUnavailable = errors.New("credentials unavailable for account")
)

type Registry interface {
	SyncAuthSources(context.Context, []store.SourceSpec) error
	ObserveAuthSource(context.Context, string, resetwatch.Account, time.Time) error
	ListAccounts(context.Context) ([]store.Account, error)
	ResolveAccount(context.Context, string) (store.Account, bool, error)
	SetAccountAlias(context.Context, string, string) error
}

// Resolver joins durable account history with current local credential
// sources. It never refreshes credentials or changes the process environment.
type Resolver struct {
	registry Registry
	sources  []Source
	clock    func() time.Time
}

func New(registry Registry, sources []Source) *Resolver {
	copySources := append([]Source(nil), sources...)
	slices.SortStableFunc(copySources, func(a, b Source) int { return cmp.Compare(a.Priority, b.Priority) })
	return &Resolver{registry: registry, sources: copySources, clock: time.Now}
}

func (r *Resolver) Sources() []Source {
	return append([]Source(nil), r.sources...)
}

func (r *Resolver) Sync(ctx context.Context) error {
	specs := make([]store.SourceSpec, 0, len(r.sources))
	for _, source := range r.sources {
		specs = append(specs, store.SourceSpec{Ref: source.Ref, Enabled: true, Priority: source.Priority})
	}
	return r.registry.SyncAuthSources(ctx, specs)
}

// Reconcile records current source identities without making provider
// requests. Missing or invalid credentials clear only that source binding.
func (r *Resolver) Reconcile(ctx context.Context) ([]Inspection, error) {
	if err := r.Sync(ctx); err != nil {
		return nil, err
	}
	checkedAt := r.clock().UTC()
	inspections := make([]Inspection, 0, len(r.sources))
	var reconcileErr error
	for _, source := range r.sources {
		inspection := Inspect(source)
		inspections = append(inspections, inspection)
		account := inspection.Account
		if !inspection.CredentialsAvailable {
			account = resetwatch.Account{}
		}
		if err := r.registry.ObserveAuthSource(ctx, source.Ref, account, checkedAt); err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
		}
	}
	return inspections, reconcileErr
}

func (r *Resolver) Accounts(ctx context.Context) ([]store.Account, error) {
	result, err := r.registry.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	available := make(map[string]bool, len(r.sources))
	for _, source := range r.sources {
		inspection := Inspect(source)
		if inspection.CredentialsAvailable {
			available[inspection.Account.Ref] = true
		}
	}
	for i := range result {
		result[i].CredentialsAvailable = available[result[i].Ref]
	}
	return result, nil
}

func (r *Resolver) Resolve(ctx context.Context, selector string) (store.Account, error) {
	if selector == "" || selector == "current" {
		known, err := r.registry.ListAccounts(ctx)
		if err != nil {
			return store.Account{}, err
		}
		byRef := make(map[string]store.Account, len(known))
		for _, account := range known {
			byRef[account.Ref] = account
		}
		currentUnknown := false
		for _, source := range r.sources {
			inspection := Inspect(source)
			if !inspection.CredentialsAvailable {
				continue
			}
			if account, ok := byRef[inspection.Account.Ref]; ok {
				account.CredentialsAvailable = true
				return account, nil
			}
			currentUnknown = true
		}
		if currentUnknown {
			return store.Account{}, ErrAccountNotFound
		}
	}
	account, ok, err := r.registry.ResolveAccount(ctx, selector)
	if err != nil {
		return store.Account{}, err
	}
	if !ok {
		return store.Account{}, ErrAccountNotFound
	}
	account.CredentialsAvailable = false
	for _, source := range r.sources {
		inspection := Inspect(source)
		if inspection.CredentialsAvailable && inspection.Account.Ref == account.Ref {
			account.CredentialsAvailable = true
			break
		}
	}
	return account, nil
}

func (r *Resolver) SetAlias(ctx context.Context, selector, alias string) error {
	return r.registry.SetAccountAlias(ctx, selector, alias)
}

type LiveAccount struct {
	Account store.Account `json:"-"`
	Source  Source        `json:"-"`
}

func (a LiveAccount) FetchOptions() remotecodex.FetchOptions {
	return remotecodex.FetchOptions{AuthPaths: []string{a.Source.Path}, ExpectedAccountID: a.Account.Ref}
}

// ResolveLive selects an exact account and a currently usable source for it.
// Historical accounts remain resolvable through Resolve even when this fails.
func (r *Resolver) ResolveLive(ctx context.Context, selector string) (LiveAccount, error) {
	account, err := r.Resolve(ctx, selector)
	if err != nil {
		return LiveAccount{}, err
	}
	for _, source := range r.sources {
		inspection := Inspect(source)
		if inspection.CredentialsAvailable && inspection.Account.Ref == account.Ref {
			return LiveAccount{Account: account, Source: inspection.Source}, nil
		}
	}
	return LiveAccount{}, ErrCredentialsUnavailable
}
