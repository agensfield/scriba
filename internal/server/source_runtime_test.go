package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/accounts"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/radar"
	"github.com/agensfield/scriba/internal/remote"
	remotecodex "github.com/agensfield/scriba/internal/remote/codex"
	"github.com/agensfield/scriba/internal/server/store"
)

type sourceFetchReply struct {
	result remote.ProbeResult
	err    error
	wait   bool
}

type orderedSourceFetcher struct {
	mu        sync.Mutex
	replies   map[string]sourceFetchReply
	order     []string
	expected  []string
	active    int
	maxActive int
}

func (f *orderedSourceFetcher) FetchLimits(ctx context.Context, source accounts.Source, expected string) (remote.ProbeResult, error) {
	f.mu.Lock()
	f.order = append(f.order, source.Ref)
	f.expected = append(f.expected, expected)
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	reply := f.replies[source.Ref]
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	if reply.wait {
		<-ctx.Done()
		return remote.ProbeResult{}, ctx.Err()
	}
	return reply.result, reply.err
}

type dynamicSourceFetcher struct {
	mu    sync.Mutex
	calls map[string]int
}

type mutatingSourceFetcher struct {
	mutate func() error
}

func (f mutatingSourceFetcher) FetchLimits(context.Context, accounts.Source, string) (remote.ProbeResult, error) {
	if err := f.mutate(); err != nil {
		return remote.ProbeResult{}, err
	}
	return remote.ProbeResult{}, &remotecodex.AccountBindingError{Changed: true}
}

func (f *dynamicSourceFetcher) FetchLimits(_ context.Context, source accounts.Source, expected string) (remote.ProbeResult, error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = make(map[string]int)
	}
	f.calls[source.Ref]++
	f.mu.Unlock()
	return sourceProbe(expected), nil
}

type countingRadar struct{ calls int }

func (r *countingRadar) Fetch(context.Context) (radar.Current, error) {
	r.calls++
	return radar.Current{SchemaVersion: "1", Status: "none"}, nil
}

func writeRuntimeSource(t *testing.T, dir, name, account string) accounts.Source {
	t.Helper()
	path := filepath.Join(dir, name+".json")
	payload := fmt.Sprintf(`{"tokens":{"access_token":"token-%s","account_id":%q},"last_refresh":"2026-09-10T00:00:00Z"}`, name, account)
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	return accounts.Source{Ref: accounts.SourceRef(path), Path: path}
}

func sourceProbe(account string) remote.ProbeResult {
	result := probeResult("2026-09-20T00:00:00Z", "2026-09-13T06:00:00Z")
	result.AuthState.AccountID = account
	result.AuthState.Email = account + "@example.com"
	return result
}

func TestRefreshSourcesKeepsFailuresIsolatedAndRunsGlobalWork(t *testing.T) {
	dir := t.TempDir()
	a := writeRuntimeSource(t, dir, "a", "")
	b := writeRuntimeSource(t, dir, "b", "acct-b")
	c := writeRuntimeSource(t, dir, "c", "acct-c")
	a.Priority, b.Priority, c.Priority = 0, 1, 2
	st := openStore(t)
	fetcher := &orderedSourceFetcher{replies: map[string]sourceFetchReply{
		b.Ref: {result: sourceProbe("acct-b")},
		c.Ref: {err: errors.New("network down")},
	}}
	radarFetcher := &countingRadar{}
	srv := New(st, fetcher, nil, Config{Sources: []accounts.Source{a, b, c}})
	srv.SetRadarFetcher(radarFetcher)

	got, err := srv.RefreshSourcesNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 3 || got.Sources[0].Failure == nil || got.Sources[1].Failure != nil || got.Sources[1].Observation.Account.Ref != "acct-b" || got.Sources[2].Failure == nil {
		t.Fatalf("sources=%+v", got.Sources)
	}
	if len(fetcher.order) != 2 || fetcher.order[0] != b.Ref || fetcher.order[1] != c.Ref || fetcher.maxActive != 1 {
		t.Fatalf("order=%v active=%d", fetcher.order, fetcher.maxActive)
	}
	if radarFetcher.calls != 1 {
		t.Fatalf("radar calls=%d", radarFetcher.calls)
	}
	if _, ok, err := st.GetSetting(context.Background(), SettingLastPruneAt); err != nil || !ok {
		t.Fatalf("prune setting ok=%v err=%v", ok, err)
	}
	health, err := st.ListSourceHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byRef := make(map[string]store.SourceHealth)
	for _, item := range health {
		byRef[item.SourceRef] = item
	}
	if byRef[a.Ref].FailureKind != store.SourceFailureAuth || byRef[b.Ref].LastSuccessAt == nil || byRef[c.Ref].FailureKind != store.SourceFailureNetwork {
		t.Fatalf("health=%+v", byRef)
	}
}

