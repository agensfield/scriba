package server

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/radar"
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
	select {
	case <-srv.intervalCh:
	default:
		t.Fatal("expected poll interval change signal")
	}
	if got := FormatDuration(90 * time.Second); got != "1m30s" {
		t.Fatalf("unexpected formatted duration: %s", got)
	}
}

func TestRefreshEmitsLimitWarningsOncePerCheckpoint(t *testing.T) {
	ctx := context.Background()
	baseline := probeResult("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z")
	baselineUsed := 70.0
	baseline.Lines[1].Used = &baselineUsed
	fetcher := &fakeFetcher{results: []remote.ProbeResult{
		baseline,
		probeResult("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z"),
		probeResult("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z"),
	}}
	notifier := &fakeNotifier{}
	srv := New(openStore(t), fetcher, notifier, Config{AccountLabel: "personal"})
	first, err := srv.RefreshNow(ctx)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if len(first.Warnings) != 0 {
		t.Fatalf("unexpected first warnings: %#v", first.Warnings)
	}
	second, err := srv.RefreshNow(ctx)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if len(second.Warnings) != 1 || second.Warnings[0].Label != resetwatch.LabelFiveHour || second.Warnings[0].ThresholdRemaining != 5 {
		t.Fatalf("unexpected second warnings: %#v", second.Warnings)
	}
	if len(notifier.warnings) != 1 {
		t.Fatalf("expected warning notification, got %d", len(notifier.warnings))
	}
	third, err := srv.RefreshNow(ctx)
	if err != nil {
		t.Fatalf("third refresh: %v", err)
	}
	if len(third.Warnings) != 0 || len(notifier.warnings) != 1 {
		t.Fatalf("expected deduped warning, result=%#v notifications=%d", third.Warnings, len(notifier.warnings))
	}
}

func TestRefreshEmitsGrantExpiryWarningsOncePerCheckpoint(t *testing.T) {
	ctx := context.Background()
	expiresAt := time.Now().UTC().Add(23 * time.Hour).Format(time.RFC3339Nano)
	baselineExpiresAt := time.Now().UTC().Add(10 * 24 * time.Hour).Format(time.RFC3339Nano)
	fetcher := &fakeFetcher{results: []remote.ProbeResult{
		probeResultWithGrant("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z", baselineExpiresAt),
		probeResultWithGrant("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z", expiresAt),
		probeResultWithGrant("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z", expiresAt),
	}}
	notifier := &fakeNotifier{}
	srv := New(openStore(t), fetcher, notifier, Config{AccountLabel: "personal"})
	first, err := srv.RefreshNow(ctx)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if len(first.GrantWarnings) != 0 {
		t.Fatalf("bootstrap should be silent, got %#v", first.GrantWarnings)
	}
	second, err := srv.RefreshNow(ctx)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if len(second.GrantWarnings) != 3 || len(notifier.grantWarnings) != 3 {
		t.Fatalf("expected three grant warnings, result=%#v notifications=%d", second.GrantWarnings, len(notifier.grantWarnings))
	}
	third, err := srv.RefreshNow(ctx)
	if err != nil {
		t.Fatalf("third refresh: %v", err)
	}
	if len(third.GrantWarnings) != 0 || len(notifier.grantWarnings) != 3 {
		t.Fatalf("expected deduped grant warnings, result=%#v notifications=%d", third.GrantWarnings, len(notifier.grantWarnings))
	}
}

