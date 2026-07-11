package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

const DefaultBackupRetention = 14

var backupNamePattern = regexp.MustCompile(`^scriba-server-backup-\d{8}T\d{6}\.\d{9}Z-[0-9a-f]{12}\.sqlite$`)

type BackupResult struct {
	Path          string    `json:"path"`
	CreatedAt     time.Time `json:"createdAt"`
	SizeBytes     int64     `json:"sizeBytes"`
	SHA256        string    `json:"sha256"`
	SchemaVersion int       `json:"schemaVersion"`
	QuickCheck    string    `json:"quickCheck"`
	Pruned        int       `json:"pruned"`
}

// Backup creates and verifies an online snapshot without reopening or migrating the source.
func (s *Store) Backup(ctx context.Context, directory string, retention int) (BackupResult, error) {
	if retention < 1 {
		return BackupResult{}, fmt.Errorf("backup retention must be at least 1")
	}
	if directory == "" {
		directory = filepath.Join(filepath.Dir(s.path), "backups")
	}
	if err := ensurePrivateDirectory(directory); err != nil {
		return BackupResult{}, err
	}
	now := time.Now().UTC()
	nonce := make([]byte, 6)
	if _, err := rand.Read(nonce); err != nil {
		return BackupResult{}, err
	}
	name := fmt.Sprintf("scriba-server-backup-%s-%s.sqlite", now.Format("20060102T150405.000000000Z"), hex.EncodeToString(nonce))
	finalPath := filepath.Join(directory, name)
	tmp, err := os.CreateTemp(directory, ".scriba-backup-candidate-*.sqlite")
	if err != nil {
		return BackupResult{}, err
	}
	candidate := tmp.Name()
	if err := tmp.Close(); err != nil {
		return BackupResult{}, err
	}
	_ = os.Remove(candidate) // VACUUM INTO requires that the destination not exist.
	defer func() { _ = os.Remove(candidate) }()
	if _, err := s.db.ExecContext(ctx, `vacuum into ?`, candidate); err != nil {
		return BackupResult{}, fmt.Errorf("create backup candidate: %w", err)
	}
	quickCheck, version, err := validateBackup(ctx, candidate)
	if err != nil {
		return BackupResult{}, err
	}
	if err := os.Chmod(candidate, 0o600); err != nil {
		return BackupResult{}, err
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return BackupResult{}, err
	}
	digest, err := fileSHA256(ctx, candidate)
	if err != nil {
		return BackupResult{}, err
	}
	if err := syncFile(candidate); err != nil {
		return BackupResult{}, err
	}
	if err := os.Rename(candidate, finalPath); err != nil {
		return BackupResult{}, err
	}
	result := BackupResult{Path: finalPath, CreatedAt: now, SizeBytes: info.Size(), SHA256: digest, SchemaVersion: version, QuickCheck: quickCheck}
	if err := syncDirectory(directory); err != nil {
		return result, fmt.Errorf("backup verified at %s but directory sync failed: %w", finalPath, err)
	}
	pruned, err := pruneBackups(directory, retention, name)
	if err != nil {
		return result, fmt.Errorf("backup verified at %s but retention failed: %w", finalPath, err)
	}
	result.Pruned = pruned
	return result, nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("backup path is not a real directory: %s", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("backup directory must not be accessible by group or others: %s has mode %04o", path, info.Mode().Perm())
	}
	return nil
}

func validateBackup(ctx context.Context, path string) (string, int, error) {
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = db.Close() }()
	var check string
	if err := db.QueryRowContext(ctx, `pragma quick_check`).Scan(&check); err != nil {
		return "", 0, fmt.Errorf("validate backup quick_check: %w", err)
	}
	if check != "ok" {
		return check, 0, fmt.Errorf("validate backup quick_check: %s", check)
	}
	var version int
	if err := db.QueryRowContext(ctx, `select max(version) from schema_migrations`).Scan(&version); err != nil {
		return check, 0, fmt.Errorf("validate backup schema: %w", err)
	}
	if version < 1 || version > SchemaVersion {
		return check, version, fmt.Errorf("validate backup schema: unsupported version %d", version)
	}
	for _, table := range []string{"accounts", "limit_observations", "server_settings"} {
		var present int
		if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table' and name = ?`, table).Scan(&present); err != nil {
			return check, version, fmt.Errorf("validate backup table %s: %w", table, err)
		}
		if present != 1 {
			return check, version, fmt.Errorf("validate backup schema: required table %s is missing", table)
		}
	}
	return check, version, nil
}

func fileSHA256(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- path is the internally generated backup candidate.
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, &contextReader{ctx: ctx, reader: f}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY, 0) // #nosec G304 -- path is the internally generated backup candidate.
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

func syncDirectory(path string) error {
	dir, err := os.Open(path) // #nosec G304 -- path is the configured backup directory.
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func pruneBackups(directory string, retain int, preserve string) (int, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && backupNamePattern.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) <= retain {
		return 0, nil
	}
	keep := map[string]bool{preserve: true}
	for _, name := range names {
		if len(keep) < retain {
			keep[name] = true
		}
	}
	pruned := 0
	for _, name := range names {
		if keep[name] {
			continue
		}
		if err := os.Remove(filepath.Join(directory, name)); err != nil {
			return pruned, err
		}
		pruned++
	}
	return pruned, nil
}
