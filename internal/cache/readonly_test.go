package cache

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/agensfield/scriba/internal/model"
)

type fileState struct {
	exists  bool
	data    []byte
	size    int64
	modTime int64
}

func captureFileState(t *testing.T, path string) fileState {
	t.Helper()
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return fileState{}
	}
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- test-owned path.
	if err != nil {
		t.Fatal(err)
	}
	return fileState{exists: true, data: data, size: info.Size(), modTime: info.ModTime().UnixNano()}
}

func assertFileState(t *testing.T, path string, want fileState) {
	t.Helper()
	got := captureFileState(t, path)
	if got.exists != want.exists || got.size != want.size || got.modTime != want.modTime || !bytes.Equal(got.data, want.data) {
		t.Fatalf("file changed during read-only access: %s\nbefore: exists=%v size=%d mtime=%d\nafter:  exists=%v size=%d mtime=%d", path, want.exists, want.size, want.modTime, got.exists, got.size, got.modTime)
	}
}

func TestOpenReadOnlyRequiresExistingRegularDatabase(t *testing.T) {
	root := t.TempDir()
	missingDir := filepath.Join(root, "missing")
	if _, err := OpenReadOnly(missingDir); err == nil {
		t.Fatal("OpenReadOnly accepted a missing database")
	}
	if _, err := os.Stat(missingDir); !os.IsNotExist(err) {
		t.Fatalf("missing cache directory was created: %v", err)
	}

	dir := filepath.Join(root, "cache")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "scriba.sqlite"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(dir); err == nil {
		t.Fatal("OpenReadOnly accepted a non-regular database")
	}
	if _, err := os.Stat(filepath.Join(dir, "status.json")); !os.IsNotExist(err) {
		t.Fatalf("status file was created: %v", err)
	}
}

func TestOpenReadOnlyLoadsStatusWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	writable, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writable.Close() }()
	if err := writable.SaveSnapshot("status", model.StatusSnapshot{GeneratedAt: "2026-07-12T00:00:00Z"}, "2026-07-12T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	paths := []string{
		filepath.Join(dir, "scriba.sqlite"),
		filepath.Join(dir, "status.json"),
	}
	before := make(map[string]fileState, len(paths))
	for _, path := range paths {
		before[path] = captureFileState(t, path)
	}

	for range 3 {
		readonly, err := OpenReadOnly(dir)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := readonly.LoadStatusSnapshot()
		if err != nil {
			_ = readonly.Close()
			t.Fatal(err)
		}
		if snapshot == nil || snapshot.GeneratedAt != "2026-07-12T00:00:00Z" {
			_ = readonly.Close()
			t.Fatalf("unexpected snapshot: %#v", snapshot)
		}
		var tables, snapshots int
		if err := readonly.db.QueryRow(`select count(*) from sqlite_master where type = 'table'`).Scan(&tables); err != nil {
			_ = readonly.Close()
			t.Fatal(err)
		}
		if err := readonly.db.QueryRow(`select count(*) from snapshots`).Scan(&snapshots); err != nil {
			_ = readonly.Close()
			t.Fatal(err)
		}
		if tables != 4 || snapshots != 1 {
			_ = readonly.Close()
			t.Fatalf("cache contents changed: tables=%d snapshots=%d", tables, snapshots)
		}
		if _, err := readonly.db.Exec(`insert into meta(key, value) values('readonly', 'no')`); err == nil {
			_ = readonly.Close()
			t.Fatal("read-only cache accepted a write")
		}
		if err := readonly.Close(); err != nil {
			t.Fatal(err)
		}
	}

	for _, path := range paths {
		assertFileState(t, path, before[path])
	}
}

func TestOpenReadOnlyRejectsFutureSchema(t *testing.T) {
	dir := t.TempDir()
	writable, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writable.db.Exec(`update meta set value = ? where key = 'schema_version'`, SchemaVersion+1); err != nil {
		_ = writable.Close()
		t.Fatal(err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}
	if readonly, err := OpenReadOnly(dir); err == nil {
		_ = readonly.Close()
		t.Fatal("OpenReadOnly accepted a future cache schema")
	}
}

func TestOpenReadOnlyContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpenReadOnlyContext(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
