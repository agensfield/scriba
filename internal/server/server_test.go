package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
)

func TestRefreshSeedsBaselineThenNotifiesResetOnce(t *testing.T) {
	ctx := context.Background()
	fetcher := &fakeFetcher{results: []remote.ProbeResult{
		probeResult("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z"),
		probeResult("2026-06-09T12:00:00Z", "2026-06-02T17:00:00Z"),
		probeResult("2026-06-09T12:00:00Z", "2026-06-02T17:00:00Z"),
	}}
	notifier := &fakeNotifier{}
	srv := New(openStore(t), fetcher, notifier, Config{AccountLabel: "personal", JokeTone: "spicy"})

	first, err := srv.RefreshNow(ctx)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if !first.Baseline || first.Inserted != 0 {
		t.Fatalf("unexpected first result: %#v", first)
	}
	if len(notifier.baselines) != 1 || len(notifier.resets) != 0 {
		t.Fatalf("unexpected first notices: baselines=%d resets=%d", len(notifier.baselines), len(notifier.resets))
	}

	second, err := srv.RefreshNow(ctx)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if second.Baseline || second.Inserted != 1 {
		t.Fatalf("unexpected second result: %#v", second)
	}
	if len(notifier.resets) != 1 {
		t.Fatalf("expected one reset notice, got %d", len(notifier.resets))
	}
	if notifier.resets[0].Account.Email != "arda@example.com" || notifier.resets[0].Account.Plan != "plus" {
		t.Fatalf("event lost account context: %#v", notifier.resets[0].Account)
	}

	third, err := srv.RefreshNow(ctx)
	if err != nil {
		t.Fatalf("third refresh: %v", err)
	}
	if third.Inserted != 0 || len(notifier.resets) != 1 {
		t.Fatalf("expected deduped reset, result=%#v notices=%d", third, len(notifier.resets))
	}
}

func TestPollIntervalSetting(t *testing.T) {
	ctx := context.Background()
	srv := New(openStore(t), nil, nil, Config{})
	interval, err := srv.PollInterval(ctx)
	if err != nil {
		t.Fatalf("default interval: %v", err)
	}
	if interval != DefaultPollInterval {
		t.Fatalf("unexpected default: %s", interval)
	}
	if err := srv.SetPollInterval(ctx, 90*time.Second); err != nil {
		t.Fatalf("set interval: %v", err)
	}
	interval, err = srv.PollInterval(ctx)
	if err != nil {
		t.Fatalf("custom interval: %v", err)
	}
	if interval != 90*time.Second {
		t.Fatalf("unexpected custom interval: %s", interval)
	}
}

func TestRefreshNowIsSingleFlight(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	started := make(chan struct{})
	fetcher := fakeFetcherFunc(func(context.Context) (remote.ProbeResult, error) {
		close(started)
		<-release
		return probeResult("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z"), nil
	})
	srv := New(openStore(t), fetcher, nil, Config{})
	errs := make(chan error, 1)
	go func() {
		_, err := srv.RefreshNow(ctx)
		errs <- err
	}()
	<-started
	if _, err := srv.RefreshNow(ctx); err != ErrRefreshInProgress {
		t.Fatalf("expected in-progress error, got %v", err)
	}
	close(release)
	if err := <-errs; err != nil {
		t.Fatalf("first refresh failed: %v", err)
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "scriba-server.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

type fakeFetcher struct {
	results []remote.ProbeResult
	index   int
}

func (f *fakeFetcher) FetchLimits(context.Context) (remote.ProbeResult, error) {
	if f.index >= len(f.results) {
		return f.results[len(f.results)-1], nil
	}
	result := f.results[f.index]
	f.index++
	return result, nil
}

type fakeFetcherFunc func(context.Context) (remote.ProbeResult, error)

func (f fakeFetcherFunc) FetchLimits(ctx context.Context) (remote.ProbeResult, error) {
	return f(ctx)
}

type fakeNotifier struct {
	baselines []BaselineNotice
	resets    []resetwatch.Event
}

func (n *fakeNotifier) NotifyBaseline(_ context.Context, notice BaselineNotice) error {
	n.baselines = append(n.baselines, notice)
	return nil
}

func (n *fakeNotifier) NotifyReset(_ context.Context, event resetwatch.Event) error {
	n.resets = append(n.resets, event)
	return nil
}

func probeResult(weeklyReset, fiveReset string) remote.ProbeResult {
	weeklyUsed := 51.0
	fiveUsed := 96.0
	limit := 100.0
	return remote.ProbeResult{
		ProviderID: "codex",
		AuthState: remote.AuthState{
			OK:        true,
			Source:    "/tmp/auth.json",
			Email:     "arda@example.com",
			AccountID: "acct_123",
		},
		Lines: []model.MetricLine{
			{Type: "badge", Label: "Plan", Text: "plus"},
			{Type: "progress", Label: resetwatch.LabelFiveHour, Used: &fiveUsed, Limit: &limit, ResetsAt: fiveReset},
			{Type: "progress", Label: resetwatch.LabelWeeklyLimit, Used: &weeklyUsed, Limit: &limit, ResetsAt: weeklyReset},
		},
	}
}
