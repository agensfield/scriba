package server

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agensfield/scriba/internal/budget"
	"github.com/agensfield/scriba/internal/buildinfo"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/radar"
	"github.com/agensfield/scriba/internal/remote"
	remotecodex "github.com/agensfield/scriba/internal/remote/codex"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
)

const (
	DefaultPollInterval         = 5 * time.Minute
	DefaultBackoff              = 30 * time.Second
	DefaultRefreshTimeout       = 90 * time.Second
	SettingPollInterval         = "poll_interval"
	maxObservationRetentionDays = 36500
	SettingPollAttemptAt        = "poll_attempt_at"
	SettingLastPruneAt          = "last_prune_at"
	SettingPollSuccessAt        = "poll_success_at"
	SettingPollFailureAt        = "poll_failure_at"
	SettingPollFailureCount     = "poll_failure_count"
	SettingPollFailureError     = "poll_failure_error"
	SettingHealthAlertState     = "health_alert_state"
	SettingRadarMilestone       = "radar_probability_milestone"
	FailureAlertThreshold       = 3
)

var (
	ErrRefreshInProgress  = errors.New("refresh already in progress")
	ErrAllProfilesFailed  = errors.New("all profiles failed")
	ErrProfileAuthPaths   = errors.New("explicit profile requires auth paths")
	ErrProfileUnavailable = errors.New("profile unavailable")
)

type Store interface {
	ApplyCodexPoll(context.Context, store.CodexPollInput) (store.CodexPollResult, error)
	GetSetting(context.Context, string) (string, bool, error)
	SetSetting(context.Context, string, string) error
	LoadLastResetEvent(context.Context) (resetwatch.Event, bool, error)
	LoadLatestObservation(context.Context) (resetwatch.Observation, bool, error)
	LoadLatestObservationForProfile(context.Context, string) (resetwatch.Observation, bool, error)
	PruneObservations(context.Context, time.Time, bool) (store.PruneResult, error)
	InsertRadarAlertEvent(context.Context, radar.ProbabilityAlert, ...string) (bool, error)
	Stats(context.Context) (store.Stats, error)
	ListProfileHealth(context.Context) ([]store.ProfileHealth, error)
	RecordProfilePollAttempt(context.Context, string, time.Time) error
	RecordProfilePollSuccess(context.Context, string, time.Time, time.Time) error
	RecordProfilePollFailure(context.Context, string, time.Time, time.Time, string, string) error
	AbortProfilePollAttempt(context.Context, string, time.Time) error
	CompareAndSwapProfileAlertState(context.Context, string, string, string) (bool, error)
}

type Fetcher interface {
	FetchLimits(context.Context) (remote.ProbeResult, error)
}

type ProfileFetcher interface {
	FetchProfileLimits(context.Context, Profile) (remote.ProbeResult, error)
}

type RadarFetcher interface {
	Fetch(context.Context) (radar.Current, error)
}

type Notifier interface {
	NotifyBaseline(context.Context, BaselineNotice) error
	NotifyReset(context.Context, resetwatch.Event) error
	NotifyLimitWarning(context.Context, resetwatch.WarningEvent) error
	NotifyPacingWarning(context.Context, budget.PacingAlert) error
	NotifyGrantExpiryWarning(context.Context, resetwatch.GrantExpiryWarning) error
	NotifyResetGrant(context.Context, resetwatch.ResetGrantEvent) error
	NotifyRadarProbability(context.Context, radar.ProbabilityAlert) error
	NotifyHealth(context.Context, HealthNotice) error
}

type Config struct {
	Profiles                 []Profile
	NotificationTarget       string
	NotificationTargets      []string
	AccountLabel             string
	JokeTone                 string
	StartupHeartbeat         bool
	ObservationRetentionDays int
}

type Profile struct {
	Ref, Label         string
	AuthPaths          []string
	Default            bool
	AllowAuthDiscovery bool
}

type ProfileIdentity struct {
	Ref   string `json:"ref"`
	Label string `json:"label"`
}

