package accounts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
)

type fakeRegistry struct {
	accounts      []store.Account
	syncs         int
	observed      []observedSource
	aliasSelector string
	aliasValue    string
	registered    []registeredAlias
}

type registeredAlias struct {
	spec    store.SourceSpec
	account resetwatch.Account
	alias   string
}

type observedSource struct {
	ref     string
	account resetwatch.Account
}

func (f *fakeRegistry) SyncAuthSources(context.Context, []store.SourceSpec) error {
	f.syncs++
	return nil
}

func (f *fakeRegistry) ObserveAuthSource(_ context.Context, ref string, account resetwatch.Account, _ time.Time) error {
	f.observed = append(f.observed, observedSource{ref: ref, account: account})
	return nil
}

func (f *fakeRegistry) ListAccounts(context.Context) ([]store.Account, error) {
	return append([]store.Account(nil), f.accounts...), nil
}

func (f *fakeRegistry) ResolveAccount(_ context.Context, selector string) (store.Account, bool, error) {
	for _, account := range f.accounts {
		if selector == "" || selector == "current" || selector == account.ID || selector == account.Alias {
			return account, true, nil
		}
	}
	return store.Account{}, false, nil
}

func (f *fakeRegistry) SetAccountAlias(_ context.Context, selector, alias string) error {
	f.aliasSelector = selector
	f.aliasValue = alias
	for i := range f.accounts {
		if f.accounts[i].ID == selector {
			f.accounts[i].Alias = alias
		}
	}
	return nil
}

func (f *fakeRegistry) RegisterAuthSourceAccountAlias(_ context.Context, spec store.SourceSpec, account resetwatch.Account, alias string, _ time.Time) error {
	f.registered = append(f.registered, registeredAlias{spec: spec, account: account, alias: alias})
	f.accounts = append(f.accounts, store.Account{ID: store.AccountID("codex", account.Ref), Ref: account.Ref, ProviderID: "codex", Alias: alias, Email: account.Email, CredentialsAvailable: true, SourceRefs: []string{spec.Ref}})
	return nil
}

func writeResolverAuth(t *testing.T, dir, name, account string) Source {
	t.Helper()
	path := filepath.Join(dir, name+".json")
	payload := fmt.Sprintf(`{"tokens":{"access_token":"token-%s","account_id":%q}}`, name, account)
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	return Source{Ref: SourceRef(path), Path: path}
}

func TestResolverReadsNeverReconcileAndRouteExactAccount(t *testing.T) {
	dir := t.TempDir()
	sourceA := writeResolverAuth(t, dir, "a", "private-a")
	sourceB := writeResolverAuth(t, dir, "b", "private-b")
	sourceB.Priority = 1
	accountA := store.Account{ID: store.AccountID("codex", "private-a"), Ref: "private-a", ProviderID: "codex", Alias: "personal", CredentialsAvailable: true}
	accountB := store.Account{ID: store.AccountID("codex", "private-b"), Ref: "private-b", ProviderID: "codex", Alias: "work"}
	registry := &fakeRegistry{accounts: []store.Account{accountA, accountB}}
	resolver := New(registry, []Source{sourceA, sourceB})
	authBefore, err := os.ReadFile(sourceA.Path)
	if err != nil {
		t.Fatal(err)
	}

	listed, err := resolver.Accounts(context.Background())
	if err != nil || len(listed) != 2 || !listed[0].CredentialsAvailable || !listed[1].CredentialsAvailable {
		t.Fatalf("accounts=%+v err=%v", listed, err)
	}
	current, err := resolver.Resolve(context.Background(), "current")
	if err != nil || current.Ref != "private-a" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	live, err := resolver.ResolveLive(context.Background(), "work")
	if err != nil || live.Account.Ref != "private-b" || live.Source.Path != sourceB.Path {
		t.Fatalf("live=%+v err=%v", live, err)
	}
	opts := live.FetchOptions()
	if len(opts.AuthPaths) != 1 || opts.AuthPaths[0] != sourceB.Path || opts.ExpectedAccountID != "private-b" {
		t.Fatalf("fetch options=%+v", opts)
	}
	if registry.syncs != 0 || len(registry.observed) != 0 {
		t.Fatalf("read path wrote state: syncs=%d observed=%+v", registry.syncs, registry.observed)
	}
	authAfter, err := os.ReadFile(sourceA.Path)
	if err != nil || !bytes.Equal(authBefore, authAfter) {
		t.Fatalf("read path changed auth: err=%v before=%q after=%q", err, authBefore, authAfter)
	}
	if _, err := resolver.Resolve(context.Background(), "unknown"); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("unknown selector err=%v", err)
	}
}