func TestPollOnceUsesAtomicApplyAndMapsTypedResults(t *testing.T) {
	ctx := context.Background()
	base := openStore(t)
	want := store.CodexPollResult{
		LegacyDecision:           resetwatch.Decision{States: []resetwatch.WindowState{{AccountRef: "acct_123", Label: resetwatch.LabelFiveHour}}, Events: []resetwatch.Event{{ID: "reset-1"}}},
		ResetEvents:              []resetwatch.Event{{ID: "reset-1"}},
		WarningEvents:            []resetwatch.WarningEvent{{ID: "warning-1"}},
		GrantExpiryWarningEvents: []resetwatch.GrantExpiryWarning{{ID: "grant-warning-1"}},
		ResetGrantEvents:         []resetwatch.ResetGrantEvent{{ID: "grant-1"}},
	}
	spy := &atomicPollStore{Store: base, result: want}
	notifier := &fakeNotifier{}
	srv := New(spy, &fakeFetcher{results: []remote.ProbeResult{probeResult("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z")}}, notifier, Config{
		NotificationTarget: "telegram:42",
		JokeTone:           "spicy",
	})

	got, err := srv.pollOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if spy.applyCalls != 1 || spy.oldCalls != 0 {
		t.Fatalf("apply calls=%d old calls=%d", spy.applyCalls, spy.oldCalls)
	}
	if spy.input.NotificationTarget != "telegram:42" || spy.input.CommittedAt.IsZero() || spy.input.CommittedAt.Location() != time.UTC {
		t.Fatalf("unexpected atomic input: %#v", spy.input)
	}
	chooser, ok := spy.input.ResetOptions.JokeChooser.(resetwatch.CatalogJokeChooser)
	if !ok || chooser.Tone != "spicy" {
		t.Fatalf("unexpected joke chooser: %#v", spy.input.ResetOptions.JokeChooser)
	}
	if got.Inserted != 1 || !reflect.DeepEqual(got.Decision, want.LegacyDecision) || !reflect.DeepEqual(got.Warnings, want.WarningEvents) || !reflect.DeepEqual(got.GrantWarnings, want.GrantExpiryWarningEvents) || !reflect.DeepEqual(got.ResetGrants, want.ResetGrantEvents) {
		t.Fatalf("typed results not mapped exactly: %#v", got)
	}
	if len(notifier.resets) != 1 || len(notifier.warnings) != 1 || len(notifier.grantWarnings) != 1 || len(notifier.resetGrants) != 1 {
		t.Fatalf("typed notifier wake calls missing: %#v", notifier)
	}
}

func TestPollOnceAtomicApplyFailureDoesNotNotify(t *testing.T) {
	base := openStore(t)
	spy := &atomicPollStore{Store: base, err: errors.New("atomic write failed")}
	notifier := &fakeNotifier{}
	srv := New(spy, &fakeFetcher{results: []remote.ProbeResult{probeResult("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z")}}, notifier, Config{})

	if _, err := srv.pollOnce(context.Background()); err == nil || err.Error() != "atomic write failed" {
		t.Fatalf("unexpected error: %v", err)
	}
	if spy.applyCalls != 1 || spy.oldCalls != 0 {
		t.Fatalf("apply calls=%d old calls=%d", spy.applyCalls, spy.oldCalls)
	}
	if len(notifier.baselines)+len(notifier.resets)+len(notifier.warnings)+len(notifier.grantWarnings)+len(notifier.resetGrants)+len(notifier.radarAlerts) != 0 {
		t.Fatalf("transaction failure notified: %#v", notifier)
	}
}

