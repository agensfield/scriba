package cache

import (
	"testing"

	"github.com/agensfield/scriba/internal/model"
)

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
