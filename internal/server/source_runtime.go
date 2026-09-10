package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/agensfield/scriba/internal/accounts"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
	remotecodex "github.com/agensfield/scriba/internal/remote/codex"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
)

func sourceHealthFromStore(row store.SourceHealth, account AccountIdentity, interval, staleAfter time.Duration, now time.Time) SourceHealth {
	health := SourceHealth{
		Source:              SourceIdentity{Ref: row.SourceRef},
		Status:              HealthUnknown,
		LastSuccessAt:       row.LastSuccessAt,
		LastAttemptAt:       row.LastAttemptAt,
		LastFailureAt:       row.LastFailureAt,
		FailureKind:         row.FailureKind,
		LastErrorCode:       row.LastErrorCode,
		ConsecutiveFailures: row.ConsecutiveFailures,
	}
	if account.ID != "" {
		health.Account = &account
	}
	if row.LastAttemptAt != nil && (row.LastSuccessAt == nil || row.LastAttemptAt.After(*row.LastSuccessAt)) && (row.LastFailureAt == nil || row.LastAttemptAt.After(*row.LastFailureAt)) && now.Sub(*row.LastAttemptAt) > DefaultRefreshTimeout {
		health.Status = HealthDegraded
		health.FailureKind = "interrupted"
		return health
	}
	if row.LastFailureAt != nil && (row.LastSuccessAt == nil || row.LastFailureAt.After(*row.LastSuccessAt)) && row.ConsecutiveFailures > 0 {
		health.Status = HealthDegraded
		next := row.LastFailureAt.Add(pollBackoff(row.ConsecutiveFailures))
		health.NextPollEstimateAt = &next
		return health
	}
	if row.LastSuccessAt != nil {
		next := row.LastSuccessAt.Add(interval)
		health.NextPollEstimateAt = &next
		health.IsStale = now.Sub(*row.LastSuccessAt) > staleAfter
		if health.IsStale {
			health.Status = HealthStale
		} else {
			health.Status = HealthOK
		}
	}
	return health
}

func worseHealth(current, candidate HealthStatus) HealthStatus {
	rank := map[HealthStatus]int{HealthOK: 0, HealthUnknown: 1, HealthStale: 2, HealthDegraded: 3}
	if rank[candidate] > rank[current] {
		return candidate
	}
	return current
}

func sanitizeProbeResult(result remote.ProbeResult) remote.ProbeResult {
	clean := result
	clean.AuthState.Source = ""
	clean.AuthState.Error = ""
	clean.AuthState.AccessToken = ""
	clean.AuthState.AccountID = ""
	clean.Provenance = append([]model.SourceProvenance(nil), result.Provenance...)
	for i := range clean.Provenance {
		clean.Provenance[i].Error = ""
	}
	clean.Lines = append([]model.MetricLine(nil), result.Lines...)
	for i := range clean.Lines {
		clean.Lines[i].Provenance = append([]model.SourceProvenance(nil), result.Lines[i].Provenance...)
		for j := range clean.Lines[i].Provenance {
			clean.Lines[i].Provenance[j].Error = ""
		}
	}
	return clean
}

func sourceFailure(err error, stage string) (string, string) {
	probeClass := remotecodex.ClassifyProbeError(err)
	var bindingErr *remotecodex.AccountBindingError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return store.SourceFailureNetwork, store.SourceErrorTimeout
	case errors.Is(err, store.ErrSourceDisabled):
		return store.SourceFailureInternal, store.SourceErrorSourceDisabled
	case errors.As(err, &bindingErr):
		return store.SourceFailureAuth, store.SourceErrorAuthRejected
	case probeClass == remotecodex.ProbeErrorAuth:
		return store.SourceFailureAuth, store.SourceErrorAuthRejected
	case probeClass == remotecodex.ProbeErrorRateLimited:
		return store.SourceFailureProvider, store.SourceErrorRateLimited
	case probeClass == remotecodex.ProbeErrorUnavailable:
		return store.SourceFailureProvider, store.SourceErrorUnavailable
	case stage == "auth":
		return store.SourceFailureAuth, store.SourceErrorAuthUnavailable
	case stage == "shape":
		return store.SourceFailureProvider, store.SourceErrorNoResetWindows
	case stage == "apply":
		return store.SourceFailureInternal, store.SourceErrorPersistence
	default:
		return store.SourceFailureNetwork, store.SourceErrorRequestFailed
	}
}

func isSourceScopedStoreError(err error) bool {
	return errors.Is(err, store.ErrInvalidSource) || errors.Is(err, store.ErrSourceMissing) || errors.Is(err, store.ErrSourceDisabled)
}