func TestSourceRotationDiscoversNewAccountAndKeepsHistory(t *testing.T) {
	dir := t.TempDir()
	source := writeRuntimeSource(t, dir, "rotating", "acct-a")
	st := openStore(t)
	srv := New(st, &dynamicSourceFetcher{}, nil, Config{Sources: []accounts.Source{source}})
	if _, err := srv.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeRuntimeSource(t, dir, "rotating", "acct-b")
	if _, err := srv.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	listed, err := srv.Accounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("accounts=%+v", listed)
	}
	byRef := make(map[string]store.Account)
	for _, account := range listed {
		byRef[account.Ref] = account
	}
	if byRef["acct-a"].CredentialsAvailable || !byRef["acct-b"].CredentialsAvailable || byRef["acct-a"].LastSeenAt.IsZero() || byRef["acct-b"].LastSeenAt.IsZero() {
		t.Fatalf("accounts by ref=%+v", byRef)
	}
	for _, ref := range []string{"acct-a", "acct-b"} {
		obs, ok, err := srv.LatestObservationForAccount(context.Background(), byRef[ref].ID)
		if err != nil || !ok || obs.Account.Ref != ref {
			t.Fatalf("ref=%s obs=%+v ok=%v err=%v", ref, obs, ok, err)
		}
	}
}

func TestFailedFetchReconcilesCredentialLossOrRotation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, string)
		wantB  bool
	}{
		{
			name: "missing",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "rotated",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				payload := `{"tokens":{"access_token":"token-b","account_id":"acct-b"},"last_refresh":"2026-09-10T00:00:00Z"}`
				if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantB: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			source := writeRuntimeSource(t, dir, "race", "acct-a")
			st := openStore(t)
			srv := New(st, &dynamicSourceFetcher{}, nil, Config{Sources: []accounts.Source{source}})
			if _, err := srv.RefreshNow(context.Background()); err != nil {
				t.Fatal(err)
			}
			srv.fetcher = mutatingSourceFetcher{mutate: func() error {
				tc.mutate(t, source.Path)
				return nil
			}}
			if _, err := srv.RefreshNow(context.Background()); !errors.Is(err, ErrAllSourcesFailed) {
				t.Fatalf("refresh err=%v", err)
			}
			listed, err := st.ListAccounts(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			byRef := make(map[string]store.Account)
			for _, account := range listed {
				byRef[account.Ref] = account
			}
			if byRef["acct-a"].CredentialsAvailable {
				t.Fatalf("old account stayed credential-available: %+v", byRef)
			}
			if tc.wantB {
				if !byRef["acct-b"].CredentialsAvailable || !byRef["acct-b"].LastSeenAt.IsZero() {
					t.Fatalf("rotated account state=%+v", byRef["acct-b"])
				}
			} else if len(byRef) != 1 {
				t.Fatalf("missing credentials fabricated account: %+v", byRef)
			}
		})
	}
}

func TestHealthUsesSourcesWithoutMakingHistoricalAccountsFailures(t *testing.T) {
	dir := t.TempDir()
	source := writeRuntimeSource(t, dir, "current", "acct-a")
	st := openStore(t)
	srv := New(st, &dynamicSourceFetcher{}, nil, Config{Sources: []accounts.Source{source}})
	if _, err := srv.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeRuntimeSource(t, dir, "current", "acct-b")
	if _, err := srv.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	health, err := srv.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != HealthOK || len(health.Sources) != 1 || health.Sources[0].Status != HealthOK || len(health.Accounts) != 2 {
		t.Fatalf("health=%+v", health)
	}
	byRef := make(map[string]AccountHealth)
	for _, account := range health.Accounts {
		byRef[account.Account.Ref] = account
	}
	if byRef["acct-a"].Account.CredentialsAvailable || !byRef["acct-b"].Account.CredentialsAvailable {
		t.Fatalf("account health=%+v", byRef)
	}
}

func TestHealthMarksInterruptedSourceAttempt(t *testing.T) {
	dir := t.TempDir()
	source := writeRuntimeSource(t, dir, "interrupted", "acct-a")
	st := openStore(t)
	resolver := accounts.New(st, []accounts.Source{source})
	if err := resolver.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	attempt := time.Now().Add(-DefaultRefreshTimeout - time.Second).UTC()
	if err := st.RecordSourcePollAttempt(context.Background(), source.Ref, attempt); err != nil {
		t.Fatal(err)
	}
	health, err := New(st, nil, nil, Config{Sources: []accounts.Source{source}}).Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != HealthDegraded || len(health.Sources) != 1 || health.Sources[0].FailureKind != "interrupted" {
		t.Fatalf("health=%+v", health)
	}
}