func TestRefreshEmitsResetGrantLoadedOnlyForNewCredits(t *testing.T) {
	ctx := context.Background()
	fetcher := &fakeFetcher{results: []remote.ProbeResult{
		probeResultWithGrantID("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z", "credit_1", "2026-06-12T01:20:48Z", "2026-07-12T01:20:48Z"),
		probeResultWithCredits("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z", []remote.ResetCredit{
			{ID: "credit_1", Status: "available", Title: "Rate limit reset", ResetType: "codex_rate_limits", GrantedAt: "2026-06-12T01:20:48Z", ExpiresAt: "2026-07-12T01:20:48Z"},
			{ID: "credit_2", Status: "available", Title: "Rate limit reset", ResetType: "codex_rate_limits", GrantedAt: "2026-06-18T00:29:25Z", ExpiresAt: "2026-07-18T00:29:25Z"},
		}),
		probeResultWithCredits("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z", []remote.ResetCredit{
			{ID: "credit_1", Status: "available", Title: "Rate limit reset", ResetType: "codex_rate_limits", GrantedAt: "2026-06-12T01:20:48Z", ExpiresAt: "2026-07-12T01:20:48Z"},
			{ID: "credit_2", Status: "available", Title: "Rate limit reset", ResetType: "codex_rate_limits", GrantedAt: "2026-06-18T00:29:25Z", ExpiresAt: "2026-07-18T00:29:25Z"},
		}),
	}}
	notifier := &fakeNotifier{}
	srv := New(openStore(t), fetcher, notifier, Config{AccountLabel: "personal"})
	first, err := srv.RefreshNow(ctx)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if len(first.ResetGrants) != 0 || len(notifier.resetGrants) != 0 {
		t.Fatalf("first grant observation should seed silently, result=%#v notifications=%d", first.ResetGrants, len(notifier.resetGrants))
	}
	second, err := srv.RefreshNow(ctx)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if len(second.ResetGrants) != 1 || second.ResetGrants[0].CreditID != "credit_2" {
		t.Fatalf("expected one new grant event, got %#v", second.ResetGrants)
	}
	if len(notifier.resetGrants) != 1 || notifier.resetGrants[0].CreditID != "credit_2" {
		t.Fatalf("expected one reset grant notification, got %#v", notifier.resetGrants)
	}
	third, err := srv.RefreshNow(ctx)
	if err != nil {
		t.Fatalf("third refresh: %v", err)
	}
	if len(third.ResetGrants) != 0 || len(notifier.resetGrants) != 1 {
		t.Fatalf("expected deduped reset grant, result=%#v notifications=%d", third.ResetGrants, len(notifier.resetGrants))
	}
}

func TestRefreshEmitsRadarProbabilityAlertsOnUpwardMilestones(t *testing.T) {
	ctx := context.Background()
	fetcher := &fakeFetcher{results: []remote.ProbeResult{
		probeResult("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z"),
		probeResult("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z"),
		probeResult("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z"),
		probeResult("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z"),
	}}
	notifier := &fakeNotifier{}
	radarFetcher := &fakeRadarFetcher{currents: []radar.Current{
		radarCurrent("2026-06-01T12:00:00Z", 0.26),
		radarCurrent("2026-06-01T12:05:00Z", 0.49),
		radarCurrent("2026-06-01T12:10:00Z", 0.76),
		radarCurrent("2026-06-01T12:15:00Z", 0.30),
	}}
	srv := New(openStore(t), fetcher, notifier, Config{AccountLabel: "personal"})
	srv.SetRadarFetcher(radarFetcher)
	for i := 0; i < 4; i++ {
		if _, err := srv.RefreshNow(ctx); err != nil {
			t.Fatalf("refresh %d: %v", i, err)
		}
	}
	if len(notifier.radarAlerts) != 2 {
		t.Fatalf("expected two radar alerts, got %#v", notifier.radarAlerts)
	}
	if notifier.radarAlerts[0].Milestone != 25 || notifier.radarAlerts[1].Milestone != 75 {
		t.Fatalf("unexpected radar milestones: %#v", notifier.radarAlerts)
	}
}

func TestStartupHeartbeatOnlySendsOnce(t *testing.T) {
	ctx := context.Background()
	fetcher := &fakeFetcher{results: []remote.ProbeResult{
		probeResult("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z"),
		probeResult("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z"),
		probeResult("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z"),
	}}
	notifier := &fakeNotifier{}
	srv := New(openStore(t), fetcher, notifier, Config{AccountLabel: "personal", StartupHeartbeat: true})
	for i := 0; i < 3; i++ {
		if _, err := srv.RefreshNow(ctx); err != nil {
			t.Fatalf("refresh %d: %v", i, err)
		}
	}
	// First poll sends both the normal baseline notice and the startup heartbeat
	// in one message. Later polls must not keep sending heartbeat messages.
	if len(notifier.baselines) != 1 {
		t.Fatalf("expected one baseline/heartbeat notice, got %d", len(notifier.baselines))
	}
}

