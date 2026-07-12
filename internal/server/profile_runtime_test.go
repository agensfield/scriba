package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/radar"
	"github.com/agensfield/scriba/internal/remote"
	"github.com/agensfield/scriba/internal/server/store"
)

type profileFetchReply struct {
	result remote.ProbeResult
	err    error
	wait   bool
}

type orderedProfileFetcher struct {
	mu        sync.Mutex
	replies   map[string]profileFetchReply
	order     []string
	active    int
	maxActive int
}

func (f *orderedProfileFetcher) FetchLimits(context.Context) (remote.ProbeResult, error) {
	return remote.ProbeResult{}, errors.New("legacy fetch used")
}

func (f *orderedProfileFetcher) FetchProfileLimits(ctx context.Context, profile Profile) (remote.ProbeResult, error) {
	f.mu.Lock()
	f.order = append(f.order, profile.Ref)
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	reply := f.replies[profile.Ref]
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

type countingRadar struct {
	calls int
}

type blockingRadar struct {
	started chan struct{}
}

func (r *blockingRadar) Fetch(ctx context.Context) (radar.Current, error) {
	close(r.started)
	<-ctx.Done()
	return radar.Current{}, ctx.Err()
}

type cancelingRadarNotifier struct {
	*fakeNotifier
	cancel context.CancelFunc
}

type blockingPruneStore struct {
	*store.Store
	started chan struct{}
}

func (s *blockingPruneStore) PruneObservations(ctx context.Context, _ time.Time, _ bool) (store.PruneResult, error) {
	close(s.started)
	<-ctx.Done()
	return store.PruneResult{}, ctx.Err()
}

func (n *cancelingRadarNotifier) NotifyRadarProbability(ctx context.Context, alert radar.ProbabilityAlert) error {
	n.cancel()
	return n.fakeNotifier.NotifyRadarProbability(ctx, alert)
}

func (r *countingRadar) Fetch(context.Context) (radar.Current, error) {
	r.calls++
	return radar.Current{SchemaVersion: "1", Status: "none"}, nil
}

func profileProbe(account string) remote.ProbeResult {
	result := probeResult("2026-07-20T00:00:00Z", "2026-07-13T06:00:00Z")
	result.AuthState.AccountID = account
	result.AuthState.Email = account + "@example.com"
	return result
}

func syncRuntimeProfiles(t *testing.T, st *store.Store, profiles []Profile) {
	t.Helper()
	specs := make([]store.ProfileSpec, 0, len(profiles))
	for _, profile := range profiles {
		specs = append(specs, store.ProfileSpec{ProfileRef: profile.Ref, ProviderID: "codex", Label: profile.Label, Enabled: true, IsDefault: profile.Default})
	}
	if err := st.SyncProfiles(context.Background(), specs); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshProfilesSequentialPartialIsolationAndGlobalWork(t *testing.T) {
	st := openStore(t)
	profiles := []Profile{{Ref: "a", Label: "A", Default: true}, {Ref: "b", Label: "B"}, {Ref: "c", Label: "C"}}
	syncRuntimeProfiles(t, st, profiles)
	fetcher := &orderedProfileFetcher{replies: map[string]profileFetchReply{
		"a": {result: remote.ProbeResult{AuthState: remote.AuthState{OK: false}}},
		"b": {result: profileProbe("acct-b")},
		"c": {err: errors.New("network down")},
	}}
	notifier := &fakeNotifier{}
	radarFetcher := &countingRadar{}
	srv := New(st, fetcher, notifier, Config{Profiles: profiles})
	srv.SetRadarFetcher(radarFetcher)

	got, err := srv.RefreshProfilesNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Profiles) != 3 || got.Profiles[0].Failure == nil || got.Profiles[1].Failure != nil || got.Profiles[1].Observation.Account.Ref != "acct-b" || got.Profiles[2].Failure == nil {
		t.Fatalf("profiles=%+v", got.Profiles)
	}
	if want := []string{"a", "b", "c"}; len(fetcher.order) != len(want) || fetcher.order[0] != "a" || fetcher.order[1] != "b" || fetcher.order[2] != "c" || fetcher.maxActive != 1 {
		t.Fatalf("order=%v active=%d", fetcher.order, fetcher.maxActive)
	}
	if radarFetcher.calls != 1 {
		t.Fatalf("radar calls=%d", radarFetcher.calls)
	}
	if _, ok, err := st.GetSetting(context.Background(), SettingLastPruneAt); err != nil || !ok {
		t.Fatalf("prune setting ok=%v err=%v", ok, err)
	}
	health, err := st.ListProfileHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byRef := map[string]store.ProfileHealth{}
	for _, item := range health {
		byRef[item.ProfileRef] = item
	}
	if byRef["a"].FailureKind != store.ProfileFailureAuth || byRef["b"].LastSuccessAt == nil || byRef["c"].FailureKind != store.ProfileFailureNetwork {
		t.Fatalf("health=%+v", byRef)
	}
}

func TestRefreshProfilesAllFailStillRunsGlobalWork(t *testing.T) {
	st := openStore(t)
	profiles := []Profile{{Ref: "a", Label: "A", Default: true}, {Ref: "b", Label: "B"}}
	syncRuntimeProfiles(t, st, profiles)
	fetcher := &orderedProfileFetcher{replies: map[string]profileFetchReply{
		"a": {err: errors.New("down")},
		"b": {result: remote.ProbeResult{AuthState: remote.AuthState{OK: false}}},
	}}
	radarFetcher := &countingRadar{}
	srv := New(st, fetcher, nil, Config{Profiles: profiles})
	srv.SetRadarFetcher(radarFetcher)
	if _, err := srv.RefreshNow(context.Background()); !errors.Is(err, ErrAllProfilesFailed) {
		t.Fatalf("err=%v", err)
	}
	if radarFetcher.calls != 1 {
		t.Fatalf("radar calls=%d", radarFetcher.calls)
	}
	if _, ok, _ := st.GetSetting(context.Background(), SettingLastPruneAt); !ok {
		t.Fatal("prune did not run")
	}
}

func TestRefreshProfilesTimeoutContinuesAndParentCancelStopsCycle(t *testing.T) {
	profiles := []Profile{{Ref: "a", Label: "A", Default: true}, {Ref: "b", Label: "B"}}

	t.Run("profile timeout", func(t *testing.T) {
		st := openStore(t)
		syncRuntimeProfiles(t, st, profiles)
		fetcher := &orderedProfileFetcher{replies: map[string]profileFetchReply{"a": {wait: true}, "b": {result: profileProbe("acct-b")}}}
		srv := New(st, fetcher, nil, Config{Profiles: profiles})
		srv.profileTimeout = 250 * time.Millisecond
		if _, err := srv.RefreshNow(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(fetcher.order) != 2 || fetcher.order[1] != "b" {
			t.Fatalf("order=%v", fetcher.order)
		}
	})

	t.Run("parent cancellation", func(t *testing.T) {
		st := openStore(t)
		syncRuntimeProfiles(t, st, profiles)
		fetcher := &orderedProfileFetcher{replies: map[string]profileFetchReply{"a": {wait: true}, "b": {result: profileProbe("acct-b")}}}
		radarFetcher := &countingRadar{}
		srv := New(st, fetcher, nil, Config{Profiles: profiles})
		srv.SetRadarFetcher(radarFetcher)
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(10*time.Millisecond, cancel)
		if _, err := srv.RefreshNow(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
		if len(fetcher.order) != 1 || radarFetcher.calls != 0 {
			t.Fatalf("order=%v radar=%d", fetcher.order, radarFetcher.calls)
		}
		if _, ok, _ := st.GetSetting(context.Background(), SettingLastPruneAt); ok {
			t.Fatal("prune ran after cancellation")
		}
	})
}

func TestRefreshProfilesCancellationDuringGlobalWorkSkipsPrune(t *testing.T) {
	for _, allFail := range []bool{false, true} {
		t.Run(map[bool]string{false: "mixed", true: "all-fail"}[allFail], func(t *testing.T) {
			st := openStore(t)
			profiles := []Profile{{Ref: "a", Label: "A", Default: true}}
			syncRuntimeProfiles(t, st, profiles)
			reply := profileFetchReply{result: profileProbe("acct-a")}
			if allFail {
				reply = profileFetchReply{err: errors.New("down")}
			}
			fetcher := &orderedProfileFetcher{replies: map[string]profileFetchReply{"a": reply}}
			radarFetcher := &blockingRadar{started: make(chan struct{})}
			srv := New(st, fetcher, nil, Config{Profiles: profiles})
			srv.SetRadarFetcher(radarFetcher)
			ctx, cancel := context.WithCancel(context.Background())
			errCh := make(chan error, 1)
			go func() { _, err := srv.RefreshNow(ctx); errCh <- err }()
			<-radarFetcher.started
			cancel()
			if err := <-errCh; !errors.Is(err, context.Canceled) {
				t.Fatalf("err=%v", err)
			}
			if _, ok, _ := st.GetSetting(context.Background(), SettingLastPruneAt); ok {
				t.Fatal("prune ran after Radar cancellation")
			}
		})
	}
}

func TestRefreshProfilesCancellationDuringRadarNotificationSkipsPrune(t *testing.T) {
	st := openStore(t)
	profiles := []Profile{{Ref: "a", Label: "A", Default: true}}
	syncRuntimeProfiles(t, st, profiles)
	fetcher := &orderedProfileFetcher{replies: map[string]profileFetchReply{"a": {result: profileProbe("acct-a")}}}
	ctx, cancel := context.WithCancel(context.Background())
	notifier := &cancelingRadarNotifier{fakeNotifier: &fakeNotifier{}, cancel: cancel}
	srv := New(st, fetcher, notifier, Config{Profiles: profiles})
	srv.SetRadarFetcher(&fakeRadarFetcher{currents: []radar.Current{radarCurrent("2026-07-13T00:00:00Z", 0.9)}})
	if _, err := srv.RefreshNow(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if _, ok, _ := st.GetSetting(context.Background(), SettingLastPruneAt); ok {
		t.Fatal("prune ran after notification cancellation")
	}
}

func TestProfileSnapshotsStripAuthPathsAndProviderErrors(t *testing.T) {
	st := openStore(t)
	profiles := []Profile{{Ref: "private", Label: "Private", Default: true, AuthPaths: []string{"/secret/profile/auth.json"}}}
	syncRuntimeProfiles(t, st, profiles)
	probe := profileProbe("acct-private")
	probe.AuthState.Source = "/secret/profile/auth.json"
	probe.AuthState.Error = "failed reading /secret/profile/auth.json"
	probe.AuthState.AccessToken = "bearer-secret"
	probe.Provenance = []model.SourceProvenance{{Kind: "provider-api", ProviderID: "codex", Error: "/secret/profile/auth.json"}}
	probe.Lines[0].Provenance = []model.SourceProvenance{{Kind: "provider-api", ProviderID: "codex", Error: "bearer-secret"}}
	fetcher := &orderedProfileFetcher{replies: map[string]profileFetchReply{"private": {result: probe}}}
	notifier := &fakeNotifier{}
	srv := New(st, fetcher, notifier, Config{Profiles: profiles, NotificationTarget: "telegram:42"})
	result, err := srv.RefreshNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{result.Observation.SnapshotJSON, notifier.baselines[0].SnapshotJSON} {
		if strings.Contains(string(raw), "/secret/profile/auth.json") || strings.Contains(string(raw), "bearer-secret") {
			t.Fatalf("private data leaked: %s", raw)
		}
	}
	durable, ok, err := st.LoadLatestObservation(context.Background())
	if err != nil || !ok {
		t.Fatalf("durable observation ok=%v err=%v", ok, err)
	}
	outbox, err := st.ListOutbox(context.Background(), store.OutboxFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	combined := string(durable.SnapshotJSON)
	for _, message := range outbox {
		combined += message.PayloadJSON
	}
	if strings.Contains(combined, "/secret/profile/auth.json") || strings.Contains(combined, "bearer-secret") {
		t.Fatalf("durable private data leaked: %s", combined)
	}
}

func TestExplicitProfileCannotUseAmbientAuthDiscovery(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"tokens":{"access_token":"secret"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", dir)
	_, err := (CodexFetcher{}).FetchProfileLimits(context.Background(), Profile{Ref: "explicit", Label: "Explicit"})
	if !errors.Is(err, ErrProfileAuthPaths) {
		t.Fatalf("err=%v", err)
	}
	legacy := fakeFetcherFunc(func(context.Context) (remote.ProbeResult, error) {
		return profileProbe("ambient-account"), nil
	})
	st := openStore(t)
	syncRuntimeProfiles(t, st, []Profile{{Ref: "explicit", Label: "Explicit", Default: true}})
	if _, err = New(st, legacy, nil, Config{Profiles: []Profile{{Ref: "explicit", Label: "Explicit", Default: true}}}).RefreshNow(context.Background()); !errors.Is(err, ErrAllProfilesFailed) {
		t.Fatalf("legacy explicit fallback err=%v", err)
	}
	health, listErr := st.ListProfileHealth(context.Background())
	if listErr != nil || health[0].LastSuccessAt != nil || health[0].LastErrorCode != store.ProfileErrorRequestFailed {
		t.Fatalf("health=%+v err=%v", health, listErr)
	}
}

func TestRefreshProfilesCancellationDuringPruneIsReturned(t *testing.T) {
	base := openStore(t)
	profiles := []Profile{{Ref: "a", Label: "A", Default: true}}
	syncRuntimeProfiles(t, base, profiles)
	st := &blockingPruneStore{Store: base, started: make(chan struct{})}
	fetcher := &orderedProfileFetcher{replies: map[string]profileFetchReply{"a": {result: profileProbe("acct-a")}}}
	srv := New(st, fetcher, nil, Config{Profiles: profiles})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { _, err := srv.RefreshNow(ctx); errCh <- err }()
	<-st.started
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestProfileHealthNotificationsRetryWithoutPoisoningPolls(t *testing.T) {
	st := openStore(t)
	profiles := []Profile{{Ref: "a", Label: "A", Default: true}}
	syncRuntimeProfiles(t, st, profiles)
	fetcher := &orderedProfileFetcher{replies: map[string]profileFetchReply{"a": {err: errors.New("down")}}}
	notifier := &fakeNotifier{healthFailures: 1}
	srv := New(st, fetcher, notifier, Config{Profiles: profiles})
	for range FailureAlertThreshold {
		if _, err := srv.RefreshNow(context.Background()); !errors.Is(err, ErrAllProfilesFailed) {
			t.Fatalf("failure cycle err=%v", err)
		}
	}
	health, err := st.ListProfileHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health[0].AlertState != "ok" || len(notifier.health) != 1 {
		t.Fatalf("failed notice state=%q notices=%d", health[0].AlertState, len(notifier.health))
	}
	if _, err := srv.RefreshNow(context.Background()); !errors.Is(err, ErrAllProfilesFailed) {
		t.Fatalf("retry failure err=%v", err)
	}
	health, _ = st.ListProfileHealth(context.Background())
	if health[0].AlertState != "failing" || len(notifier.health) != 2 {
		t.Fatalf("retry state=%q notices=%d", health[0].AlertState, len(notifier.health))
	}

	notifier.healthFailures = 1
	fetcher.replies["a"] = profileFetchReply{result: profileProbe("acct-a")}
	if _, err := srv.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	health, _ = st.ListProfileHealth(context.Background())
	if health[0].AlertState != "failing" {
		t.Fatalf("failed recovery changed state=%q", health[0].AlertState)
	}
	if _, err := srv.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	health, _ = st.ListProfileHealth(context.Background())
	if health[0].AlertState != "ok" || len(notifier.health) != 4 || !notifier.health[2].Recovery || !notifier.health[3].Recovery {
		t.Fatalf("recovery state=%q notices=%+v", health[0].AlertState, notifier.health)
	}
}

func TestRefreshNowReturnsLaterSuccessAndHeartbeatUsesFirstSuccess(t *testing.T) {
	st := openStore(t)
	profiles := []Profile{{Ref: "a", Label: "A", Default: true}, {Ref: "b", Label: "B"}, {Ref: "c", Label: "C"}}
	syncRuntimeProfiles(t, st, profiles)
	bootstrap := &orderedProfileFetcher{replies: map[string]profileFetchReply{
		"a": {result: profileProbe("acct-a")}, "b": {result: profileProbe("acct-b")}, "c": {result: profileProbe("acct-c")},
	}}
	if _, err := New(st, bootstrap, nil, Config{Profiles: profiles}).RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}

	fetcher := &orderedProfileFetcher{replies: map[string]profileFetchReply{
		"a": {err: errors.New("down")}, "b": {result: profileProbe("acct-b")}, "c": {result: profileProbe("acct-c")},
	}}
	notifier := &fakeNotifier{}
	srv := New(st, fetcher, notifier, Config{Profiles: profiles, StartupHeartbeat: true})
	result, err := srv.RefreshNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile.Ref != "b" || len(notifier.baselines) != 1 || notifier.baselines[0].Profile.Ref != "b" {
		t.Fatalf("result=%+v baselines=%+v", result.Profile, notifier.baselines)
	}
}
