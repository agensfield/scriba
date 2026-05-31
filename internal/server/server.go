package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agensfield/scriba/internal/buildinfo"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
	remotecodex "github.com/agensfield/scriba/internal/remote/codex"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
)

const (
	DefaultPollInterval     = 5 * time.Minute
	DefaultBackoff          = 30 * time.Second
	SettingPollInterval     = "poll_interval"
	SettingLastPruneAt      = "last_prune_at"
	SettingPollSuccessAt    = "poll_success_at"
	SettingPollFailureAt    = "poll_failure_at"
	SettingPollFailureCount = "poll_failure_count"
	SettingPollFailureError = "poll_failure_error"
	SettingHealthAlertState = "health_alert_state"
	FailureAlertThreshold   = 3
)

var ErrRefreshInProgress = errors.New("refresh already in progress")

type Store interface {
	LoadWindowStates(context.Context, string) (map[string]resetwatch.WindowState, error)
	ApplyDecision(context.Context, resetwatch.Observation, resetwatch.Decision) (int, error)
	GetSetting(context.Context, string) (string, bool, error)
	SetSetting(context.Context, string, string) error
	LoadLastResetEvent(context.Context) (resetwatch.Event, bool, error)
	LoadLatestObservation(context.Context) (resetwatch.Observation, bool, error)
	PruneObservations(context.Context, time.Time, bool) (store.PruneResult, error)
	InsertWarningEvents(context.Context, []resetwatch.WarningEvent) ([]resetwatch.WarningEvent, error)
	Stats(context.Context) (store.Stats, error)
}

type Fetcher interface {
	FetchLimits(context.Context) (remote.ProbeResult, error)
}

type Notifier interface {
	NotifyBaseline(context.Context, BaselineNotice) error
	NotifyReset(context.Context, resetwatch.Event) error
	NotifyLimitWarning(context.Context, resetwatch.WarningEvent) error
	NotifyHealth(context.Context, HealthNotice) error
}

type Config struct {
	AccountLabel             string
	JokeTone                 string
	StartupHeartbeat         bool
	ObservationRetentionDays int
}

type Server struct {
	store    Store
	fetcher  Fetcher
	notifier Notifier
	cfg      Config
	logger   *slog.Logger

	mu         sync.Mutex
	refreshing bool
	heartbeat  bool
}

type BaselineNotice struct {
	Account      resetwatch.Account
	ObservedAt   time.Time
	Windows      []resetwatch.Window
	SnapshotJSON []byte
}

type PollResult struct {
	Observation resetwatch.Observation
	Decision    resetwatch.Decision
	Inserted    int
	Warnings    []resetwatch.WarningEvent
	Baseline    bool
}

type HealthStatus string

const (
	HealthUnknown  HealthStatus = "unknown"
	HealthOK       HealthStatus = "ok"
	HealthStale    HealthStatus = "stale"
	HealthDegraded HealthStatus = "degraded"
)

type Stats struct {
	Store                    store.Stats   `json:"store"`
	PollInterval             time.Duration `json:"pollInterval"`
	ObservationRetentionDays int           `json:"observationRetentionDays"`
	Health                   Health        `json:"health"`
	Version                  string        `json:"version"`
	Commit                   string        `json:"commit"`
}

type Health struct {
	Status                   HealthStatus  `json:"status"`
	Version                  string        `json:"version"`
	Commit                   string        `json:"commit"`
	PollInterval             time.Duration `json:"pollInterval"`
	ObservationRetentionDays int           `json:"observationRetentionDays"`
	LastSuccessAt            *time.Time    `json:"lastSuccessAt,omitempty"`
	LastFailureAt            *time.Time    `json:"lastFailureAt,omitempty"`
	LastError                string        `json:"lastError,omitempty"`
	FailureKind              string        `json:"failureKind,omitempty"`
	ConsecutiveFailures      int           `json:"consecutiveFailures"`
	NextPollEstimateAt       *time.Time    `json:"nextPollEstimateAt,omitempty"`
	StaleAfter               time.Duration `json:"staleAfter"`
	IsStale                  bool          `json:"isStale"`
}