func TestHealthRecordsPollFailuresAndRecovery(t *testing.T) {
	ctx := context.Background()
	fetcher := fakeFetcherFunc(func(context.Context) (remote.ProbeResult, error) {
		return remote.ProbeResult{}, errors.New("401 auth exploded")
	})
	notifier := &fakeNotifier{}
	srv := New(openStore(t), fetcher, notifier, Config{})
	for i := 0; i < FailureAlertThreshold; i++ {
		if _, err := srv.RefreshNow(ctx); err == nil {
			t.Fatal("expected refresh failure")
		}
	}
	health, err := srv.Health(ctx)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if health.Status != HealthDegraded || health.ConsecutiveFailures != FailureAlertThreshold || health.FailureKind != "auth" {
		t.Fatalf("unexpected degraded health: %#v", health)
	}
	if len(notifier.health) != 1 || notifier.health[0].Recovery {
		t.Fatalf("expected one failure health notice, got %#v", notifier.health)
	}

	srv.fetcher = fakeFetcherFunc(func(context.Context) (remote.ProbeResult, error) {
		return probeResult("2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z"), nil
	})
	if _, err := srv.RefreshNow(ctx); err != nil {
		t.Fatalf("recovery refresh: %v", err)
	}
	health, err = srv.Health(ctx)
	if err != nil {
		t.Fatalf("health after recovery: %v", err)
	}
	if health.Status != HealthOK || health.ConsecutiveFailures != 0 {
		t.Fatalf("unexpected recovered health: %#v", health)
	}
	if len(notifier.health) != 2 || !notifier.health[1].Recovery {
		t.Fatalf("expected recovery notice, got %#v", notifier.health)
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
	health, err := srv.Health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Status == HealthDegraded && health.FailureKind == "interrupted" {
		t.Fatalf("active refresh reported interrupted: %#v", health)
	}
	if _, err := srv.RefreshNow(ctx); err != ErrRefreshInProgress {
		t.Fatalf("expected in-progress error, got %v", err)
	}
	close(release)
	if err := <-errs; err != nil {
		t.Fatalf("first refresh failed: %v", err)
	}
}

func TestHealthMarksExpiredAttemptInterrupted(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if err := st.SetSetting(ctx, SettingPollAttemptAt, time.Now().Add(-DefaultRefreshTimeout-time.Second).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	srv := New(st, nil, nil, Config{})
	health, err := srv.Health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != HealthDegraded || health.FailureKind != "interrupted" {
		t.Fatalf("unexpected health: %#v", health)
	}
}

func TestHealthDegradesForQueueWithoutOverwritingPollFailure(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	now := time.Now().UTC()
	if err := st.StageTelegramUpdates(ctx, "bot", []store.TelegramUpdateInput{{UpdateID: 1, RawJSON: `{}`}}, now); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.MarkTelegramUpdateDead(ctx, "bot", 1, "bad update", now); err != nil || !ok {
		t.Fatalf("dead ok=%v err=%v", ok, err)
	}
	if err := st.SetSetting(ctx, SettingPollFailureError, "401 auth exploded"); err != nil {
		t.Fatal(err)
	}
	health, err := New(st, nil, nil, Config{}).Health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != HealthDegraded || health.QueueReason != "dead_letters" || health.TelegramInbox.Dead != 1 {
		t.Fatalf("health: %#v", health)
	}
	if health.FailureKind != "auth" || health.LastError != "401 auth exploded" {
		t.Fatalf("poll failure overwritten: %#v", health)
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

type atomicPollStore struct {
	Store
	input      store.CodexPollInput
	result     store.CodexPollResult
	err        error
	applyCalls int
	oldCalls   int
}

func (s *atomicPollStore) ApplyCodexPoll(_ context.Context, input store.CodexPollInput) (store.CodexPollResult, error) {
	s.applyCalls++
	s.input = input
	return s.result, s.err
}

func (s *atomicPollStore) LoadWindowStates(context.Context, string) (map[string]resetwatch.WindowState, error) {
	s.oldCalls++
	return nil, errors.New("legacy LoadWindowStates called")
}

func (s *atomicPollStore) ApplyDecision(context.Context, resetwatch.Observation, resetwatch.Decision, ...string) (int, error) {
	s.oldCalls++
	return 0, errors.New("legacy ApplyDecision called")
}

func (s *atomicPollStore) InsertWarningEvents(context.Context, []resetwatch.WarningEvent, ...string) ([]resetwatch.WarningEvent, error) {
	s.oldCalls++
	return nil, errors.New("legacy InsertWarningEvents called")
}

func (s *atomicPollStore) InsertGrantExpiryWarningEvents(context.Context, []resetwatch.GrantExpiryWarning, ...string) ([]resetwatch.GrantExpiryWarning, error) {
	s.oldCalls++
	return nil, errors.New("legacy InsertGrantExpiryWarningEvents called")
}

func (s *atomicPollStore) InsertResetGrantEvents(context.Context, resetwatch.Observation, []resetwatch.ResetGrantEvent, ...string) ([]resetwatch.ResetGrantEvent, error) {
	s.oldCalls++
	return nil, errors.New("legacy InsertResetGrantEvents called")
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
	baselines     []BaselineNotice
	resets        []resetwatch.Event
	warnings      []resetwatch.WarningEvent
	grantWarnings []resetwatch.GrantExpiryWarning
	resetGrants   []resetwatch.ResetGrantEvent
	radarAlerts   []radar.ProbabilityAlert
	health        []HealthNotice
}

func (n *fakeNotifier) NotifyBaseline(_ context.Context, notice BaselineNotice) error {
	n.baselines = append(n.baselines, notice)
	return nil
}

func (n *fakeNotifier) NotifyReset(_ context.Context, event resetwatch.Event) error {
	n.resets = append(n.resets, event)
	return nil
}

func (n *fakeNotifier) NotifyLimitWarning(_ context.Context, warning resetwatch.WarningEvent) error {
	n.warnings = append(n.warnings, warning)
	return nil
}

func (n *fakeNotifier) NotifyGrantExpiryWarning(_ context.Context, warning resetwatch.GrantExpiryWarning) error {
	n.grantWarnings = append(n.grantWarnings, warning)
	return nil
}

func (n *fakeNotifier) NotifyResetGrant(_ context.Context, event resetwatch.ResetGrantEvent) error {
	n.resetGrants = append(n.resetGrants, event)
	return nil
}

func (n *fakeNotifier) NotifyRadarProbability(_ context.Context, alert radar.ProbabilityAlert) error {
	n.radarAlerts = append(n.radarAlerts, alert)
	return nil
}

func (n *fakeNotifier) NotifyHealth(_ context.Context, notice HealthNotice) error {
	n.health = append(n.health, notice)
	return nil
}

type fakeRadarFetcher struct {
	currents []radar.Current
	index    int
}

func (f *fakeRadarFetcher) Fetch(context.Context) (radar.Current, error) {
	if f.index >= len(f.currents) {
		return f.currents[len(f.currents)-1], nil
	}
	current := f.currents[f.index]
	f.index++
	return current, nil
}

func radarCurrent(checkedAt string, probability float64) radar.Current {
	return radar.Current{
		SchemaVersion: "1.0",
		Service:       "codex-reset-radar",
		CheckedAt:     checkedAt,
		Status:        "none",
		Prediction: &radar.Prediction{
			Level:          "high",
			Probability24H: probability,
		},
	}
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

func probeResultWithGrant(weeklyReset, fiveReset, expiresAt string) remote.ProbeResult {
	return probeResultWithGrantID(weeklyReset, fiveReset, "credit_1", "2026-05-07T12:00:00Z", expiresAt)
}

func probeResultWithGrantID(weeklyReset, fiveReset, id, grantedAt, expiresAt string) remote.ProbeResult {
	return probeResultWithCredits(weeklyReset, fiveReset, []remote.ResetCredit{
		{ID: id, Status: "available", Title: "Rate limit reset", ResetType: "codex_rate_limits", GrantedAt: grantedAt, ExpiresAt: expiresAt},
	})
}

func probeResultWithCredits(weeklyReset, fiveReset string, credits []remote.ResetCredit) remote.ProbeResult {
	result := probeResult(weeklyReset, fiveReset)
	result.ResetCredits = credits
	return result
}