type Server struct {
	store    Store
	fetcher  Fetcher
	radar    RadarFetcher
	notifier Notifier
	cfg      Config
	logger   *slog.Logger

	mu             sync.Mutex
	refreshing     bool
	heartbeat      bool
	intervalCh     chan struct{}
	profileTimeout time.Duration
}

type BaselineNotice struct {
	Profile      ProfileIdentity
	Account      resetwatch.Account
	ObservedAt   time.Time
	Windows      []resetwatch.Window
	SnapshotJSON []byte
}

type PollResult struct {
	Profile        ProfileIdentity
	Observation    resetwatch.Observation
	Decision       resetwatch.Decision
	Inserted       int
	Warnings       []resetwatch.WarningEvent
	PacingWarnings []budget.PacingAlert
	GrantWarnings  []resetwatch.GrantExpiryWarning
	ResetGrants    []resetwatch.ResetGrantEvent
	RadarAlerts    []radar.ProbabilityAlert
	Baseline       bool
}

type ProfilePollFailure struct {
	Kind, Code string
}

type ProfilePollResult struct {
	Profile ProfileIdentity
	PollResult
	Failure *ProfilePollFailure
}

type RefreshResult struct {
	Profiles    []ProfilePollResult
	RadarAlerts []radar.ProbabilityAlert
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
	Status                   HealthStatus     `json:"status"`
	Version                  string           `json:"version"`
	Commit                   string           `json:"commit"`
	PollInterval             time.Duration    `json:"pollInterval"`
	ObservationRetentionDays int              `json:"observationRetentionDays"`
	LastSuccessAt            *time.Time       `json:"lastSuccessAt,omitempty"`
	LastAttemptAt            *time.Time       `json:"lastAttemptAt,omitempty"`
	LastFailureAt            *time.Time       `json:"lastFailureAt,omitempty"`
	LastError                string           `json:"lastError,omitempty"`
	FailureKind              string           `json:"failureKind,omitempty"`
	ConsecutiveFailures      int              `json:"consecutiveFailures"`
	NextPollEstimateAt       *time.Time       `json:"nextPollEstimateAt,omitempty"`
	StaleAfter               time.Duration    `json:"staleAfter"`
	IsStale                  bool             `json:"isStale"`
	QueueReason              string           `json:"queueReason,omitempty"`
	Outbox                   store.QueueStats `json:"outbox"`
	TelegramInbox            store.InboxStats `json:"telegramInbox"`
	Profiles                 []ProfileHealth  `json:"profiles,omitempty"`
}

type ProfileHealth struct {
	Profile             ProfileIdentity `json:"profile"`
	IsDefault           bool            `json:"isDefault"`
	Status              HealthStatus    `json:"status"`
	LastSuccessAt       *time.Time      `json:"lastSuccessAt,omitempty"`
	LastAttemptAt       *time.Time      `json:"lastAttemptAt,omitempty"`
	LastFailureAt       *time.Time      `json:"lastFailureAt,omitempty"`
	FailureKind         string          `json:"failureKind,omitempty"`
	LastErrorCode       string          `json:"lastErrorCode,omitempty"`
	ConsecutiveFailures int             `json:"consecutiveFailures"`
	NextPollEstimateAt  *time.Time      `json:"nextPollEstimateAt,omitempty"`
	IsStale             bool            `json:"isStale"`
}

type HealthNotice struct {
	Profile  ProfileIdentity
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
	if len(cfg.Profiles) == 0 {
		cfg.Profiles = []Profile{{Ref: "default", Label: cfg.AccountLabel, Default: true, AllowAuthDiscovery: true}}
	}
	if cfg.ObservationRetentionDays == 0 {
		cfg.ObservationRetentionDays = 120
	}
	return &Server{
		store:          st,
		fetcher:        fetcher,
		notifier:       notifier,
		cfg:            cfg,
		logger:         slog.Default(),
		heartbeat:      cfg.StartupHeartbeat,
		intervalCh:     make(chan struct{}, 1),
		profileTimeout: DefaultRefreshTimeout,
	}
}

