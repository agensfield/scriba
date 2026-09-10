package server

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agensfield/scriba/internal/accounts"
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
	SettingLastPruneAt          = "last_prune_at"
	SettingRadarMilestone       = "radar_probability_milestone"
	FailureAlertThreshold       = 3
)

var (
	ErrRefreshInProgress = errors.New("refresh already in progress")
	ErrAllSourcesFailed  = errors.New("all auth sources failed")
)

type Store interface {
	ApplyCodexPoll(context.Context, store.CodexPollInput) (store.CodexPollResult, error)
	GetSetting(context.Context, string) (string, bool, error)
	SetSetting(context.Context, string, string) error
	LoadLastResetEvent(context.Context) (resetwatch.Event, bool, error)
	LoadLatestObservation(context.Context) (resetwatch.Observation, bool, error)
	LoadLatestObservationForAccount(context.Context, string) (resetwatch.Observation, bool, error)
	PruneObservations(context.Context, time.Time, bool) (store.PruneResult, error)
	InsertRadarAlertEvent(context.Context, radar.ProbabilityAlert, ...string) (bool, error)
	Stats(context.Context) (store.Stats, error)
	SyncAuthSources(context.Context, []store.SourceSpec) error
	ObserveAuthSource(context.Context, string, resetwatch.Account, time.Time) error
	ListAccounts(context.Context) ([]store.Account, error)
	ResolveAccount(context.Context, string) (store.Account, bool, error)
	SetAccountAlias(context.Context, string, string) error
	RegisterAuthSourceAccountAlias(context.Context, store.SourceSpec, resetwatch.Account, string, time.Time) error
	ListSourceHealth(context.Context) ([]store.SourceHealth, error)
	RecordSourcePollAttempt(context.Context, string, time.Time) error
	RecordSourcePollSuccess(context.Context, string, time.Time, time.Time) error
	RecordSourcePollFailure(context.Context, string, time.Time, time.Time, string, string) error
	AbortSourcePollAttempt(context.Context, string, time.Time) error
	CompareAndSwapSourceAlertState(context.Context, string, string, string) (bool, error)
}

type Fetcher interface {
	FetchLimits(context.Context, accounts.Source, string) (remote.ProbeResult, error)
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
	Sources                  []accounts.Source
	NotificationTarget       string
	NotificationTargets      []string
	JokeTone                 string
	StartupHeartbeat         bool
	ObservationRetentionDays int
}

type AccountIdentity struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type SourceIdentity struct {
	Ref string `json:"ref"`
}

type Server struct {
	store         Store
	fetcher       Fetcher
	radar         RadarFetcher
	notifier      Notifier
	cfg           Config
	accounts      *accounts.Resolver
	fetchActivity func(context.Context, remotecodex.FetchOptions) (remotecodex.ProfileResult, error)
	logger        *slog.Logger

	mu            sync.Mutex
	refreshing    bool
	heartbeat     bool
	intervalCh    chan struct{}
	sourceTimeout time.Duration
}

type BaselineNotice struct {
	Account      AccountIdentity
	ObservedAt   time.Time
	Windows      []resetwatch.Window
	SnapshotJSON []byte
}

