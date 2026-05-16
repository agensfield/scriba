package cache

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/agensfield/scriba/internal/model"
	_ "modernc.org/sqlite"
)

const SchemaVersion = 1

type Cache struct {
	dir string
	db  *sql.DB
}

type Status struct {
	CacheDir      string           `json:"cacheDir"`
	DatabasePath  string           `json:"databasePath"`
	SchemaVersion int              `json:"schemaVersion"`
	SizeBytes     int64            `json:"sizeBytes"`
	Snapshots     []SnapshotInfo   `json:"snapshots"`
	ScanStats     []ScanStatsInfo  `json:"scanStats"`
	FileEvents    []FileEventsInfo `json:"fileEvents"`
	WAL           WALInfo          `json:"wal"`
}

type SnapshotInfo struct {
	Name      string `json:"name"`
	UpdatedAt string `json:"updatedAt"`
}

type ScanStatsInfo struct {
	ProviderID string             `json:"providerId"`
	UpdatedAt  string             `json:"updatedAt"`
	Stats      model.ScannerStats `json:"stats"`
}

type FileEventsInfo struct {
	ProviderID string `json:"providerId"`
	Files      int    `json:"files"`
	UpdatedAt  string `json:"updatedAt"`
}

type WALInfo struct {
	Enabled       bool   `json:"enabled"`
	Mode          string `json:"mode"`
	BusyTimeoutMs int    `json:"busyTimeoutMs"`
}

type VacuumResult struct {
	BeforeBytes    int64 `json:"beforeBytes"`
	AfterBytes     int64 `json:"afterBytes"`
	DeltaBytes     int64 `json:"deltaBytes"`
	ReclaimedBytes int64 `json:"reclaimedBytes"`
	GrewBytes      int64 `json:"grewBytes"`
}

func ResolveDir(configured string) string {
	if configured != "" {
		return configured
	}
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "scriba")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "scriba")
}

func Open(configured string) (*Cache, error) {
	dir := ResolveDir(configured)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "scriba.sqlite"))
	if err != nil {
		return nil, err
	}
	cache := &Cache{dir: dir, db: db}
	if err := cache.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return cache, nil
}

func Reset(configured string) (string, error) {
	dir := ResolveDir(configured)
	return dir, os.RemoveAll(dir)
}

func (c *Cache) Close() error {
	return c.db.Close()
}

func (c *Cache) Dir() string {
	return c.dir
}

func (c *Cache) DatabasePath() string {
	return filepath.Join(c.dir, "scriba.sqlite")
}

func (c *Cache) init() error {
	if _, err := c.db.Exec(`pragma busy_timeout = 5000;`); err != nil {
		return err
	}
	_, _ = c.db.Exec(`pragma journal_mode = wal;`)
	_, err := c.db.Exec(`
create table if not exists snapshots (
  name text primary key,
  json text not null,
  updated_at text not null
);
create table if not exists meta (
  key text primary key,
  value text not null
);
create table if not exists scan_stats (
  provider_id text primary key,
  json text not null,
  updated_at text not null
);
create table if not exists file_events (
  provider_id text not null,
  path text not null,
  size integer not null,
  mtime_ms real not null,
  events_json text not null,
  stats_json text not null,
  updated_at text not null,
  primary key (provider_id, path)
);`)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(`insert or replace into meta (key, value) values ('schema_version', ?)`, SchemaVersion)
	return err
}

