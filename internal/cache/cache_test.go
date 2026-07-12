package cache

import (
	"context"
	"os"
	"testing"

	"github.com/agensfield/scriba/internal/model"
)

func TestSQLiteConfigurationAndConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if got := c.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("max connections = %d", got)
	}
	var busy, foreign int
	if err := c.db.QueryRow(`pragma busy_timeout`).Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if err := c.db.QueryRow(`pragma foreign_keys`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if busy != 5000 || foreign != 1 {
		t.Fatalf("busy=%d foreign=%d", busy, foreign)
	}
	info, err := os.Stat(c.DatabasePath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache mode: %v, %v", info, err)
	}
	ctx := context.Background()
	done := make(chan error, 2)
	go func() {
		_, err := c.db.ExecContext(ctx, `insert or replace into meta(key,value) values('concurrent','write')`)
		done <- err
	}()
	go func() {
		var value string
		err := c.db.QueryRowContext(ctx, `select value from meta where key='schema_version'`).Scan(&value)
		done <- err
	}()
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadStatusSnapshotNormalizesLegacySchemaVersion(t *testing.T) {
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	defer func() { _ = c.Close() }()

	if err := c.SaveSnapshot("status", model.StatusSnapshot{
		SchemaVersion: "scriba.alpha.v1",
		GeneratedAt:   "2026-06-01T00:00:00Z",
	}, "2026-06-01T00:00:00Z"); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	snapshot, err := c.LoadStatusSnapshot()
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if snapshot == nil {
		t.Fatal("expected snapshot")
	}
	if snapshot.SchemaVersion != model.SchemaVersion {
		t.Fatalf("schema version = %q, want %q", snapshot.SchemaVersion, model.SchemaVersion)
	}
}
