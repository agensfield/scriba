package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/agensfield/scriba/internal/budget"
	"github.com/agensfield/scriba/internal/budgetadapter"
	"github.com/agensfield/scriba/internal/resetwatch"
)

var pacingWindowKeys = map[budget.WindowKey]bool{
	"primary.five_hour": true,
	"primary.weekly":    true,
}

func derivePacingReport(ctx context.Context, tx *sql.Tx, obs resetwatch.Observation) (budget.Report, error) {
	history, err := loadBudgetHistory(ctx, tx, obs.ProviderID, obs.Account.Ref, obs.ObservedAt.Add(-24*time.Hour))
	if err != nil {
		return budget.Report{}, err
	}
	historyState := budget.HistoryAvailable
	if len(history) == 0 {
		historyState = budget.HistoryEmpty
	}
	return budget.Evaluate(budget.Input{ProviderID: obs.ProviderID, Observation: budgetadapter.FromResetwatch(obs), History: history, HistoryState: historyState}, obs.ObservedAt), nil
}

func persistPacingAlerts(ctx context.Context, tx *sql.Tx, obs resetwatch.Observation, report budget.Report, profile string, targets []string, committedAt time.Time) ([]budget.PacingAlert, error) {
	var inserted []budget.PacingAlert
	for _, window := range report.Windows {
		if !pacingWindowKeys[window.Key] || window.ResetAt == nil {
			continue
		}
		var previousReset, lastRisk, lastObserved string
		var alerted int
		err := tx.QueryRowContext(ctx, `select reset_at,alerted,last_risk,last_observed_at from pacing_alert_states where provider_id=? and account_ref=? and window_key=?`, obs.ProviderID, obs.Account.Ref, string(window.Key)).Scan(&previousReset, &alerted, &lastRisk, &lastObserved)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil && obs.ObservedAt.Before(parseDBTime(lastObserved)) {
			return nil, ErrStaleObservation
		}
		if errors.Is(err, sql.ErrNoRows) || !parseDBTime(previousReset).Equal(*window.ResetAt) {
			alerted = 0
		}
		if alerted == 0 && pacingAlertable(window) {
			alert := newPacingAlert(obs, window)
			added, insertErr := insertPacingAlertTx(ctx, tx, alert, profile, targets, committedAt)
			if insertErr != nil {
				return nil, insertErr
			}
			if added {
				inserted = append(inserted, alert)
			}
			alerted = 1
		}
		now := formatTime(committedAt)
		_, err = tx.ExecContext(ctx, `insert into pacing_alert_states(provider_id,account_ref,window_key,reset_at,alerted,last_risk,last_observed_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?) on conflict(provider_id,account_ref,window_key) do update set reset_at=excluded.reset_at,alerted=excluded.alerted,last_risk=excluded.last_risk,last_observed_at=excluded.last_observed_at,updated_at=excluded.updated_at`, obs.ProviderID, obs.Account.Ref, string(window.Key), formatTime(*window.ResetAt), alerted, window.Risk, formatTime(obs.ObservedAt), now, now)
		if err != nil {
			return nil, err
		}
	}
	return inserted, nil
}

func pacingAlertable(window budget.Window) bool {
	return window.Risk == "high" && window.UsedPercent != nil && window.RemainingPercentPoints != nil && *window.RemainingPercentPoints > 20 && window.PaceBurnPercentPointsPerHour != nil && window.SafeHourlyAllowancePercentPoints != nil && window.ProjectedExhaustionAt != nil && window.ResetAt != nil
}

func newPacingAlert(obs resetwatch.Observation, window budget.Window) budget.PacingAlert {
	sum := sha256.Sum256([]byte(obs.ProviderID + "\x00" + obs.Account.Ref + "\x00" + string(window.Key) + "\x00" + window.ResetAt.UTC().Format(time.RFC3339Nano) + "\x00" + window.Risk))
	return budget.PacingAlert{
		ID:                       "pacing_" + hex.EncodeToString(sum[:16]),
		ProviderID:               obs.ProviderID,
		AccountRef:               obs.Account.Ref,
		AccountLabel:             obs.Account.Label,
		WindowKey:                string(window.Key),
		Label:                    window.Label,
		Risk:                     window.Risk,
		Confidence:               window.Confidence,
		UsedPercent:              *window.UsedPercent,
		RemainingPercentPoints:   *window.RemainingPercentPoints,
		PacePercentPointsPerHour: *window.PaceBurnPercentPointsPerHour,
		SafePercentPointsPerHour: *window.SafeHourlyAllowancePercentPoints,
		ProjectedExhaustionAt:    window.ProjectedExhaustionAt.UTC(),
		ResetAt:                  window.ResetAt.UTC(),
		DetectedAt:               obs.ObservedAt.UTC(),
	}
}

func insertPacingAlertTx(ctx context.Context, tx *sql.Tx, alert budget.PacingAlert, profile string, targets []string, committedAt time.Time) (bool, error) {
	r, err := tx.ExecContext(ctx, `insert into pacing_warning_events(id,provider_id,account_ref,account_label,window_key,label,risk,confidence,used_percent,remaining_percent,pace_per_hour,safe_per_hour,projected_exhaustion_at,reset_at,detected_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(id) do nothing`, alert.ID, alert.ProviderID, alert.AccountRef, alert.AccountLabel, alert.WindowKey, alert.Label, alert.Risk, alert.Confidence, alert.UsedPercent, alert.RemainingPercentPoints, alert.PacePercentPointsPerHour, alert.SafePercentPointsPerHour, formatTime(alert.ProjectedExhaustionAt), formatTime(alert.ResetAt), formatTime(alert.DetectedAt), formatTime(committedAt))
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		var matches int
		err = tx.QueryRowContext(ctx, `select count(*) from pacing_warning_events where id=? and provider_id=? and account_ref=? and account_label=? and window_key=? and label=? and risk=? and confidence=? and used_percent=? and remaining_percent=? and pace_per_hour=? and safe_per_hour=? and projected_exhaustion_at=? and reset_at=? and detected_at=?`, alert.ID, alert.ProviderID, alert.AccountRef, alert.AccountLabel, alert.WindowKey, alert.Label, alert.Risk, alert.Confidence, alert.UsedPercent, alert.RemainingPercentPoints, alert.PacePercentPointsPerHour, alert.SafePercentPointsPerHour, formatTime(alert.ProjectedExhaustionAt), formatTime(alert.ResetAt), formatTime(alert.DetectedAt)).Scan(&matches)
		if err != nil {
			return false, err
		}
		if matches != 1 {
			return false, errors.New("conflicting pacing warning semantic duplicate")
		}
	}
	for _, target := range targets {
		payload, encodeErr := EncodeOutboxPayload("pacing_warning", alert)
		if encodeErr != nil {
			return false, encodeErr
		}
		if enqueueErr := EnqueueOutbox(ctx, tx, OutboxEnqueue{EventKind: "pacing_warning", Source: "budget-v1", ProfileRef: profile, AccountRef: alert.AccountRef, EventID: alert.ID, Target: target, PayloadVersion: 1, PayloadJSON: payload}, committedAt); enqueueErr != nil {
			return false, enqueueErr
		}
	}
	return n == 1, nil
}