type HealthNotice struct {
	Health   Health
	Recovery bool
}

type CodexFetcher struct{}

type NoopNotifier struct{}

func New(st Store, fetcher Fetcher, notifier Notifier, cfg Config) *Server {
	if fetcher == nil {
		fetcher = CodexFetcher{}
	}
	if notifier == nil {
		notifier = NoopNotifier{}
	}
	if cfg.AccountLabel == "" {
		cfg.AccountLabel = "personal"
	}
	if cfg.ObservationRetentionDays == 0 {
		cfg.ObservationRetentionDays = 120
	}
	return &Server{
		store:     st,
		fetcher:   fetcher,
		notifier:  notifier,
		cfg:       cfg,
		logger:    slog.Default(),
		heartbeat: cfg.StartupHeartbeat,
	}
}

func (s *Server) SetNotifier(notifier Notifier) {
	if notifier == nil {
		notifier = NoopNotifier{}
	}
	s.notifier = notifier
}

func (s *Server) Run(ctx context.Context) error {
	backoff := DefaultBackoff
	for {
		if _, err := s.RefreshNow(ctx); err != nil {
			s.logger.Warn("scriba server poll failed", "error", err)
			if !sleep(ctx, backoff) {
				return ctx.Err()
			}
			backoff = minDuration(backoff*2, 5*time.Minute)
		} else {
			backoff = DefaultBackoff
			interval, err := s.PollInterval(ctx)
			if err != nil {
				s.logger.Warn("scriba poll interval setting failed", "error", err)
				interval = DefaultPollInterval
			}
			if !sleep(ctx, interval) {
				return ctx.Err()
			}
		}
	}
}

func (s *Server) RefreshNow(ctx context.Context) (PollResult, error) {
	if !s.beginRefresh() {
		return PollResult{}, ErrRefreshInProgress
	}
	defer s.endRefresh()
	result, err := s.pollOnce(ctx)
	if err != nil {
		if recordErr := s.recordPollFailure(ctx, err); recordErr != nil {
			s.logger.Warn("scriba poll failure recording failed", "error", recordErr)
		}
		return PollResult{}, err
	}
	if err := s.recordPollSuccess(ctx); err != nil {
		s.logger.Warn("scriba poll success recording failed", "error", err)
	}
	return result, nil
}

func (s *Server) PollInterval(ctx context.Context) (time.Duration, error) {
	value, ok, err := s.store.GetSetting(ctx, SettingPollInterval)
	if err != nil {
		return 0, err
	}
	if !ok || strings.TrimSpace(value) == "" {
		return DefaultPollInterval, nil
	}
	interval, err := time.ParseDuration(value)
	if err != nil || interval <= 0 {
		return DefaultPollInterval, err
	}
	return interval, nil
}

func (s *Server) SetPollInterval(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("poll interval must be positive")
	}
	return s.store.SetSetting(ctx, SettingPollInterval, interval.String())
}

func (s *Server) LastResetEvent(ctx context.Context) (resetwatch.Event, bool, error) {
	return s.store.LoadLastResetEvent(ctx)
}

func (s *Server) LatestObservation(ctx context.Context) (resetwatch.Observation, bool, error) {
	return s.store.LoadLatestObservation(ctx)
}

func (s *Server) Stats(ctx context.Context) (Stats, error) {
	storeStats, err := s.store.Stats(ctx)
	if err != nil {
		return Stats{}, err
	}
	health, err := s.Health(ctx)
	if err != nil {
		return Stats{}, err
	}
	return Stats{
		Store:                    storeStats,
		PollInterval:             health.PollInterval,
		ObservationRetentionDays: s.cfg.ObservationRetentionDays,
		Health:                   health,
		Version:                  buildinfo.Version,
		Commit:                   buildinfo.Commit,
	}, nil
}