func TestResolverKeepsHistoricalReadsWhenCredentialsDisappear(t *testing.T) {
	dir := t.TempDir()
	source := writeResolverAuth(t, dir, "a", "private-a")
	account := store.Account{ID: store.AccountID("codex", "private-a"), Ref: "private-a", ProviderID: "codex", Alias: "personal", CredentialsAvailable: true}
	registry := &fakeRegistry{accounts: []store.Account{account}}
	resolver := New(registry, []Source{source})
	if err := os.Remove(source.Path); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.Resolve(context.Background(), "personal")
	if err != nil || resolved.CredentialsAvailable {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	if _, err := resolver.ResolveLive(context.Background(), "personal"); !errors.Is(err, ErrCredentialsUnavailable) {
		t.Fatalf("live err=%v", err)
	}
	current, err := resolver.Resolve(context.Background(), "current")
	if err != nil || current.CredentialsAvailable {
		t.Fatalf("historical current=%+v err=%v", current, err)
	}
	if err := resolver.SetAlias(context.Background(), "current", "renamed"); !errors.Is(err, ErrCredentialsUnavailable) {
		t.Fatalf("logged-out current alias err=%v", err)
	}
	if registry.syncs != 0 || len(registry.observed) != 0 {
		t.Fatalf("historical read wrote state: syncs=%d observed=%+v", registry.syncs, registry.observed)
	}
}

func TestResolverIncludesColdAuthenticatedAccountWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	source := writeResolverAuth(t, dir, "cold", "private-cold")
	registry := &fakeRegistry{}
	resolver := New(registry, []Source{source})
	listed, err := resolver.Accounts(context.Background())
	if err != nil || len(listed) != 1 {
		t.Fatalf("accounts=%+v err=%v", listed, err)
	}
	account := listed[0]
	if account.ID != store.AccountID("codex", "private-cold") || account.Ref != "private-cold" || !account.CredentialsAvailable || !account.FirstSeenAt.IsZero() || !account.LastSeenAt.IsZero() {
		t.Fatalf("cold account=%+v", account)
	}
	live, err := resolver.ResolveLive(context.Background(), "current")
	if err != nil || live.Account.ID != account.ID || live.Source.Ref != source.Ref {
		t.Fatalf("live=%+v err=%v", live, err)
	}
	if registry.syncs != 0 || len(registry.observed) != 0 || len(registry.accounts) != 0 {
		t.Fatalf("cold read wrote state: registry=%+v", registry)
	}
}