type PollResult struct {
	Account        AccountIdentity
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

type CodexResetPlan struct {
	Account store.Account                  `json:"account"`
	Plan    remotecodex.RateLimitResetPlan `json:"plan"`
}

type CodexActivityResult struct {
	Account  store.Account             `json:"account"`
	Activity remotecodex.ProfileResult `json:"activity"`
}

type SourcePollFailure struct {
	Kind, Code string
}

type SourcePollResult struct {
	Source  SourceIdentity
	Account AccountIdentity
	PollResult
	Failure *SourcePollFailure
}

type RefreshResult struct {
	Sources     []SourcePollResult
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
	Sources                  []SourceHealth   `json:"sources,omitempty"`
	Accounts                 []AccountHealth  `json:"accounts,omitempty"`
}

type SourceHealth struct {
	Source              SourceIdentity   `json:"source"`
	Account             *AccountIdentity `json:"account,omitempty"`
	Status              HealthStatus     `json:"status"`
	LastSuccessAt       *time.Time       `json:"lastSuccessAt,omitempty"`
	LastAttemptAt       *time.Time       `json:"lastAttemptAt,omitempty"`
	LastFailureAt       *time.Time       `json:"lastFailureAt,omitempty"`
	FailureKind         string           `json:"failureKind,omitempty"`
	LastErrorCode       string           `json:"lastErrorCode,omitempty"`
	ConsecutiveFailures int              `json:"consecutiveFailures"`
	NextPollEstimateAt  *time.Time       `json:"nextPollEstimateAt,omitempty"`
	IsStale             bool             `json:"isStale"`
}

type AccountHealth struct {
	Account store.Account `json:"account"`
	Status  HealthStatus  `json:"status"`
	IsStale bool          `json:"isStale"`
}

type HealthNotice struct {
	Source   SourceIdentity
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
	if cfg.ObservationRetentionDays == 0 {
		cfg.ObservationRetentionDays = 120
	}
	accountResolver := accounts.New(st, cfg.Sources)
	cfg.Sources = accountResolver.Sources()
	return &Server{
		store:    st,
		fetcher:  fetcher,
		notifier: notifier,
		cfg:      cfg,
		accounts: accountResolver,
		fetchActivity: func(ctx context.Context, options remotecodex.FetchOptions) (remotecodex.ProfileResult, error) {
			return remotecodex.FetchProfileWithOptions(ctx, nil, options)
		},
		logger:        slog.Default(),
		heartbeat:     cfg.StartupHeartbeat,
		intervalCh:    make(chan struct{}, 1),
		sourceTimeout: DefaultRefreshTimeout,
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
	result, err := s.RefreshSourcesNow(ctx)
	for _, source := range result.Sources {
		if source.Failure == nil {
			source.RadarAlerts = result.RadarAlerts
			return source.PollResult, err
		}
	}
	return PollResult{RadarAlerts: result.RadarAlerts}, err
}

func (s *Server) RefreshSourcesNow(ctx context.Context) (RefreshResult, error) {
	if !s.beginRefresh() {
		return RefreshResult{}, ErrRefreshInProgress
	}
	defer s.endRefresh()
	return s.refreshSources(ctx)
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

func (s *Server) Accounts(ctx context.Context) ([]store.Account, error) {
	return s.accounts.Accounts(ctx)
}

func (s *Server) SetAccountAlias(ctx context.Context, selector, alias string) error {
	return s.accounts.SetAlias(ctx, selector, alias)
}

func (s *Server) LatestObservationForAccount(ctx context.Context, selector string) (resetwatch.Observation, bool, error) {
	account, err := s.accounts.Resolve(ctx, selector)
	if err != nil {
		return resetwatch.Observation{}, false, err
	}
	return s.store.LoadLatestObservationForAccount(ctx, account.ID)
}

func (s *Server) CodexActivityForAccount(ctx context.Context, selector string) (CodexActivityResult, error) {
	live, err := s.accounts.ResolveLive(ctx, selector)
	if err != nil {
		return CodexActivityResult{}, err
	}
	result, err := s.fetchActivity(ctx, live.FetchOptions())
	if err != nil {
		return CodexActivityResult{}, err
	}
	result.SchemaVersion = model.SchemaVersion
	return CodexActivityResult{Account: live.Account, Activity: sanitizeCodexProfileResult(result)}, nil
}

func (s *Server) PlanCodexReset(ctx context.Context, selector string) (CodexResetPlan, error) {
	live, err := s.accounts.ResolveLive(ctx, selector)
	if err != nil {
		return CodexResetPlan{}, err
	}
	plan, err := remotecodex.PlanRateLimitReset(ctx, nil, live.FetchOptions(), "")
	if err != nil {
		return CodexResetPlan{}, err
	}
	plan.AuthState = sanitizeCodexAuthState(plan.AuthState)
	return CodexResetPlan{Account: live.Account, Plan: plan}, nil
}

func (s *Server) ConsumeCodexReset(ctx context.Context, selector string, accountPin remotecodex.ResetAccountPin, credit remote.ResetCredit, requestID string) (remotecodex.RateLimitResetResult, error) {
	live, err := s.accounts.ResolveLive(ctx, selector)
	if err != nil {
		if errors.Is(err, accounts.ErrCredentialsUnavailable) {
			return remotecodex.RateLimitResetResult{}, &remotecodex.ResetAccountBindingError{}
		}
		return remotecodex.RateLimitResetResult{}, err
	}
	result, err := remotecodex.ConsumeRateLimitResetCredit(ctx, nil, remotecodex.FetchOptions{AuthPaths: []string{live.Source.Path}}, accountPin, credit, requestID)
	if err != nil {
		return remotecodex.RateLimitResetResult{}, err
	}
	result.AuthState = sanitizeCodexAuthState(result.AuthState)
	return result, nil
}

func sanitizeCodexAuthState(auth remote.AuthState) remote.AuthState {
	auth.Source = ""
	auth.Error = ""
	auth.AccessToken = ""
	auth.AccountID = ""
	return auth
}

func sanitizeCodexProfileResult(result remotecodex.ProfileResult) remotecodex.ProfileResult {
	result.AuthState = sanitizeCodexAuthState(result.AuthState)
	if result.Metadata.StatsError != nil {
		result.Metadata.StatsError = "profile stats unavailable"
	}
	for i := range result.Provenance {
		result.Provenance[i].Error = ""
	}
	return result
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

	accountRows, err := s.accounts.Accounts(ctx)
	if err != nil {
		return health, err
	}
	now := time.Now().UTC()
	accountsByRef := make(map[string]AccountIdentity, len(accountRows))
	for _, account := range accountRows {
		identity := accountIdentity(account)
		accountsByRef[account.Ref] = identity
		accountHealth := AccountHealth{Account: account, Status: HealthUnknown}
		if !account.LastSeenAt.IsZero() {
			accountHealth.IsStale = now.Sub(account.LastSeenAt) > health.StaleAfter
			if accountHealth.IsStale {
				accountHealth.Status = HealthStale
			} else {
				accountHealth.Status = HealthOK
			}
		}
		health.Accounts = append(health.Accounts, accountHealth)
	}

	sourceRows, err := s.store.ListSourceHealth(ctx)
	if err != nil {
		return health, err
	}
	rowsByRef := make(map[string]store.SourceHealth, len(sourceRows))
	for _, row := range sourceRows {
		rowsByRef[row.SourceRef] = row
	}
	if len(s.cfg.Sources) > 0 {
		health.Status = HealthOK
		for _, configured := range s.cfg.Sources {
			row, exists := rowsByRef[configured.Ref]
			if !exists || !row.Enabled {
				source := SourceHealth{Source: SourceIdentity{Ref: configured.Ref}, Status: HealthUnknown}
				health.Sources = append(health.Sources, source)
				health.Status = worseHealth(health.Status, source.Status)
				continue
			}
			source := sourceHealthFromStore(row, accountsByRef[row.AccountRef], interval, health.StaleAfter, now)
			health.Sources = append(health.Sources, source)
			health.Status = worseHealth(health.Status, source.Status)
		}
		if len(health.Sources) > 0 {
			primary := health.Sources[0]
			health.LastSuccessAt = primary.LastSuccessAt
			health.LastAttemptAt = primary.LastAttemptAt
			health.LastFailureAt = primary.LastFailureAt
			health.FailureKind = primary.FailureKind
			health.LastError = primary.LastErrorCode
			health.ConsecutiveFailures = primary.ConsecutiveFailures
			health.NextPollEstimateAt = primary.NextPollEstimateAt
			health.IsStale = primary.IsStale
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

func (s *Server) pollSource(ctx context.Context, source accounts.Source, inspection accounts.Inspection) (PollResult, string, error) {
	result, err := s.fetcher.FetchLimits(ctx, source, inspection.Account.Ref)
	if err != nil {
		return PollResult{}, "fetch", err
	}
	if !result.AuthState.OK {
		return PollResult{}, "auth", errors.New("codex auth unavailable")
	}
	if result.AuthState.AccountID == "" {
		return PollResult{}, "auth", &remotecodex.AccountBindingError{}
	}
	if result.AuthState.AccountID != inspection.Account.Ref {
		return PollResult{}, "auth", &remotecodex.AccountBindingError{Changed: true}
	}
	obs := s.observationForAccount(result)
	if len(obs.Windows) == 0 {
		return PollResult{}, "shape", errors.New("codex limits response had no reset windows")
	}
	applied, err := s.store.ApplyCodexPoll(ctx, store.CodexPollInput{
		SourceRef:           source.Ref,
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
	account, ok, err := s.store.ResolveAccount(ctx, store.AccountID(obs.ProviderID, obs.Account.Ref))
	if err != nil {
		return PollResult{}, "apply", err
	}
	if !ok {
		return PollResult{}, "apply", accounts.ErrAccountNotFound
	}
	identity := accountIdentity(account)
	baseline := applied.AccountBaseline
	decision := applied.LegacyDecision
	inserted := len(applied.ResetEvents)
	heartbeat := s.consumeStartupHeartbeat()
	if baseline || heartbeat {
		if err := s.notifier.NotifyBaseline(ctx, BaselineNotice{Account: identity, ObservedAt: obs.ObservedAt, Windows: obs.Windows, SnapshotJSON: obs.SnapshotJSON}); err != nil {
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
	return PollResult{Account: identity, Observation: obs, Decision: decision, Inserted: inserted, Warnings: warnings, PacingWarnings: pacingWarnings, GrantWarnings: grantWarnings, ResetGrants: resetGrants, Baseline: baseline}, "", nil
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
	return s.observationForAccount(result)
}

func (s *Server) observationForAccount(result remote.ProbeResult) resetwatch.Observation {
	plan := planFromLines(result.Lines)
	auth := result.AuthState
	account := resetwatch.Account{
		Ref:   strings.TrimSpace(auth.AccountID),
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

func (CodexFetcher) FetchLimits(ctx context.Context, source accounts.Source, expectedAccountID string) (remote.ProbeResult, error) {
	return remotecodex.FetchLimitsWithOptions(ctx, nil, remotecodex.FetchOptions{AuthPaths: []string{source.Path}, ExpectedAccountID: expectedAccountID})
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

func accountIdentity(account store.Account) AccountIdentity {
	return AccountIdentity{ID: account.ID, DisplayName: account.DisplayName()}
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

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

var _ Store = (*store.Store)(nil)