func TestHistoricalReadSurvivesMissingCredentialsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	source := writeRuntimeSource(t, dir, "history", "acct-a")
	st := openStore(t)
	srv := New(st, &dynamicSourceFetcher{}, nil, Config{Sources: []accounts.Source{source}})
	if _, err := srv.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	accountsBefore, err := st.ListAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source.Path); err != nil {
		t.Fatal(err)
	}
	listed, err := srv.Accounts(context.Background())
	if err != nil || len(listed) != 1 || listed[0].CredentialsAvailable {
		t.Fatalf("accounts=%+v err=%v", listed, err)
	}
	obs, ok, err := srv.LatestObservationForAccount(context.Background(), listed[0].ID)
	if err != nil || !ok || obs.Account.Ref != "acct-a" {
		t.Fatalf("obs=%+v ok=%v err=%v", obs, ok, err)
	}
	accountsAfter, err := st.ListAccounts(context.Background())
	if err != nil || accountsAfter[0].CredentialsAvailable != accountsBefore[0].CredentialsAvailable {
		t.Fatalf("read path mutated durable source state: before=%+v after=%+v err=%v", accountsBefore, accountsAfter, err)
	}
}

func TestSourceSnapshotsAndPublicHealthStayPrivate(t *testing.T) {
	dir := t.TempDir()
	source := writeRuntimeSource(t, dir, "private", "private-account-ref")
	probe := sourceProbe("private-account-ref")
	probe.AuthState.Email = "safe@example.com"
	probe.AuthState.Source = source.Path
	probe.AuthState.Error = "PRIVATE_DIAGNOSTIC"
	probe.AuthState.AccessToken = "PRIVATE_BEARER"
	probe.Provenance = []model.SourceProvenance{{Kind: "provider-api", ProviderID: "codex", Error: source.Path}}
	probe.Lines[0].Provenance = []model.SourceProvenance{{Kind: "provider-api", ProviderID: "codex", Error: "PRIVATE_BEARER"}}
	st := openStore(t)
	notifier := &fakeNotifier{}
	fetcher := &orderedSourceFetcher{replies: map[string]sourceFetchReply{source.Ref: {result: probe}}}
	srv := New(st, fetcher, notifier, Config{Sources: []accounts.Source{source}})
	result, err := srv.RefreshNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range [][]byte{result.Observation.SnapshotJSON, notifier.baselines[0].SnapshotJSON} {
		for _, private := range []string{source.Path, "PRIVATE_DIAGNOSTIC", "PRIVATE_BEARER", "private-account-ref"} {
			if strings.Contains(string(snapshot), private) {
				t.Fatalf("snapshot leaked %q: %s", private, snapshot)
			}
		}
	}
	health, err := srv.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(health)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{source.Path, "PRIVATE_DIAGNOSTIC", "PRIVATE_BEARER", "private-account-ref"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("health leaked %q: %s", private, raw)
		}
	}
}

func TestDuplicateSourcesForAccountDoNotDuplicateNotifications(t *testing.T) {
	dir := t.TempDir()
	a := writeRuntimeSource(t, dir, "one", "acct-a")
	b := writeRuntimeSource(t, dir, "two", "acct-a")
	b.Priority = 1
	st := openStore(t)
	notifier := &fakeNotifier{}
	srv := New(st, &dynamicSourceFetcher{}, notifier, Config{Sources: []accounts.Source{a, b}})
	if _, err := srv.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(notifier.baselines) != 1 {
		t.Fatalf("baseline notifications=%d", len(notifier.baselines))
	}
	if _, err := srv.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(notifier.resets) > 1 || len(notifier.resetGrants) > 1 {
		t.Fatalf("duplicate semantic notifications: resets=%d grants=%d", len(notifier.resets), len(notifier.resetGrants))
	}
}

func TestSourceTimeoutContinuesAndParentCancellationStopsCycle(t *testing.T) {
	dir := t.TempDir()
	a := writeRuntimeSource(t, dir, "a", "acct-a")
	b := writeRuntimeSource(t, dir, "b", "acct-b")
	b.Priority = 1

	t.Run("source timeout", func(t *testing.T) {
		st := openStore(t)
		fetcher := &orderedSourceFetcher{replies: map[string]sourceFetchReply{a.Ref: {wait: true}, b.Ref: {result: sourceProbe("acct-b")}}}
		srv := New(st, fetcher, nil, Config{Sources: []accounts.Source{a, b}})
		srv.sourceTimeout = 500 * time.Millisecond
		if _, err := srv.RefreshNow(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(fetcher.order) != 2 || fetcher.order[1] != b.Ref {
			t.Fatalf("order=%v", fetcher.order)
		}
	})

	t.Run("parent cancellation", func(t *testing.T) {
		st := openStore(t)
		fetcher := &orderedSourceFetcher{replies: map[string]sourceFetchReply{a.Ref: {wait: true}, b.Ref: {result: sourceProbe("acct-b")}}}
		srv := New(st, fetcher, nil, Config{Sources: []accounts.Source{a, b}})
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(10*time.Millisecond, cancel)
		if _, err := srv.RefreshNow(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
		if len(fetcher.order) > 1 {
			t.Fatalf("order=%v", fetcher.order)
		}
	})
}