func TestResolverColdOverlayIsReadOnlyWithRealStore(t *testing.T) {
	dir := t.TempDir()
	source := writeResolverAuth(t, dir, "cold-real", "private-cold")
	st, err := store.Open(filepath.Join(dir, "store.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	resolver := New(st, []Source{source})
	listed, err := resolver.Accounts(context.Background())
	if err != nil || len(listed) != 1 || listed[0].Ref != "private-cold" || !listed[0].CredentialsAvailable {
		t.Fatalf("accounts=%+v err=%v", listed, err)
	}
	live, err := resolver.ResolveLive(context.Background(), "current")
	if err != nil || live.Account.Ref != "private-cold" {
		t.Fatalf("live=%+v err=%v", live, err)
	}
	durable, err := st.ListAccounts(context.Background())
	if err != nil || len(durable) != 0 {
		t.Fatalf("read overlay persisted accounts=%+v err=%v", durable, err)
	}
	health, err := st.ListSourceHealth(context.Background())
	if err != nil || len(health) != 0 {
		t.Fatalf("read overlay persisted sources=%+v err=%v", health, err)
	}
}

func TestResolverColdOverlayNeedsNoStoreForReadOrLiveUse(t *testing.T) {
	dir := t.TempDir()
	source := writeResolverAuth(t, dir, "storeless", "private-cold")
	resolver := New(nil, []Source{source})
	listed, err := resolver.Accounts(context.Background())
	if err != nil || len(listed) != 1 || listed[0].Ref != "private-cold" || !listed[0].CredentialsAvailable {
		t.Fatalf("accounts=%+v err=%v", listed, err)
	}
	live, err := resolver.ResolveLive(context.Background(), "current")
	if err != nil || live.Account.ID != listed[0].ID || live.Source.Ref != source.Ref {
		t.Fatalf("live=%+v err=%v", live, err)
	}
	if _, err := resolver.Resolve(context.Background(), "unknown"); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("unknown err=%v", err)
	}
	if _, err := resolver.Reconcile(context.Background()); !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("reconcile err=%v", err)
	}
	if err := resolver.SetAlias(context.Background(), "current", "work"); !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("alias err=%v", err)
	}
}

func TestResolverCurrentNeverFallsBackAcrossUnreconciledRotation(t *testing.T) {
	dir := t.TempDir()
	primaryB := writeResolverAuth(t, dir, "primary", "private-b")
	secondaryA := writeResolverAuth(t, dir, "secondary", "private-a")
	secondaryA.Priority = 1
	accountA := store.Account{ID: store.AccountID("codex", "private-a"), Ref: "private-a", ProviderID: "codex"}
	registry := &fakeRegistry{accounts: []store.Account{accountA}}
	resolver := New(registry, []Source{primaryB, secondaryA})
	current, err := resolver.Resolve(context.Background(), "current")
	if err != nil || current.Ref != "private-b" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	live, err := resolver.ResolveLive(context.Background(), "current")
	if err != nil || live.Account.Ref != "private-b" || live.Source.Ref != primaryB.Ref {
		t.Fatalf("live=%+v err=%v", live, err)
	}
	if registry.syncs != 0 || len(registry.observed) != 0 {
		t.Fatalf("current read reconciled state: syncs=%d observed=%+v", registry.syncs, registry.observed)
	}
}

