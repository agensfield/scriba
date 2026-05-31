package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"
)

type Stats struct {
	Path              string                    `json:"path"`
	SchemaVersion     int                       `json:"schemaVersion"`
	DBFiles           DBFileStats               `json:"dbFiles"`
	Counts            map[string]int64          `json:"counts"`
	ResetDeliveries   map[string]DeliveryCounts `json:"resetDeliveries"`
	WarningDeliveries map[string]DeliveryCounts `json:"warningDeliveries"`
	LatestObservation *ObservationSummary       `json:"latestObservation,omitempty"`
	LastReset         *ResetSummary             `json:"lastReset,omitempty"`
	LastWarning       *WarningSummary           `json:"lastWarning,omitempty"`
}

type DBFileStats struct {
	MainBytes  int64 `json:"mainBytes"`
	WALBytes   int64 `json:"walBytes"`
	SHMBytes   int64 `json:"shmBytes"`
	TotalBytes int64 `json:"totalBytes"`
}

type DeliveryCounts struct {
	Count    int64 `json:"count"`
	Attempts int64 `json:"attempts"`
}

type ObservationSummary struct {
	ObservedAt   time.Time `json:"observedAt"`
	AccountRef   string    `json:"accountRef"`
	AccountLabel string    `json:"accountLabel"`
	AccountEmail string    `json:"accountEmail"`
	AccountPlan  string    `json:"accountPlan"`
	Windows      int64     `json:"windows"`
}

type ResetSummary struct {
	ID           string    `json:"id"`
	AccountLabel string    `json:"accountLabel"`
	Trigger      string    `json:"trigger"`
	Kind         string    `json:"kind"`
	DetectedAt   time.Time `json:"detectedAt"`
}

type WarningSummary struct {
	ID                 string    `json:"id"`
	AccountLabel       string    `json:"accountLabel"`
	Label              string    `json:"label"`
	ThresholdRemaining int       `json:"thresholdRemaining"`
	RemainingPercent   float64   `json:"remainingPercent"`
	DetectedAt         time.Time `json:"detectedAt"`
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	stats := Stats{
		Path:              s.path,
		Counts:            map[string]int64{},
		ResetDeliveries:   map[string]DeliveryCounts{},
		WarningDeliveries: map[string]DeliveryCounts{},
		DBFiles:           dbFileStats(s.path),
	}
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return stats, err
	}
	stats.SchemaVersion = version
	for _, table := range []string{
		"accounts",
		"limit_observations",
		"observed_windows",
		"limit_windows",
		"reset_events",
		"notification_deliveries",
		"limit_warning_events",
		"limit_warning_deliveries",
		"server_settings",
		"telegram_offsets",
	} {
		count, err := s.countTable(ctx, table)
		if err != nil {
			return stats, err
		}
		stats.Counts[table] = count
	}
	resetDeliveries, err := s.deliveryCounts(ctx, "notification_deliveries")
	if err != nil {
		return stats, err
	}
	stats.ResetDeliveries = resetDeliveries
	warningDeliveries, err := s.deliveryCounts(ctx, "limit_warning_deliveries")
	if err != nil {
		return stats, err
	}
	stats.WarningDeliveries = warningDeliveries
	latest, ok, err := s.latestObservationSummary(ctx)
	if err != nil {
		return stats, err
	}
	if ok {
		stats.LatestObservation = &latest
	}
	lastReset, ok, err := s.lastResetSummary(ctx)
	if err != nil {
		return stats, err
	}
	if ok {
		stats.LastReset = &lastReset
	}
	lastWarning, ok, err := s.lastWarningSummary(ctx)
	if err != nil {
		return stats, err
	}
	if ok {
		stats.LastWarning = &lastWarning
	}
	return stats, nil
}

func (s *Store) countTable(ctx context.Context, table string) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `select count(*) from `+table).Scan(&count)
	return count, err
}

func (s *Store) deliveryCounts(ctx context.Context, table string) (map[string]DeliveryCounts, error) {
	query, err := deliveryCountsQuery(table)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	counts := map[string]DeliveryCounts{}
	for rows.Next() {
		var status string
		var count DeliveryCounts
		if err := rows.Scan(&status, &count.Count, &count.Attempts); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func deliveryCountsQuery(table string) (string, error) {
	switch table {
	case "notification_deliveries":
		return `select status, count(*), coalesce(sum(attempts), 0) from notification_deliveries group by status`, nil
	case "limit_warning_deliveries":
		return `select status, count(*), coalesce(sum(attempts), 0) from limit_warning_deliveries group by status`, nil
	default:
		return "", errors.New("unknown delivery table")
	}
}

func (s *Store) latestObservationSummary(ctx context.Context) (ObservationSummary, bool, error) {
	var summary ObservationSummary
	var observedAt string
	err := s.db.QueryRowContext(ctx, `
select o.observed_at, o.account_ref, a.label, a.email, a.plan, count(w.label)
from limit_observations o
join accounts a on a.account_ref = o.account_ref
left join observed_windows w on w.observation_id = o.id
group by o.id
order by o.observed_at desc, o.created_at desc
limit 1`).Scan(&observedAt, &summary.AccountRef, &summary.AccountLabel, &summary.AccountEmail, &summary.AccountPlan, &summary.Windows)
	if errors.Is(err, sql.ErrNoRows) {
		return summary, false, nil
	}
	if err != nil {
		return summary, false, err
	}
	summary.ObservedAt = parseDBTime(observedAt)
	return summary, true, nil
}

func (s *Store) lastResetSummary(ctx context.Context) (ResetSummary, bool, error) {
	var summary ResetSummary
	var detectedAt string
	err := s.db.QueryRowContext(ctx, `
select id, account_label, primary_trigger_label, reset_kind, detected_at
from reset_events
order by detected_at desc
limit 1`).Scan(&summary.ID, &summary.AccountLabel, &summary.Trigger, &summary.Kind, &detectedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return summary, false, nil
	}
	if err != nil {
		return summary, false, err
	}
	summary.DetectedAt = parseDBTime(detectedAt)
	return summary, true, nil
}

func (s *Store) lastWarningSummary(ctx context.Context) (WarningSummary, bool, error) {
	var summary WarningSummary
	var detectedAt string
	err := s.db.QueryRowContext(ctx, `
select id, account_label, label, threshold_remaining, remaining_percent, detected_at
from limit_warning_events
order by detected_at desc
limit 1`).Scan(&summary.ID, &summary.AccountLabel, &summary.Label, &summary.ThresholdRemaining, &summary.RemainingPercent, &detectedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return summary, false, nil
	}
	if err != nil {
		return summary, false, err
	}
	summary.DetectedAt = parseDBTime(detectedAt)
	return summary, true, nil
}

func dbFileStats(path string) DBFileStats {
	stats := DBFileStats{
		MainBytes: fileSize(path),
		WALBytes:  fileSize(path + "-wal"),
		SHMBytes:  fileSize(path + "-shm"),
	}
	stats.TotalBytes = stats.MainBytes + stats.WALBytes + stats.SHMBytes
	return stats
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
