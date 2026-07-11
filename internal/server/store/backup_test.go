package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupCapturesCommittedWALRowsAndDoesNotMigrateSource(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "server.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.SetSetting(ctx, "backup_test", "committed-in-wal"); err != nil {
		t.Fatal(err)
	}
	before, err := st.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := st.Backup(ctx, filepath.Join(dir, "backups"), DefaultBackupRetention)
	if err != nil {
		t.Fatal(err)
	}
	if result.QuickCheck != "ok" || result.SchemaVersion != before || len(result.SHA256) != 64 || result.SizeBytes == 0 {
		t.Fatalf("bad metadata: %+v", result)
	}
	info, err := os.Stat(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	db, err := sql.Open("sqlite", "file:"+result.Path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var value string
	if err := db.QueryRow(`select value from server_settings where key = 'backup_test'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "committed-in-wal" {
		t.Fatalf("value = %q", value)
	}
	after, err := st.SchemaVersion(ctx)
	if err != nil || after != before {
		t.Fatalf("source schema changed: before=%d after=%d err=%v", before, after, err)
	}
}

func TestOpenExistingDoesNotCreateOrMigrate(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.sqlite")
	if _, err := OpenExisting(missing); err == nil {
		t.Fatal("expected missing database to fail")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing database was created: %v", err)
	}

	path := filepath.Join(dir, "legacy.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table schema_migrations (version integer primary key, applied_at text not null); insert into schema_migrations values (5, 'test')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	version, err := st.SchemaVersion(context.Background())
	if err != nil || version != 5 {
		t.Fatalf("version = %d, err = %v", version, err)
	}
	var hasAccounts int
	if err := st.db.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = 'accounts'`).Scan(&hasAccounts); err != nil {
		t.Fatal(err)
	}
	if hasAccounts != 0 {
		t.Fatal("OpenExisting migrated the database")
	}
}

func TestBackupRetentionIgnoresUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "server.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	backupDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(backupDir, "notes.sqlite")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := st.Backup(context.Background(), backupDir, 2); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, entry := range entries {
		if backupNamePattern.MatchString(entry.Name()) {
			matched++
		}
	}
	if matched != 2 {
		t.Fatalf("matched backups = %d", matched)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated file removed: %v", err)
	}
}

func TestBackupRetentionKeepsReturnedBackup(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "server.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	backupDir := filepath.Join(dir, "backups")
	for i := 0; i < 5; i++ {
		result, err := st.Backup(context.Background(), backupDir, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(result.Path); err != nil {
			t.Fatalf("returned backup was pruned: %v", err)
		}
	}
}

func TestBackupRejectsPublicOrSymlinkDirectory(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "server.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	public := filepath.Join(dir, "public")
	if err := os.Mkdir(public, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Backup(context.Background(), public, 1); err == nil {
		t.Fatal("expected public backup directory to fail")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(public, link); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Backup(context.Background(), link, 1); err == nil {
		t.Fatal("expected symlink backup directory to fail")
	}
}

func TestBackupFailureDoesNotPromote(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "server.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	backupDir := filepath.Join(dir, "backups")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.Backup(ctx, backupDir, DefaultBackupRetention); err == nil {
		t.Fatal("expected canceled backup to fail")
	}
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if backupNamePattern.MatchString(entry.Name()) || strings.HasPrefix(entry.Name(), ".scriba-backup-candidate-") {
			t.Fatalf("failed backup left %s", entry.Name())
		}
	}
}