func TestResolverPrimaryColdIdentityWinsWithRealStore(t *testing.T) {
	dir := t.TempDir()
	primaryB := writeResolverAuth(t, dir, "primary-real", "private-b")
	secondaryA := writeResolverAuth(t, dir, "secondary-real", "private-a")
	secondaryA.Priority = 1
	st, err := store.Open(filepath.Join(dir, "store.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	specs := []store.SourceSpec{{Ref: primaryB.Ref, Enabled: true, Priority: 0}, {Ref: secondaryA.Ref, Enabled: true, Priority: 1}}
	if err := st.SyncAuthSources(context.Background(), specs); err != nil {
		t.Fatal(err)
	}
	if err := st.ObserveAuthSource(context.Background(), secondaryA.Ref, resetwatch.Account{Ref: "private-a"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	resolver := New(st, []Source{primaryB, secondaryA})
	current, err := resolver.ResolveLive(context.Background(), "current")
	if err != nil || current.Account.Ref != "private-b" || current.Source.Ref != primaryB.Ref {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	durable, err := st.ListAccounts(context.Background())
	if err != nil || len(durable) != 1 || durable[0].Ref != "private-a" {
		t.Fatalf("current read mutated durable accounts=%+v err=%v", durable, err)
	}
}

func TestResolverReconcileDiscoversRotationAndClearsUnavailableSource(t *testing.T) {
	dir := t.TempDir()
	sourceA := writeResolverAuth(t, dir, "a", "private-a")
	sourceB := writeResolverAuth(t, dir, "b", "")
	sourceB.Priority = 1
	registry := &fakeRegistry{}
	resolver := New(registry, []Source{sourceA, sourceB})
	if _, err := resolver.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if registry.syncs != 1 || len(registry.observed) != 2 || registry.observed[0].account.Ref != "private-a" || registry.observed[1].account.Ref != "" {
		t.Fatalf("syncs=%d observed=%+v", registry.syncs, registry.observed)
	}
	writeResolverAuth(t, dir, "a", "private-b")
	if _, err := resolver.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if registry.observed[2].account.Ref != "private-b" {
		t.Fatalf("rotation not observed: %+v", registry.observed)
	}
}

func TestResolverAliasIsTheOnlyOrdinaryMutation(t *testing.T) {
	account := store.Account{ID: store.AccountID("codex", "private-a"), Ref: "private-a", ProviderID: "codex", Alias: "personal"}
	registry := &fakeRegistry{accounts: []store.Account{account}}
	resolver := New(registry, nil)
	if err := resolver.SetAlias(context.Background(), account.ID, "work"); err != nil {
		t.Fatal(err)
	}
	if registry.aliasSelector != account.ID || registry.aliasValue != "work" || registry.syncs != 0 || len(registry.observed) != 0 || len(registry.registered) != 0 {
		t.Fatalf("registry=%+v", registry)
	}
}

func TestResolverAliasCurrentRegistersColdIdentityWithoutRenamingStaleAccount(t *testing.T) {
	dir := t.TempDir()
	source := writeResolverAuth(t, dir, "current", "private-b")
	accountA := store.Account{ID: store.AccountID("codex", "private-a"), Ref: "private-a", ProviderID: "codex", Alias: "personal"}
	registry := &fakeRegistry{accounts: []store.Account{accountA}}
	resolver := New(registry, []Source{source})
	if err := resolver.SetAlias(context.Background(), "current", "antari"); err != nil {
		t.Fatal(err)
	}
	if len(registry.registered) != 1 || registry.registered[0].account.Ref != "private-b" || registry.registered[0].alias != "antari" || registry.registered[0].spec.Ref != source.Ref {
		t.Fatalf("registered=%+v", registry.registered)
	}
	if registry.accounts[0].Ref != "private-a" || registry.accounts[0].Alias != "personal" || registry.accounts[1].Ref != "private-b" || registry.accounts[1].Alias != "antari" {
		t.Fatalf("accounts=%+v", registry.accounts)
	}
	if registry.aliasSelector != "" || registry.syncs != 0 || len(registry.observed) != 0 {
		t.Fatalf("unexpected mutation path: registry=%+v", registry)
	}
}

func TestResolverAliasCurrentRegistersColdIdentityWithRealStore(t *testing.T) {
	dir := t.TempDir()
	current := writeResolverAuth(t, dir, "current-real", "private-b")
	unrelated := Source{Ref: SourceRef(filepath.Join(dir, "unrelated.json")), Path: filepath.Join(dir, "unrelated.json"), Priority: 9}
	st, err := store.Open(filepath.Join(dir, "store.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.SyncAuthSources(context.Background(), []store.SourceSpec{{Ref: unrelated.Ref, Enabled: true, Priority: unrelated.Priority}}); err != nil {
		t.Fatal(err)
	}
	if err := st.ObserveAuthSource(context.Background(), unrelated.Ref, resetwatch.Account{Ref: "private-a", Email: "a@example.com"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	accountAID := store.AccountID("codex", "private-a")
	if err := st.SetAccountAlias(context.Background(), accountAID, "personal"); err != nil {
		t.Fatal(err)
	}
	resolver := New(st, []Source{current})
	if err := resolver.SetAlias(context.Background(), "current", "antari"); err != nil {
		t.Fatal(err)
	}
	accounts, err := st.ListAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byRef := make(map[string]store.Account)
	for _, account := range accounts {
		byRef[account.Ref] = account
	}
	if byRef["private-a"].Alias != "personal" || byRef["private-b"].Alias != "antari" || !byRef["private-b"].LastSeenAt.IsZero() {
		t.Fatalf("accounts=%+v", byRef)
	}
	health, err := st.ListSourceHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bound := make(map[string]string)
	for _, source := range health {
		bound[source.SourceRef] = source.AccountRef
	}
	if bound[unrelated.Ref] != "private-a" || bound[current.Ref] != "private-b" {
		t.Fatalf("source bindings=%+v", bound)
	}
}
