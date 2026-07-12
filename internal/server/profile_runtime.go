package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
	remotecodex "github.com/agensfield/scriba/internal/remote/codex"
	"github.com/agensfield/scriba/internal/server/store"
)

func profileHealthFromStore(row store.ProfileHealth, interval, staleAfter time.Duration, now time.Time) ProfileHealth {
	health := ProfileHealth{
		Profile:             ProfileIdentity{Ref: row.ProfileRef, Label: row.Label},
		IsDefault:           row.IsDefault,
		Status:              HealthUnknown,
		LastSuccessAt:       row.LastSuccessAt,
		LastAttemptAt:       row.LastAttemptAt,
		LastFailureAt:       row.LastFailureAt,
		FailureKind:         row.FailureKind,
		LastErrorCode:       row.LastErrorCode,
		ConsecutiveFailures: row.ConsecutiveFailures,
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

func (s *Server) enabledProfiles() []Profile {
	profiles := make([]Profile, 0, len(s.cfg.Profiles))
	profiles = append(profiles, s.cfg.Profiles...)
	return profiles
}

func (s *Server) fetchProfileLimits(ctx context.Context, profile Profile) (remote.ProbeResult, error) {
	if fetcher, ok := s.fetcher.(ProfileFetcher); ok {
		return fetcher.FetchProfileLimits(ctx, profile)
	}
	if len(s.cfg.Profiles) != 1 || !profile.AllowAuthDiscovery {
		return remote.ProbeResult{}, ErrProfileAuthPaths
	}
	return s.fetcher.FetchLimits(ctx)
}

func profileFailure(err error, stage string) (string, string) {
	probeClass := remotecodex.ClassifyProbeError(err)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return store.ProfileFailureNetwork, store.ProfileErrorTimeout
	case errors.Is(err, store.ErrProfileAccountOwned):
		return store.ProfileFailureInternal, store.ProfileErrorAccountOwned
	case errors.Is(err, store.ErrProfileDisabled):
		return store.ProfileFailureInternal, store.ProfileErrorProfileDisabled
	case probeClass == remotecodex.ProbeErrorAuth:
		return store.ProfileFailureAuth, store.ProfileErrorAuthRejected
	case probeClass == remotecodex.ProbeErrorRateLimited:
		return store.ProfileFailureProvider, store.ProfileErrorRateLimited
	case probeClass == remotecodex.ProbeErrorUnavailable:
		return store.ProfileFailureProvider, store.ProfileErrorUnavailable
	case stage == "auth":
		return store.ProfileFailureAuth, store.ProfileErrorAuthUnavailable
	case stage == "shape":
		return store.ProfileFailureProvider, store.ProfileErrorNoResetWindows
	case stage == "apply":
		return store.ProfileFailureInternal, store.ProfileErrorPersistence
	default:
		return store.ProfileFailureNetwork, store.ProfileErrorRequestFailed
	}
}

func isProfileScopedStoreError(err error) bool {
	return errors.Is(err, store.ErrInvalidProfile) || errors.Is(err, store.ErrProfileMissing) || errors.Is(err, store.ErrProfileDisabled) || errors.Is(err, store.ErrProfileProviderMismatch)
}

func (s *Server) refreshProfiles(ctx context.Context) (RefreshResult, error) {
	profiles := s.enabledProfiles()
	result := RefreshResult{Profiles: make([]ProfilePollResult, 0, len(profiles))}
	successes := 0
	for _, profile := range profiles {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		attempt := time.Now().UTC()
		bookkeepingCtx, bookkeepingCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err := s.store.RecordProfilePollAttempt(bookkeepingCtx, profile.Ref, attempt)
		bookkeepingCancel()
		if err != nil {
			if isProfileScopedStoreError(err) {
				kind, code := profileFailure(err, "apply")
				result.Profiles = append(result.Profiles, ProfilePollResult{Profile: ProfileIdentity{Ref: profile.Ref, Label: profile.Label}, Failure: &ProfilePollFailure{Kind: kind, Code: code}})
				continue
			}
			return result, err
		}
		pollCtx, cancel := context.WithTimeout(ctx, s.profileTimeout)
		poll, stage, pollErr := s.pollProfile(pollCtx, profile)
		cancel()
		completed := time.Now().UTC()
		bookkeepingCtx, bookkeepingCancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if pollErr != nil {
			if ctx.Err() != nil {
				_ = s.store.AbortProfilePollAttempt(bookkeepingCtx, profile.Ref, attempt)
				bookkeepingCancel()
				return result, ctx.Err()
			}
			kind, code := profileFailure(pollErr, stage)
			if recordErr := s.store.RecordProfilePollFailure(bookkeepingCtx, profile.Ref, attempt, completed, kind, code); recordErr != nil {
				bookkeepingCancel()
				if isProfileScopedStoreError(recordErr) {
					result.Profiles = append(result.Profiles, ProfilePollResult{Profile: ProfileIdentity{Ref: profile.Ref, Label: profile.Label}, Failure: &ProfilePollFailure{Kind: kind, Code: code}})
					continue
				}
				return result, fmt.Errorf("record profile failure: %w", recordErr)
			}
			bookkeepingCancel()
			entry := ProfilePollResult{Profile: ProfileIdentity{Ref: profile.Ref, Label: profile.Label}, Failure: &ProfilePollFailure{Kind: kind, Code: code}}
			result.Profiles = append(result.Profiles, entry)
			s.notifyProfileFailure(ctx, profile)
			continue
		}
		if recordErr := s.store.RecordProfilePollSuccess(bookkeepingCtx, profile.Ref, attempt, completed); recordErr != nil {
			bookkeepingCancel()
			return result, fmt.Errorf("record profile success: %w", recordErr)
		}
		bookkeepingCancel()
		successes++
		result.Profiles = append(result.Profiles, ProfilePollResult{Profile: ProfileIdentity{Ref: profile.Ref, Label: profile.Label}, PollResult: poll})
		s.notifyProfileRecovery(ctx, profile)
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
		return result, ErrAllProfilesFailed
	}
	return result, nil
}

func (s *Server) profileHealth(ctx context.Context, ref string) (store.ProfileHealth, bool) {
	health, err := s.store.ListProfileHealth(ctx)
	if err != nil {
		return store.ProfileHealth{}, false
	}
	for _, item := range health {
		if item.ProfileRef == ref {
			return item, true
		}
	}
	return store.ProfileHealth{}, false
}

func (s *Server) notifyProfileFailure(ctx context.Context, profile Profile) {
	h, ok := s.profileHealth(ctx, profile.Ref)
	if !ok || h.ConsecutiveFailures < FailureAlertThreshold || h.AlertState == "failing" {
		return
	}
	health, err := s.Health(ctx)
	if err != nil {
		return
	}
	if err = s.notifier.NotifyHealth(ctx, HealthNotice{Profile: ProfileIdentity{Ref: profile.Ref, Label: profile.Label}, Health: health}); err != nil {
		s.logger.Warn("scriba profile health notification failed", "profile_ref", profile.Ref, "error", err)
		return
	}
	_, _ = s.store.CompareAndSwapProfileAlertState(ctx, profile.Ref, "ok", "failing")
}

func (s *Server) notifyProfileRecovery(ctx context.Context, profile Profile) {
	h, ok := s.profileHealth(ctx, profile.Ref)
	if !ok || h.AlertState != "failing" {
		return
	}
	health, err := s.Health(ctx)
	if err != nil {
		return
	}
	if err = s.notifier.NotifyHealth(ctx, HealthNotice{Profile: ProfileIdentity{Ref: profile.Ref, Label: profile.Label}, Health: health, Recovery: true}); err != nil {
		s.logger.Warn("scriba profile recovery notification failed", "profile_ref", profile.Ref, "error", err)
		return
	}
	_, _ = s.store.CompareAndSwapProfileAlertState(ctx, profile.Ref, "failing", "ok")
}