func (s *Server) Health(ctx context.Context) (Health, error) {
	interval, err := s.PollInterval(ctx)
	if err != nil {
		return Health{}, err
	}
	health := Health{
		Status:                   HealthUnknown,
		Version:                  buildinfo.Version,
		Commit:                   buildinfo.Commit,
		PollInterval:             interval,
		ObservationRetentionDays: s.cfg.ObservationRetentionDays,
		StaleAfter:               2 * interval,
	}
	success, ok, err := s.timeSetting(ctx, SettingPollSuccessAt)
	if err != nil {
		return health, err
	}
	if ok {
		health.LastSuccessAt = &success
	}
	failure, ok, err := s.timeSetting(ctx, SettingPollFailureAt)
	if err != nil {
		return health, err
	}
	if ok {
		health.LastFailureAt = &failure
	}
	count, err := s.intSetting(ctx, SettingPollFailureCount)
	if err != nil {
		return health, err
	}
	health.ConsecutiveFailures = count
	if value, ok, err := s.store.GetSetting(ctx, SettingPollFailureError); err != nil {
		return health, err
	} else if ok && strings.TrimSpace(value) != "" {
		health.LastError = value
		health.FailureKind = classifyPollError(value)
	}
	now := time.Now().UTC()
	if health.LastFailureAt != nil && (health.LastSuccessAt == nil || health.LastFailureAt.After(*health.LastSuccessAt)) && count > 0 {
		health.Status = HealthDegraded
		next := health.LastFailureAt.Add(pollBackoff(count))
		health.NextPollEstimateAt = &next
		return health, nil
	}
	if health.LastSuccessAt != nil {
		next := health.LastSuccessAt.Add(interval)
		health.NextPollEstimateAt = &next
		health.IsStale = now.Sub(*health.LastSuccessAt) > health.StaleAfter
		if health.IsStale {
			health.Status = HealthStale
		} else {
			health.Status = HealthOK
		}
	}
	return health, nil
}

func (s *Server) recordPollFailure(ctx context.Context, pollErr error) error {
	count, err := s.intSetting(ctx, SettingPollFailureCount)
	if err != nil {
		return err
	}
	count++
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.store.SetSetting(ctx, SettingPollFailureAt, now); err != nil {
		return err
	}
	if err := s.store.SetSetting(ctx, SettingPollFailureCount, strconv.Itoa(count)); err != nil {
		return err
	}
	if err := s.store.SetSetting(ctx, SettingPollFailureError, pollErr.Error()); err != nil {
		return err
	}
	if count < FailureAlertThreshold {
		return nil
	}
	alertState, _, err := s.store.GetSetting(ctx, SettingHealthAlertState)
	if err != nil {
		return err
	}
	if alertState == "failing" {
		return nil
	}
	health, err := s.Health(ctx)
	if err != nil {
		return err
	}
	if notifyErr := s.notifier.NotifyHealth(ctx, HealthNotice{Health: health}); notifyErr != nil {
		s.logger.Warn("scriba health failure notification failed", "error", notifyErr)
		return notifyErr
	}
	return s.store.SetSetting(ctx, SettingHealthAlertState, "failing")
}

