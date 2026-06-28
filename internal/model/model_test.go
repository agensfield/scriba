package model

import "testing"

func TestSchemaVersionIsMainline(t *testing.T) {
	if SchemaVersion != "scriba.v1" {
		t.Fatalf("SchemaVersion = %q, want scriba.v1", SchemaVersion)
	}
}