func (s *Server) SetRadarFetcher(fetcher RadarFetcher) {
	s.radar = fetcher
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
			if ctx.Err() != nil {
				return ctx.Err()
			}
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
			if !s.waitPollInterval(ctx, interval) {
				return ctx.Err()
			}
		}
	}
}

func (s *Server) RefreshNow(ctx context.Context) (PollResult, error) {
	result, err := s.RefreshProfilesNow(ctx)
	for _, profile := range result.Profiles {
		if profile.Failure == nil && profile.Profile.Ref == s.defaultProfile().Ref {
			profile.RadarAlerts = result.RadarAlerts
			return profile.PollResult, err
		}
	}
	for _, profile := range result.Profiles {
		if profile.Failure == nil {
			profile.RadarAlerts = result.RadarAlerts
			return profile.PollResult, err
		}
	}
	return PollResult{RadarAlerts: result.RadarAlerts}, err
}

func (s *Server) RefreshProfilesNow(ctx context.Context) (RefreshResult, error) {
	if !s.beginRefresh() {
		return RefreshResult{}, ErrRefreshInProgress
	}
	defer s.endRefresh()
	return s.refreshProfiles(ctx)
}

func (s *Server) defaultProfile() Profile {
	for _, profile := range s.cfg.Profiles {
		if profile.Default {
			return profile
		}
	}
	return s.cfg.Profiles[0]
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
	if err := s.store.SetSetting(ctx, SettingPollInterval, interval.String()); err != nil {
		return err
	}
	s.notifyIntervalChanged()
	return nil
}

func (s *Server) LastResetEvent(ctx context.Context) (resetwatch.Event, bool, error) {
	return s.store.LoadLastResetEvent(ctx)
}

func (s *Server) LatestObservation(ctx context.Context) (resetwatch.Observation, bool, error) {
	return s.store.LoadLatestObservation(ctx)
}

func (s *Server) LatestObservationForProfile(ctx context.Context, profileRef string) (resetwatch.Observation, bool, error) {
	profile, err := s.configuredProfile(profileRef)
	if err != nil {
		return resetwatch.Observation{}, false, err
	}
	return s.store.LoadLatestObservationForProfile(ctx, profile.Ref)
}

func (s *Server) CodexProfile(ctx context.Context) (remotecodex.ProfileResult, error) {
	profile, err := remotecodex.FetchProfile(ctx, nil)
	if err != nil {
		return remotecodex.ProfileResult{}, err
	}
	profile.SchemaVersion = model.SchemaVersion
	return profile, nil
}

func (s *Server) CodexProfileForProfile(ctx context.Context, profileRef string) (remotecodex.ProfileResult, error) {
	profile, err := s.configuredProfile(profileRef)
	if err != nil {
		return remotecodex.ProfileResult{}, err
	}
	if len(profile.AuthPaths) == 0 && !profile.AllowAuthDiscovery {
		return remotecodex.ProfileResult{}, ErrProfileAuthPaths
	}
	result, err := remotecodex.FetchProfileWithOptions(ctx, nil, remotecodex.FetchOptions{AuthPaths: append([]string(nil), profile.AuthPaths...)})
	if err != nil {
		return remotecodex.ProfileResult{}, err
	}
	result.SchemaVersion = model.SchemaVersion
	return sanitizeCodexProfileResult(result), nil
}

func sanitizeCodexProfileResult(result remotecodex.ProfileResult) remotecodex.ProfileResult {
	result.AuthState.Source = ""
	result.AuthState.Error = ""
	result.AuthState.AccessToken = ""
	result.AuthState.AccountID = ""
	if result.Metadata.StatsError != nil {
		result.Metadata.StatsError = "profile stats unavailable"
	}
	for i := range result.Provenance {
		result.Provenance[i].Error = ""
	}
	return result
}