func (s *Server) refreshSources(ctx context.Context) (RefreshResult, error) {
	sources := s.accounts.Sources()
	result := RefreshResult{Sources: make([]SourcePollResult, 0, len(sources))}
	if err := s.accounts.Sync(ctx); err != nil {
		return result, err
	}
	successes := 0
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		attempt := time.Now().UTC()
		bookkeepingCtx, bookkeepingCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err := s.store.RecordSourcePollAttempt(bookkeepingCtx, source.Ref, attempt)
		bookkeepingCancel()
		if err != nil {
			if isSourceScopedStoreError(err) {
				kind, code := sourceFailure(err, "apply")
				result.Sources = append(result.Sources, SourcePollResult{Source: SourceIdentity{Ref: source.Ref}, Failure: &SourcePollFailure{Kind: kind, Code: code}})
				continue
			}
			return result, err
		}

		inspection := accounts.Inspect(source)
		bookkeepingCtx, bookkeepingCancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		observeErr := s.store.ObserveAuthSource(bookkeepingCtx, source.Ref, inspection.Account, attempt)
		bookkeepingCancel()
		var poll PollResult
		var stage string
		var pollErr error
		if observeErr != nil {
			stage, pollErr = "apply", observeErr
		} else if !inspection.CredentialsAvailable {
			stage, pollErr = "auth", errors.New("codex credentials unavailable")
		} else {
			pollCtx, cancel := context.WithTimeout(ctx, s.sourceTimeout)
			poll, stage, pollErr = s.pollSource(pollCtx, source, inspection)
			cancel()
		}
		completed := time.Now().UTC()
		bookkeepingCtx, bookkeepingCancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if pollErr != nil {
			if stage == "fetch" || stage == "auth" {
				postFailure := accounts.Inspect(source)
				account := postFailure.Account
				if !postFailure.CredentialsAvailable {
					account = resetwatch.Account{}
				}
				if reconcileErr := s.store.ObserveAuthSource(bookkeepingCtx, source.Ref, account, completed); reconcileErr != nil {
					_ = s.store.AbortSourcePollAttempt(bookkeepingCtx, source.Ref, attempt)
					bookkeepingCancel()
					return result, fmt.Errorf("reconcile auth source after failed poll: %w", reconcileErr)
				}
			}
			if ctx.Err() != nil {
				_ = s.store.AbortSourcePollAttempt(bookkeepingCtx, source.Ref, attempt)
				bookkeepingCancel()
				return result, ctx.Err()
			}
			kind, code := sourceFailure(pollErr, stage)
			s.logger.Warn("scriba auth source poll failed", "source_ref", source.Ref, "stage", stage, "failure_kind", kind, "error_code", code)
			if recordErr := s.store.RecordSourcePollFailure(bookkeepingCtx, source.Ref, attempt, completed, kind, code); recordErr != nil {
				bookkeepingCancel()
				if isSourceScopedStoreError(recordErr) {
					result.Sources = append(result.Sources, SourcePollResult{Source: SourceIdentity{Ref: source.Ref}, Failure: &SourcePollFailure{Kind: kind, Code: code}})
					continue
				}
				return result, fmt.Errorf("record auth source failure: %w", recordErr)
			}
			bookkeepingCancel()
			entry := SourcePollResult{Source: SourceIdentity{Ref: source.Ref}, Failure: &SourcePollFailure{Kind: kind, Code: code}}
			result.Sources = append(result.Sources, entry)
			s.notifySourceFailure(ctx, source)
			continue
		}
		if recordErr := s.store.RecordSourcePollSuccess(bookkeepingCtx, source.Ref, attempt, completed); recordErr != nil {
			bookkeepingCancel()
			return result, fmt.Errorf("record auth source success: %w", recordErr)
		}
		bookkeepingCancel()
		successes++
		result.Sources = append(result.Sources, SourcePollResult{Source: SourceIdentity{Ref: source.Ref}, Account: poll.Account, PollResult: poll})
		s.notifySourceRecovery(ctx, source)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	alerts, err := s.pollRadar(ctx)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	if err != nil {
		s.logger.Warn("scriba radar poll failed", "error", err)
	}
	result.RadarAlerts = alerts
	for _, alert := range alerts {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		if notifyErr := s.notifier.NotifyRadarProbability(ctx, alert); notifyErr != nil {
			s.logger.Warn("scriba radar probability notification failed", "alert_id", alert.ID, "error", notifyErr)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	if err := s.pruneIfDue(ctx); err != nil {
		s.logger.Warn("scriba observation prune failed", "error", err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	if successes == 0 {
		return result, ErrAllSourcesFailed
	}
	return result, nil
}

func (s *Server) sourceHealth(ctx context.Context, ref string) (store.SourceHealth, bool) {
	health, err := s.store.ListSourceHealth(ctx)
	if err != nil {
		return store.SourceHealth{}, false
	}
	for _, item := range health {
		if item.SourceRef == ref {
			return item, true
		}
	}
	return store.SourceHealth{}, false
}

func (s *Server) notifySourceFailure(ctx context.Context, source accounts.Source) {
	h, ok := s.sourceHealth(ctx, source.Ref)
	if !ok || h.ConsecutiveFailures < FailureAlertThreshold || h.AlertState == "failing" {
		return
	}
	health, err := s.Health(ctx)
	if err != nil {
		return
	}
	identity := SourceIdentity{Ref: source.Ref}
	if err = s.notifier.NotifyHealth(ctx, HealthNotice{Source: identity, Health: health}); err != nil {
		s.logger.Warn("scriba auth source health notification failed", "source_ref", source.Ref, "error", err)
		return
	}
	_, _ = s.store.CompareAndSwapSourceAlertState(ctx, source.Ref, "ok", "failing")
}

func (s *Server) notifySourceRecovery(ctx context.Context, source accounts.Source) {
	h, ok := s.sourceHealth(ctx, source.Ref)
	if !ok || h.AlertState != "failing" {
		return
	}
	health, err := s.Health(ctx)
	if err != nil {
		return
	}
	identity := SourceIdentity{Ref: source.Ref}
	if err = s.notifier.NotifyHealth(ctx, HealthNotice{Source: identity, Health: health, Recovery: true}); err != nil {
		s.logger.Warn("scriba auth source recovery notification failed", "source_ref", source.Ref, "error", err)
		return
	}
	_, _ = s.store.CompareAndSwapSourceAlertState(ctx, source.Ref, "failing", "ok")
}