func (s *Server) recordPollSuccess(ctx context.Context) error {
	wasFailing := false
	if alertState, ok, err := s.store.GetSetting(ctx, SettingHealthAlertState); err != nil {
		return err
	} else if ok && alertState == "failing" {
		wasFailing = true
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.store.SetSetting(ctx, SettingPollSuccessAt, now); err != nil {
		return err
	}
	if err := s.store.SetSetting(ctx, SettingPollFailureCount, "0"); err != nil {
		return err
	}
	if err := s.store.SetSetting(ctx, SettingPollFailureError, ""); err != nil {
		return err
	}
	if err := s.store.SetSetting(ctx, SettingHealthAlertState, "ok"); err != nil {
		return err
	}
	if !wasFailing {
		return nil
	}
	health, err := s.Health(ctx)
	if err != nil {
		return err
	}
	if notifyErr := s.notifier.NotifyHealth(ctx, HealthNotice{Health: health, Recovery: true}); notifyErr != nil {
		s.logger.Warn("scriba health recovery notification failed", "error", notifyErr)
		return notifyErr
	}
	return nil
}

func (s *Server) pollOnce(ctx context.Context) (PollResult, error) {
	result, err := s.fetcher.FetchLimits(ctx)
	if err != nil {
		return PollResult{}, err
	}
	if !result.AuthState.OK {
		if result.AuthState.Error == "" {
			return PollResult{}, errors.New("codex auth unavailable")
		}
		return PollResult{}, errors.New(result.AuthState.Error)
	}
	obs := s.observation(result)
	if len(obs.Windows) == 0 {
		return PollResult{}, errors.New("codex limits response had no reset windows")
	}
	states, err := s.store.LoadWindowStates(ctx, obs.Account.Ref)
	if err != nil {
		return PollResult{}, err
	}
	baseline := states[resetwatch.StateKey(obs.Account.Ref, resetwatch.LabelWeeklyLimit)].StableResetAt.IsZero()
	decision := resetwatch.Decide(obs, states, resetwatch.Options{
		JokeChooser: resetwatch.CatalogJokeChooser{Tone: s.cfg.JokeTone},
	})
	inserted, err := s.store.ApplyDecision(ctx, obs, decision)
	if err != nil {
		return PollResult{}, err
	}
	heartbeat := s.consumeStartupHeartbeat()
	if baseline || heartbeat {
		if err := s.notifier.NotifyBaseline(ctx, BaselineNotice{Account: obs.Account, ObservedAt: obs.ObservedAt, Windows: obs.Windows, SnapshotJSON: obs.SnapshotJSON}); err != nil {
			s.logger.Warn("scriba baseline notification failed", "error", err)
		}
	}
	if err := s.pruneIfDue(ctx); err != nil {
		s.logger.Warn("scriba observation prune failed", "error", err)
	}
	warnings, err := s.store.InsertWarningEvents(ctx, resetwatch.WarningCandidates(obs))
	if err != nil {
		return PollResult{}, err
	}
	if inserted > 0 {
		for _, event := range decision.Events {
			if err := s.notifier.NotifyReset(ctx, event); err != nil {
				s.logger.Warn("scriba reset notification failed", "event_id", event.ID, "error", err)
			}
		}
	}
	for _, warning := range warnings {
		if err := s.notifier.NotifyLimitWarning(ctx, warning); err != nil {
			s.logger.Warn("scriba limit warning notification failed", "warning_id", warning.ID, "error", err)
		}
	}
	return PollResult{Observation: obs, Decision: decision, Inserted: inserted, Warnings: warnings, Baseline: baseline}, nil
}

func (s *Server) PruneObservations(ctx context.Context, compact bool) (store.PruneResult, error) {
	days := s.cfg.ObservationRetentionDays
	if days <= 0 {
		return store.PruneResult{}, errors.New("observation retention must be positive")
	}
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	return s.store.PruneObservations(ctx, cutoff, compact)
}

func (s *Server) timeSetting(ctx context.Context, key string) (time.Time, bool, error) {
	value, ok, err := s.store.GetSetting(ctx, key)
	if err != nil || !ok || strings.TrimSpace(value) == "" {
		return time.Time{}, ok, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false, nil
	}
	return parsed.UTC(), true, nil
}

func (s *Server) intSetting(ctx context.Context, key string) (int, error) {
	value, ok, err := s.store.GetSetting(ctx, key)
	if err != nil || !ok || strings.TrimSpace(value) == "" {
		return 0, err
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, nil
	}
	return parsed, nil
}

func (s *Server) pruneIfDue(ctx context.Context) error {
	value, ok, err := s.store.GetSetting(ctx, SettingLastPruneAt)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if ok {
		last, err := time.Parse(time.RFC3339Nano, value)
		if err == nil && now.Sub(last) < 24*time.Hour {
			return nil
		}
	}
	result, err := s.PruneObservations(ctx, true)
	if err != nil {
		return err
	}
	if err := s.store.SetSetting(ctx, SettingLastPruneAt, now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if result.DeletedObservations > 0 || result.DeletedWindows > 0 {
		s.logger.Info("scriba pruned observations", "observations", result.DeletedObservations, "windows", result.DeletedWindows, "cutoff", result.Cutoff.Format(time.RFC3339))
	}
	return nil
}

func (s *Server) observation(result remote.ProbeResult) resetwatch.Observation {
	plan := planFromLines(result.Lines)
	auth := result.AuthState
	account := resetwatch.Account{
		Ref:   accountRef(auth),
		Label: s.cfg.AccountLabel,
		Email: auth.Email,
		Plan:  plan,
	}
	return resetwatch.Observation{
		ProviderID:   resetwatch.ProviderCodex,
		Account:      account,
		ObservedAt:   time.Now().UTC(),
		Windows:      resetwatch.FromMetricLines(result.Lines),
		SnapshotJSON: resetwatch.SnapshotJSON(result),
	}
}

func (s *Server) beginRefresh() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refreshing {
		return false
	}
	s.refreshing = true
	return true
}

func (s *Server) endRefresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshing = false
}