func (s *Server) configuredProfile(ref string) (Profile, error) {
	trimmed := strings.TrimSpace(ref)
	if ref != trimmed {
		return Profile{}, ErrProfileUnavailable
	}
	ref = trimmed
	for _, profile := range s.cfg.Profiles {
		if (ref == "" && profile.Default) || (ref != "" && profile.Ref == ref) {
			return profile, nil
		}
	}
	return Profile{}, ErrProfileUnavailable
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
	queueStats, err := s.store.Stats(ctx)
	if err != nil {
		return health, err
	}
	health.Outbox = queueStats.Outbox
	health.TelegramInbox = queueStats.TelegramInbox
	profileRows, err := s.store.ListProfileHealth(ctx)
	if err != nil {
		return health, err
	}
	if len(profileRows) > 0 {
		health.Profiles = make([]ProfileHealth, 0, len(profileRows))
		health.Status = HealthOK
		rowsByRef := make(map[string]store.ProfileHealth, len(profileRows))
		for _, row := range profileRows {
			rowsByRef[row.ProfileRef] = row
		}
		for _, configured := range s.cfg.Profiles {
			row, exists := rowsByRef[configured.Ref]
			if !exists || !row.Enabled {
				profile := ProfileHealth{Profile: ProfileIdentity{Ref: configured.Ref, Label: configured.Label}, IsDefault: configured.Default, Status: HealthUnknown}
				health.Profiles = append(health.Profiles, profile)
				health.Status = worseHealth(health.Status, profile.Status)
				continue
			}
			profile := profileHealthFromStore(row, interval, health.StaleAfter, time.Now().UTC())
			profile.IsDefault = configured.Default
			health.Profiles = append(health.Profiles, profile)
			health.Status = worseHealth(health.Status, profile.Status)
			if configured.Default {
				health.LastSuccessAt = profile.LastSuccessAt
				health.LastAttemptAt = profile.LastAttemptAt
				health.LastFailureAt = profile.LastFailureAt
				health.FailureKind = profile.FailureKind
				health.LastError = profile.LastErrorCode
				health.ConsecutiveFailures = profile.ConsecutiveFailures
				health.NextPollEstimateAt = profile.NextPollEstimateAt
				health.IsStale = profile.IsStale
			}
		}
		if len(health.Profiles) == 0 {
			health.Status = HealthUnknown
		}
		return applyQueueHealth(health), nil
	}
	success, ok, err := s.timeSetting(ctx, SettingPollSuccessAt)
	if err != nil {
		return health, err
	}
	if ok {
		health.LastSuccessAt = &success
	}
	attempt, ok, err := s.timeSetting(ctx, SettingPollAttemptAt)
	if err != nil {
		return health, err
	}
	if ok {
		health.LastAttemptAt = &attempt
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
	if health.LastAttemptAt != nil &&
		(health.LastSuccessAt == nil || health.LastAttemptAt.After(*health.LastSuccessAt)) &&
		(health.LastFailureAt == nil || health.LastAttemptAt.After(*health.LastFailureAt)) &&
		now.Sub(*health.LastAttemptAt) > DefaultRefreshTimeout {
		health.Status = HealthDegraded
		health.FailureKind = "interrupted"
		health.LastError = "previous poll was interrupted before completion"
		return applyQueueHealth(health), nil
	}
	if health.LastFailureAt != nil && (health.LastSuccessAt == nil || health.LastFailureAt.After(*health.LastSuccessAt)) && count > 0 {
		health.Status = HealthDegraded
		next := health.LastFailureAt.Add(pollBackoff(count))
		health.NextPollEstimateAt = &next
		return applyQueueHealth(health), nil
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
	return applyQueueHealth(health), nil
}

func applyQueueHealth(health Health) Health {
	switch {
	case health.Outbox.DeadLetter > 0 || health.TelegramInbox.Dead > 0:
		health.Status = HealthDegraded
		health.QueueReason = "dead_letters"
	case health.Outbox.ExpiredLeases > 0:
		health.Status = HealthDegraded
		health.QueueReason = "expired_leases"
	}
	return health
}

func (s *Server) pollProfile(ctx context.Context, profile Profile) (PollResult, string, error) {
	result, err := s.fetchProfileLimits(ctx, profile)
	if err != nil {
		return PollResult{}, "fetch", err
	}
	if !result.AuthState.OK {
		return PollResult{}, "auth", errors.New("codex auth unavailable")
	}
	obs := s.observationForProfile(result, profile)
	if len(obs.Windows) == 0 {
		return PollResult{}, "shape", errors.New("codex limits response had no reset windows")
	}
	applied, err := s.store.ApplyCodexPoll(ctx, store.CodexPollInput{
		ProfileRef:          profile.Ref,
		Observation:         obs,
		NotificationTarget:  s.cfg.NotificationTarget,
		NotificationTargets: append([]string(nil), s.cfg.NotificationTargets...),
		ResetOptions: resetwatch.Options{
			JokeChooser: resetwatch.CatalogJokeChooser{Tone: s.cfg.JokeTone},
		},
		CommittedAt: time.Now().UTC(),
	})
	if err != nil {
		return PollResult{}, "apply", err
	}
	baseline := applied.AccountBaseline
	decision := applied.LegacyDecision
	inserted := len(applied.ResetEvents)
	heartbeat := s.consumeStartupHeartbeat()
	if baseline || heartbeat {
		if err := s.notifier.NotifyBaseline(ctx, BaselineNotice{Profile: ProfileIdentity{Ref: profile.Ref, Label: profile.Label}, Account: obs.Account, ObservedAt: obs.ObservedAt, Windows: obs.Windows, SnapshotJSON: obs.SnapshotJSON}); err != nil {
			s.logger.Warn("scriba baseline notification failed", "error", err)
		}
	}
	warnings := applied.WarningEvents
	pacingWarnings := applied.PacingWarnings
	grantWarnings := applied.GrantExpiryWarningEvents
	resetGrants := applied.ResetGrantEvents
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
	for _, warning := range pacingWarnings {
		if err := s.notifier.NotifyPacingWarning(ctx, warning); err != nil {
			s.logger.Warn("scriba pacing warning notification failed", "warning_id", warning.ID, "error", err)
		}
	}
	for _, warning := range grantWarnings {
		if err := s.notifier.NotifyGrantExpiryWarning(ctx, warning); err != nil {
			s.logger.Warn("scriba reset grant warning notification failed", "warning_id", warning.ID, "error", err)
		}
	}
	for _, event := range resetGrants {
		if err := s.notifier.NotifyResetGrant(ctx, event); err != nil {
			s.logger.Warn("scriba reset grant loaded notification failed", "event_id", event.ID, "error", err)
		}
	}
	return PollResult{Profile: ProfileIdentity{Ref: profile.Ref, Label: profile.Label}, Observation: obs, Decision: decision, Inserted: inserted, Warnings: warnings, PacingWarnings: pacingWarnings, GrantWarnings: grantWarnings, ResetGrants: resetGrants, Baseline: baseline}, "", nil
}

func (s *Server) pollOnce(ctx context.Context) (PollResult, error) {
	result, _, err := s.pollProfile(ctx, s.defaultProfile())
	return result, err
}

func (s *Server) pollRadar(ctx context.Context) ([]radar.ProbabilityAlert, error) {
	if s.radar == nil {
		return nil, nil
	}
	current, err := s.radar.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	milestone := 0
	if current.Prediction != nil {
		milestone = radar.ProbabilityMilestone(current.Prediction.Probability24H)
	}
	previous := 0
	if raw, ok, err := s.store.GetSetting(ctx, SettingRadarMilestone); err != nil {
		return nil, err
	} else if ok && strings.TrimSpace(raw) != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr == nil {
			previous = parsed
		}
	}
	if milestone <= previous {
		if milestone != previous {
			if err := s.store.SetSetting(ctx, SettingRadarMilestone, strconv.Itoa(milestone)); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	alert, err := radar.NewProbabilityAlert(current, milestone, time.Now())
	if err != nil {
		return nil, err
	}
	targets := append([]string(nil), s.cfg.NotificationTargets...)
	if s.cfg.NotificationTarget != "" {
		targets = append(targets, s.cfg.NotificationTarget)
	}
	inserted, err := s.store.InsertRadarAlertEvent(ctx, alert, targets...)
	if err != nil {
		return nil, err
	}
	if err := s.store.SetSetting(ctx, SettingRadarMilestone, strconv.Itoa(milestone)); err != nil {
		return nil, err
	}
	if !inserted {
		return nil, nil
	}
	return []radar.ProbabilityAlert{alert}, nil
}

func (s *Server) PruneObservations(ctx context.Context, compact bool) (store.PruneResult, error) {
	days := s.cfg.ObservationRetentionDays
	if days <= 0 || days > maxObservationRetentionDays {
		return store.PruneResult{}, errors.New("observation retention must be between 1 and 36500 days")
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
	if result.DeletedObservations > 0 || result.DeletedWindows > 0 || result.DeletedEvents > 0 || result.DeletedDeliveries > 0 || result.DeletedReplayRows > 0 || result.DeletedInboxRows > 0 {
		s.logger.Info("scriba pruned retained history", "observations", result.DeletedObservations, "windows", result.DeletedWindows, "events", result.DeletedEvents, "deliveries", result.DeletedDeliveries, "replay_rows", result.DeletedReplayRows, "inbox_rows", result.DeletedInboxRows, "cutoff", result.Cutoff.Format(time.RFC3339))
	}
	return nil
}

func (s *Server) observation(result remote.ProbeResult) resetwatch.Observation {
	return s.observationForProfile(result, s.defaultProfile())
}

func (s *Server) observationForProfile(result remote.ProbeResult, profile Profile) resetwatch.Observation {
	plan := planFromLines(result.Lines)
	auth := result.AuthState
	account := resetwatch.Account{
		Ref:   accountRef(auth),
		Label: profile.Label,
		Email: auth.Email,
		Plan:  plan,
	}
	snapshotJSON := resetwatch.SnapshotJSON(sanitizeProbeResult(result))
	return resetwatch.Observation{
		ProviderID:   resetwatch.ProviderCodex,
		Account:      account,
		ObservedAt:   time.Now().UTC(),
		Windows:      resetwatch.FromMetricLines(result.Lines),
		ResetGrants:  resetwatch.ResetGrantsFromSnapshotJSON(snapshotJSON),
		SnapshotJSON: snapshotJSON,
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

func (CodexFetcher) FetchProfileLimits(ctx context.Context, profile Profile) (remote.ProbeResult, error) {
	if len(profile.AuthPaths) == 0 && !profile.AllowAuthDiscovery {
		return remote.ProbeResult{}, ErrProfileAuthPaths
	}
	return remotecodex.FetchLimitsWithOptions(ctx, nil, remotecodex.FetchOptions{AuthPaths: profile.AuthPaths})
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

func (NoopNotifier) NotifyPacingWarning(context.Context, budget.PacingAlert) error {
	return nil
}

func (NoopNotifier) NotifyGrantExpiryWarning(context.Context, resetwatch.GrantExpiryWarning) error {
	return nil
}

func (NoopNotifier) NotifyResetGrant(context.Context, resetwatch.ResetGrantEvent) error {
	return nil
}

func (NoopNotifier) NotifyRadarProbability(context.Context, radar.ProbabilityAlert) error {
	return nil
}

func (NoopNotifier) NotifyHealth(context.Context, HealthNotice) error {
	return nil
}

func accountRef(auth remote.AuthState) string {
	return remote.AccountRef(auth)
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

func (s *Server) waitPollInterval(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-s.intervalCh:
		return true
	case <-timer.C:
		return true
	}
}

func (s *Server) notifyIntervalChanged() {
	select {
	case s.intervalCh <- struct{}{}:
	default:
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
	case strings.Contains(lowered, "timeout"), strings.Contains(lowered, "deadline"):
		return "timeout"
	case strings.Contains(lowered, "temporary"), strings.Contains(lowered, "connection"), strings.Contains(lowered, "network"):
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