func (c *Cache) SaveSnapshot(name string, snapshot any, updatedAt string) error {
	if updatedAt == "" {
		updatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if _, err := c.db.Exec(`insert or replace into snapshots (name, json, updated_at) values (?, ?, ?)`, name, string(data), updatedAt); err != nil {
		return err
	}
	if name == "status" {
		return os.WriteFile(filepath.Join(c.dir, "status.json"), append(pretty(snapshot), '\n'), 0o600)
	}
	return nil
}

func (c *Cache) LoadStatusSnapshot() (*model.StatusSnapshot, error) {
	var text string
	err := c.db.QueryRow(`select json from snapshots where name = ?`, "status").Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot model.StatusSnapshot
	if err := json.Unmarshal([]byte(text), &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (c *Cache) SaveScanStats(providerID string, stats model.ScannerStats, updatedAt string) error {
	data, err := json.Marshal(stats)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(`insert or replace into scan_stats (provider_id, json, updated_at) values (?, ?, ?)`, providerID, string(data), updatedAt)
	return err
}

func (c *Cache) LoadFileEvents(providerID, path string, size int64, mtimeMs float64) ([]model.LocalUsageEvent, model.ScannerStats, bool, error) {
	var rowSize int64
	var mtime float64
	var eventsJSON, statsJSON string
	err := c.db.QueryRow(`select size, mtime_ms, events_json, stats_json from file_events where provider_id = ? and path = ?`, providerID, path).Scan(&rowSize, &mtime, &eventsJSON, &statsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ScannerStats{}, false, nil
	}
	if err != nil {
		return nil, model.ScannerStats{}, false, err
	}
	if rowSize != size || mtime != mtimeMs {
		return nil, model.ScannerStats{}, false, nil
	}
	var events []model.LocalUsageEvent
	var stats model.ScannerStats
	if err := json.Unmarshal([]byte(eventsJSON), &events); err != nil {
		return nil, stats, false, err
	}
	if err := json.Unmarshal([]byte(statsJSON), &stats); err != nil {
		return nil, stats, false, err
	}
	return events, stats, true, nil
}

func (c *Cache) SaveFileEvents(providerID, path string, size int64, mtimeMs float64, events []model.LocalUsageEvent, stats model.ScannerStats) error {
	eventsJSON, err := json.Marshal(events)
	if err != nil {
		return err
	}
	statsJSON, err := json.Marshal(stats)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(`insert or replace into file_events (provider_id, path, size, mtime_ms, events_json, stats_json, updated_at) values (?, ?, ?, ?, ?, ?, ?)`,
		providerID, path, size, mtimeMs, string(eventsJSON), string(statsJSON), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (c *Cache) Status() (Status, error) {
	status := Status{
		CacheDir:     c.dir,
		DatabasePath: c.DatabasePath(),
		SizeBytes:    databaseSize(c.DatabasePath()),
		WAL:          WALInfo{BusyTimeoutMs: 5000},
	}
	_ = c.db.QueryRow(`select value from meta where key = 'schema_version'`).Scan(&status.SchemaVersion)
	_ = c.db.QueryRow(`pragma journal_mode`).Scan(&status.WAL.Mode)
	status.WAL.Enabled = status.WAL.Mode == "wal"
	rows, err := c.db.Query(`select name, updated_at from snapshots order by name`)
	if err != nil {
		return status, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var item SnapshotInfo
		if err := rows.Scan(&item.Name, &item.UpdatedAt); err == nil {
			status.Snapshots = append(status.Snapshots, item)
		}
	}
	statRows, err := c.db.Query(`select provider_id, json, updated_at from scan_stats order by provider_id`)
	if err != nil {
		return status, err
	}
	defer func() { _ = statRows.Close() }()
	for statRows.Next() {
		var providerID, text, updatedAt string
		if err := statRows.Scan(&providerID, &text, &updatedAt); err == nil {
			var stats model.ScannerStats
			_ = json.Unmarshal([]byte(text), &stats)
			status.ScanStats = append(status.ScanStats, ScanStatsInfo{ProviderID: providerID, UpdatedAt: updatedAt, Stats: stats})
		}
	}
	eventRows, err := c.db.Query(`select provider_id, count(*), max(updated_at) from file_events group by provider_id order by provider_id`)
	if err != nil {
		return status, err
	}
	defer func() { _ = eventRows.Close() }()
	for eventRows.Next() {
		var item FileEventsInfo
		if err := eventRows.Scan(&item.ProviderID, &item.Files, &item.UpdatedAt); err == nil {
			status.FileEvents = append(status.FileEvents, item)
		}
	}
	return status, nil
}

func (c *Cache) Prune(existing map[string]struct{}) (int, error) {
	rows, err := c.db.Query(`select provider_id, path from file_events`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	type key struct{ providerID, path string }
	var stale []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.providerID, &k.path); err == nil {
			if _, ok := existing[k.path]; !ok {
				stale = append(stale, k)
			}
		}
	}
	for _, k := range stale {
		if _, err := c.db.Exec(`delete from file_events where provider_id = ? and path = ?`, k.providerID, k.path); err != nil {
			return len(stale), err
		}
	}
	return len(stale), nil
}

func (c *Cache) Vacuum() VacuumResult {
	_, _ = c.db.Exec(`pragma wal_checkpoint(truncate)`)
	before := databaseSize(c.DatabasePath())
	_, _ = c.db.Exec(`vacuum`)
	_, _ = c.db.Exec(`pragma wal_checkpoint(truncate)`)
	after := databaseSize(c.DatabasePath())
	delta := after - before
	result := VacuumResult{BeforeBytes: before, AfterBytes: after, DeltaBytes: delta}
	if delta < 0 {
		result.ReclaimedBytes = -delta
	} else {
		result.GrewBytes = delta
	}
	return result
}

func databaseSize(path string) int64 {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(path + suffix); err == nil {
			total += info.Size()
		}
	}
	return total
}

func pretty(value any) []byte {
	data, _ := json.MarshalIndent(value, "", "  ")
	return data
}
