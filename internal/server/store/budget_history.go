package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/agensfield/scriba/internal/budget"
	"github.com/agensfield/scriba/internal/budgetadapter"
	"github.com/agensfield/scriba/internal/resetwatch"
)

// OpenReadOnly opens an existing database without migrations, main-database
// writes, or business-state mutations. SQLite may coordinate a live WAL reader
// through transient sidecar bookkeeping.
func OpenReadOnly(path string) (*Store, error) {
	return OpenReadOnlyContext(context.Background(), path)
}

// OpenReadOnlyContext is OpenReadOnly with cancellation-aware open and schema
// validation for request-scoped agent/API consumers.
func OpenReadOnlyContext(ctx context.Context, path string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errors.New("store path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("store path is not a regular file: %s", path)
	}
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxOpenConnections)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := checkSchemaCompatibility(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, path: path}, nil
}

// LoadBudgetHistory returns chronological observations isolated to one
// provider account. Derived budget values are never persisted.
func (s *Store) LoadBudgetHistory(ctx context.Context, providerID, accountRef string, since time.Time) ([]budget.Observation, error) {
	rows, err := s.db.QueryContext(ctx, `
select o.id, o.observed_at, w.label, w.used_percent, w.reset_at, w.period_duration_ms
from limit_observations o
join observed_windows w on w.observation_id = o.id
where o.provider_id = ? and o.account_ref = ? and julianday(o.observed_at) >= julianday(?)
order by julianday(o.observed_at) asc, o.id asc, w.label asc`, providerID, accountRef, formatTime(since))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []budget.Observation
	var currentID string
	var current resetwatch.Observation
	flush := func() {
		if currentID != "" {
			out = append(out, budgetadapter.FromResetwatch(current))
		}
	}
	for rows.Next() {
		var id, observedAt, label, resetAt string
		var used sql.NullFloat64
		var period sql.NullInt64
		if err := rows.Scan(&id, &observedAt, &label, &used, &resetAt, &period); err != nil {
			return nil, err
		}
		if id != currentID {
			flush()
			currentID = id
			current = resetwatch.Observation{ProviderID: providerID, ObservedAt: parseDBTime(observedAt)}
		}
		window := resetwatch.Window{Label: label, ResetAt: parseDBTime(resetAt)}
		if used.Valid {
			value := used.Float64
			window.UsedPercent = &value
		}
		if period.Valid {
			value := period.Int64
			window.PeriodDurationMs = &value
		}
		current.Windows = append(current.Windows, window)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	flush()
	return out, nil
}
