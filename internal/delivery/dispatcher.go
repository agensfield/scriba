package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/agensfield/scriba/internal/server/store"
)

const (
	DefaultInterval = 30 * time.Second
	claimLease      = time.Minute
	maxBatch        = 50
)

type OutboxStore interface {
	ClaimOutboxForTarget(context.Context, string, time.Time, time.Duration, int) ([]store.OutboxMessage, error)
	FinishOutboxSuccess(context.Context, string, string, string, time.Time) (bool, error)
	FinishOutboxRetry(context.Context, store.OutboxMessage, string, time.Time, time.Duration) (bool, error)
	FinishOutboxTerminal(context.Context, store.OutboxMessage, string, time.Time) (bool, error)
}

type Adapter interface {
	Target() string
	Deliver(context.Context, Envelope) Outcome
}

type Dispatcher struct {
	Store    OutboxStore
	Adapter  Adapter
	Interval time.Duration
	Now      func() time.Time
	Logger   *slog.Logger
}

func (d Dispatcher) Run(ctx context.Context) error {
	if err := d.validate(); err != nil {
		return err
	}
	interval := d.Interval
	if interval == 0 {
		interval = DefaultInterval
	}
	if interval < 0 {
		return errors.New("delivery interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		processed, err := d.DispatchOnce(ctx)
		if err != nil && ctx.Err() == nil {
			logger := d.Logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.Warn("delivery dispatch failed", "target", d.Adapter.Target(), "error", err)
		}
		if processed == maxBatch && ctx.Err() == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (d Dispatcher) DispatchOnce(ctx context.Context) (int, error) {
	if err := d.validate(); err != nil {
		return 0, err
	}
	processed := 0
	for range maxBatch {
		now := time.Now().UTC()
		if d.Now != nil {
			now = d.Now().UTC()
		}
		claims, err := d.Store.ClaimOutboxForTarget(ctx, d.Adapter.Target(), now, claimLease, 1)
		if err != nil {
			return processed, err
		}
		if len(claims) == 0 {
			return processed, nil
		}
		claim := claims[0]
		envelope, err := FromOutbox(claim)
		if err != nil {
			ok, finishErr := d.Store.FinishOutboxTerminal(ctx, claim, "invalid_notification_envelope", now)
			if finishErr != nil || !ok {
				return processed, fenceError(ok, finishErr)
			}
			processed++
			continue
		}
		outcome := d.Adapter.Deliver(ctx, envelope)
		if ctx.Err() != nil && outcome.Disposition == Retryable && outcome.StatusCode == 0 {
			return processed, ctx.Err()
		}
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		finishedAt := time.Now().UTC()
		if d.Now != nil {
			finishedAt = d.Now().UTC()
		}
		var ok bool
		switch outcome.Disposition {
		case Delivered:
			ok, err = d.Store.FinishOutboxSuccess(finishCtx, claim.ID, claim.LeaseToken, outcome.ProviderID, finishedAt)
		case Retryable:
			ok, err = d.Store.FinishOutboxRetry(finishCtx, claim, outcomeCode(outcome), finishedAt, outcome.RetryAfter)
		case Terminal:
			ok, err = d.Store.FinishOutboxTerminal(finishCtx, claim, outcomeCode(outcome), finishedAt)
		default:
			ok, err = d.Store.FinishOutboxTerminal(finishCtx, claim, "invalid_delivery_outcome", finishedAt)
		}
		cancel()
		if err != nil || !ok {
			return processed, fenceError(ok, err)
		}
		processed++
		if ctx.Err() != nil {
			return processed, ctx.Err()
		}
	}
	return processed, nil
}

func (d Dispatcher) validate() error {
	if d.Store == nil || d.Adapter == nil {
		return errors.New("delivery dispatcher requires store and adapter")
	}
	target := d.Adapter.Target()
	if target == "" || target == "webhook:" || target == "ntfy:" {
		return errors.New("delivery adapter target is required")
	}
	return nil
}

func outcomeCode(outcome Outcome) string {
	if outcome.StatusCode != 0 {
		return fmt.Sprintf("http_%d", outcome.StatusCode)
	}
	if outcome.Disposition == Retryable {
		return "transport_retryable"
	}
	return "delivery_terminal"
}

func fenceError(ok bool, err error) error {
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("delivery outbox fence rejected")
	}
	return nil
}
