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
	ErrRegistryUnavailable    = errors.New("account registry unavailable for write")
)

type Registry interface {
	SyncAuthSources(context.Context, []store.SourceSpec) error
	ObserveAuthSource(context.Context, string, resetwatch.Account, time.Time) error
	ListAccounts(context.Context) ([]store.Account, error)
	ResolveAccount(context.Context, string) (store.Account, bool, error)
	SetAccountAlias(context.Context, string, string) error
	RegisterAuthSourceAccountAlias(context.Context, store.SourceSpec, resetwatch.Account, string, time.Time) error
}

// Resolver joins durable account history with current local credential
// sources. It never refreshes credentials or changes the process environment.
type Resolver struct {
	registry Registry
	sources  []Source
	clock    func() time.Time
	writable bool
}

func New(registry Registry, sources []Source) *Resolver {
	writable := registry != nil
	if registry == nil {
		registry = emptyRegistry{}
	}
	copySources := append([]Source(nil), sources...)
	slices.SortStableFunc(copySources, func(a, b Source) int { return cmp.Compare(a.Priority, b.Priority) })
	return &Resolver{registry: registry, sources: copySources, clock: time.Now, writable: writable}
}

func (r *Resolver) Sources() []Source {
	return append([]Source(nil), r.sources...)
}

func (r *Resolver) Sync(ctx context.Context) error {
	if !r.writable {
		return ErrRegistryUnavailable
	}
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
	snapshot, err := r.snapshot(ctx)
	return snapshot.accounts, err
}

func (r *Resolver) Resolve(ctx context.Context, selector string) (store.Account, error) {
	snapshot, err := r.snapshot(ctx)
	if err != nil {
		return store.Account{}, err
	}
	account, _, err := r.resolveSnapshot(ctx, snapshot, selector)
	return account, err
}

func (r *Resolver) SetAlias(ctx context.Context, selector, alias string) error {
	if !r.writable {
		return ErrRegistryUnavailable
	}
	snapshot, err := r.snapshot(ctx)
	if err != nil {
		return err
	}
	account, source, err := r.resolveSnapshot(ctx, snapshot, selector)
	if err != nil {
		return err
	}
	if (selector == "" || selector == "current") && source == nil {
		return ErrCredentialsUnavailable
	}
	if !snapshot.durable[account.Ref] {
		if source == nil {
			return ErrCredentialsUnavailable
		}
		spec := store.SourceSpec{Ref: source.Ref, Enabled: true, Priority: source.Priority}
		observed := resetwatch.Account{Ref: account.Ref, Email: account.Email, Plan: account.Plan}
		return r.registry.RegisterAuthSourceAccountAlias(ctx, spec, observed, alias, r.clock().UTC())
	}
	return r.registry.SetAccountAlias(ctx, account.ID, alias)
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
	snapshot, err := r.snapshot(ctx)
	if err != nil {
		return LiveAccount{}, err
	}
	account, source, err := r.resolveSnapshot(ctx, snapshot, selector)
	if err != nil {
		return LiveAccount{}, err
	}
	if source != nil {
		return LiveAccount{Account: account, Source: *source}, nil
	}
	return LiveAccount{}, ErrCredentialsUnavailable
}

type emptyRegistry struct{}

func (emptyRegistry) SyncAuthSources(context.Context, []store.SourceSpec) error {
	return ErrRegistryUnavailable
}

func (emptyRegistry) ObserveAuthSource(context.Context, string, resetwatch.Account, time.Time) error {
	return ErrRegistryUnavailable
}

func (emptyRegistry) ListAccounts(context.Context) ([]store.Account, error) {
	return nil, nil
}

func (emptyRegistry) ResolveAccount(context.Context, string) (store.Account, bool, error) {
	return store.Account{}, false, nil
}

func (emptyRegistry) SetAccountAlias(context.Context, string, string) error {
	return ErrRegistryUnavailable
}

func (emptyRegistry) RegisterAuthSourceAccountAlias(context.Context, store.SourceSpec, resetwatch.Account, string, time.Time) error {
	return ErrRegistryUnavailable
}

type resolverSnapshot struct {
	accounts []store.Account
	byRef    map[string]store.Account
	sources  map[string]Source
	current  string
	durable  map[string]bool
}

func (r *Resolver) snapshot(ctx context.Context) (resolverSnapshot, error) {
	durable, err := r.registry.ListAccounts(ctx)
	if err != nil {
		return resolverSnapshot{}, err
	}
	snapshot := resolverSnapshot{
		accounts: append([]store.Account(nil), durable...),
		byRef:    make(map[string]store.Account, len(durable)),
		sources:  make(map[string]Source, len(r.sources)),
		durable:  make(map[string]bool, len(durable)),
	}
	indexes := make(map[string]int, len(durable))
	for i, account := range snapshot.accounts {
		account.CredentialsAvailable = false
		account.SourceRefs = nil
		snapshot.accounts[i] = account
		snapshot.byRef[account.Ref] = account
		snapshot.durable[account.Ref] = true
		indexes[account.Ref] = i
	}
	for _, source := range r.sources {
		inspection := Inspect(source)
		if !inspection.CredentialsAvailable {
			continue
		}
		ref := inspection.Account.Ref
		if snapshot.current == "" {
			snapshot.current = ref
		}
		if _, exists := snapshot.sources[ref]; !exists {
			snapshot.sources[ref] = source
		}
		account, exists := snapshot.byRef[ref]
		if !exists {
			account = store.Account{
				ID:                   store.AccountID(resetwatch.ProviderCodex, ref),
				Ref:                  ref,
				ProviderID:           resetwatch.ProviderCodex,
				Email:                inspection.Account.Email,
				CredentialsAvailable: true,
				SourceRefs:           []string{source.Ref},
			}
			indexes[ref] = len(snapshot.accounts)
			snapshot.accounts = append(snapshot.accounts, account)
		} else {
			account.CredentialsAvailable = true
			if account.Email == "" {
				account.Email = inspection.Account.Email
			}
			account.SourceRefs = append(account.SourceRefs, source.Ref)
			snapshot.accounts[indexes[ref]] = account
		}
		snapshot.byRef[ref] = account
	}
	return snapshot, nil
}

func (r *Resolver) resolveSnapshot(ctx context.Context, snapshot resolverSnapshot, selector string) (store.Account, *Source, error) {
	if selector == "" || selector == "current" {
		if snapshot.current != "" {
			account := snapshot.byRef[snapshot.current]
			source := snapshot.sources[account.Ref]
			return account, &source, nil
		}
		account, ok, err := r.registry.ResolveAccount(ctx, selector)
		if err != nil {
			return store.Account{}, nil, err
		}
		if !ok {
			return store.Account{}, nil, ErrAccountNotFound
		}
		if overlaid, exists := snapshot.byRef[account.Ref]; exists {
			account = overlaid
		}
		return account, nil, nil
	}
	for _, account := range snapshot.accounts {
		if selector == account.ID || selector == account.Alias {
			if source, ok := snapshot.sources[account.Ref]; ok {
				return account, &source, nil
			}
			return account, nil, nil
		}
	}
	_, _, err := r.registry.ResolveAccount(ctx, selector)
	if err != nil {
		return store.Account{}, nil, err
	}
	return store.Account{}, nil, ErrAccountNotFound
}