func (s *Server) consumeStartupHeartbeat() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.heartbeat {
		return false
	}
	s.heartbeat = false
	return true
}

func (CodexFetcher) FetchLimits(ctx context.Context) (remote.ProbeResult, error) {
	return remotecodex.FetchLimits(ctx, nil)
}

func (NoopNotifier) NotifyBaseline(context.Context, BaselineNotice) error {
	return nil
}

func (NoopNotifier) NotifyReset(context.Context, resetwatch.Event) error {
	return nil
}

func (NoopNotifier) NotifyLimitWarning(context.Context, resetwatch.WarningEvent) error {
	return nil
}

func (NoopNotifier) NotifyHealth(context.Context, HealthNotice) error {
	return nil
}

func accountRef(auth remote.AuthState) string {
	if auth.AccountID != "" {
		return auth.AccountID
	}
	stable := auth.Email
	if stable == "" {
		stable = auth.Source
	}
	if stable == "" {
		stable = "unknown"
	}
	sum := sha256.Sum256([]byte(stable))
	return "acct_" + hex.EncodeToString(sum[:8])
}

func planFromLines(lines []model.MetricLine) string {
	for _, line := range lines {
		if line.Type == "badge" && line.Label == "Plan" {
			return line.Text
		}
	}
	return ""
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func pollBackoff(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return DefaultBackoff
	case attempts == 2:
		return time.Minute
	case attempts == 3:
		return 2 * time.Minute
	case attempts == 4:
		return 5 * time.Minute
	default:
		return 5 * time.Minute
	}
}

func classifyPollError(message string) string {
	lowered := strings.ToLower(message)
	switch {
	case strings.Contains(lowered, "auth"), strings.Contains(lowered, "token"), strings.Contains(lowered, "unauthorized"), strings.Contains(lowered, "forbidden"), strings.Contains(lowered, "401"), strings.Contains(lowered, "403"):
		return "auth"
	case strings.Contains(lowered, "timeout"), strings.Contains(lowered, "deadline"), strings.Contains(lowered, "temporary"), strings.Contains(lowered, "connection"), strings.Contains(lowered, "network"):
		return "network"
	default:
		return "backend"
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

var _ Store = (*store.Store)(nil)
