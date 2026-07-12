package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/resetwatch"
)

func TestLoadAgentEventsBoundedAndDeterministic(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `insert into accounts(provider_id,account_ref,label,email,plan,updated_at) values('codex','acct','','','','2026-07-12T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	payload := agentWarningPayload(t)
	for i := 0; i < 105; i++ {
		id := fmt.Sprintf("event-%03d", i)
		at := "2026-07-12T00:00:00Z"
		if i < 103 {
			at = time.Date(2026, 7, 11, 0, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		}
		_, err := s.db.ExecContext(ctx, `insert into policy_events(id,semantic_key,event_kind,semantic_event_id,rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,payload_version,payload_json,detected_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, id, "limit_warning", id, "rule", "subject", "remaining_checkpoint", "codex", "acct", "rev", "hash", 1, payload, at, at)
		if err != nil {
			t.Fatal(err)
		}
	}

	events, err := s.LoadAgentEvents(ctx, "codex", "acct", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 100 {
		t.Fatalf("events = %d, want 100", len(events))
	}
	if events[0].ID != "event-104" || events[1].ID != "event-103" {
		t.Fatalf("unstable newest tie ordering: %q, %q", events[0].ID, events[1].ID)
	}
	if got := events[0]; got.Kind != "remaining_checkpoint" || got.ProviderID != "codex" || got.WindowKey != "primary.five_hour" || got.Checkpoint != 20 || got.UsedPercent == nil || *got.UsedPercent != 81 || got.RemainingPercentPoints == nil || *got.RemainingPercentPoints != 19 {
		t.Fatalf("unexpected event mapping: %#v", got)
	}

	one, err := s.LoadAgentEvents(ctx, "codex", "acct", 1)
	if err != nil || len(one) != 1 || !reflect.DeepEqual(one[0], events[0]) {
		t.Fatalf("repeat read differs: events=%#v err=%v", one, err)
	}
}

func TestLoadAgentEventsThroughReadOnlyStoreDoesNotMutateFilesOrDatabase(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `insert into accounts(provider_id,account_ref,label,email,plan,updated_at) values('codex','acct','','','','2026-07-12T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `insert into policy_events(id,semantic_key,event_kind,semantic_event_id,rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,payload_version,payload_json,detected_at,created_at) values('event','event','limit_warning','event','rule','subject','remaining_checkpoint','codex','acct','rev','hash',1,?,'2026-07-12T00:00:00Z','2026-07-12T00:00:00Z')`, agentWarningPayload(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `pragma wal_checkpoint(truncate)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	mainBefore := snapshotStoreFiles(t, s.path)[s.path]
	ro, err := OpenReadOnly(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ro.LoadAgentEvents(ctx, "codex", "acct", 10); err != nil {
		t.Fatal(err)
	}
	before := snapshotStoreFiles(t, s.path)
	for i := 0; i < 3; i++ {
		events, err := ro.LoadAgentEvents(ctx, "codex", "acct", 10)
		if err != nil || len(events) != 1 {
			t.Fatalf("read %d: events=%d err=%v", i, len(events), err)
		}
	}
	var version, rows, schemaObjects int
	if err := ro.db.QueryRowContext(ctx, `select max(version) from schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := ro.db.QueryRowContext(ctx, `select count(*) from policy_events`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := ro.db.QueryRowContext(ctx, `select count(*) from sqlite_schema`).Scan(&schemaObjects); err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion || rows != 1 || schemaObjects == 0 {
		t.Fatalf("database state changed: version=%d rows=%d schema=%d", version, rows, schemaObjects)
	}
	after := snapshotStoreFiles(t, s.path)
	if fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("repeated read-only access changed database files:\nbefore=%v\nafter=%v", before, after)
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	mainAfter := snapshotStoreFiles(t, s.path)[s.path]
	if mainAfter != mainBefore {
		t.Fatalf("read-only open changed main database: before=%v after=%v", mainBefore, mainAfter)
	}
}

func agentWarningPayload(t *testing.T) string {
	t.Helper()
	payload, err := EncodeOutboxPayload("limit_warning", resetwatch.WarningEvent{
		Account:            resetwatch.Account{Label: "PRIVATE_LABEL", Email: "PRIVATE_EMAIL", Plan: "PRIVATE_PLAN"},
		Label:              resetwatch.LabelFiveHour,
		ThresholdRemaining: 20,
		UsedPercent:        81,
		RemainingPercent:   19,
		ResetAt:            time.Date(2026, 7, 12, 13, 0, 0, 0, time.UTC),
		SnapshotJSON:       []byte(`{"secret":"PRIVATE_SNAPSHOT"}`),
		DetectedAt:         time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

type storeFileSnapshot struct {
	Exists bool
	Size   int64
	ModNS  int64
	Hash   [sha256.Size]byte
}

func snapshotStoreFiles(t *testing.T, path string) map[string]storeFileSnapshot {
	t.Helper()
	out := make(map[string]storeFileSnapshot, 3)
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if os.IsNotExist(err) {
			out[candidate] = storeFileSnapshot{}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		contents, err := os.ReadFile(candidate)
		if err != nil {
			t.Fatal(err)
		}
		out[candidate] = storeFileSnapshot{Exists: true, Size: info.Size(), ModNS: info.ModTime().UnixNano(), Hash: sha256.Sum256(contents)}
	}
	return out
}
