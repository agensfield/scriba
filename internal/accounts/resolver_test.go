package accounts

import (
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
	accounts []store.Account
	syncs    int
	observed []observedSource
	aliases  int
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

func (f *fakeRegistry) SetAccountAlias(context.Context, string, string) error {
	f.aliases++
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
	if registry.syncs != 0 || len(registry.observed) != 0 {
		t.Fatalf("historical read wrote state: syncs=%d observed=%+v", registry.syncs, registry.observed)
	}
}

func TestResolverCurrentNeverFallsBackAcrossUnreconciledRotation(t *testing.T) {
	dir := t.TempDir()
	source := writeResolverAuth(t, dir, "current", "private-b")
	accountA := store.Account{ID: store.AccountID("codex", "private-a"), Ref: "private-a", ProviderID: "codex"}
	registry := &fakeRegistry{accounts: []store.Account{accountA}}
	resolver := New(registry, []Source{source})
	if _, err := resolver.Resolve(context.Background(), "current"); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("current rotation fell back to stale account: %v", err)
	}
	if registry.syncs != 0 || len(registry.observed) != 0 {
		t.Fatalf("current read reconciled state: syncs=%d observed=%+v", registry.syncs, registry.observed)
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
	registry := &fakeRegistry{}
	resolver := New(registry, nil)
	if err := resolver.SetAlias(context.Background(), "acct-00000000000000000000", "work"); err != nil {
		t.Fatal(err)
	}
	if registry.aliases != 1 || registry.syncs != 0 || len(registry.observed) != 0 {
		t.Fatalf("aliases=%d syncs=%d observed=%+v", registry.aliases, registry.syncs, registry.observed)
	}
}
