package store

import (
	"context"
	"testing"
	"time"
)

func TestTelegramUpdatesStageIsAtomicIdempotentAndMonotonic(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.migrateNotificationOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	batch := []TelegramUpdateInput{{UpdateID: 11, RawJSON: `{"update_id":11}`}, {UpdateID: 10, RawJSON: `{"update_id":10}`}, {UpdateID: 11, RawJSON: `{"update_id":11}`}}
	if err := s.StageTelegramUpdates(ctx, "default", batch, now); err != nil {
		t.Fatal(err)
	}
	if err := s.StageTelegramUpdates(ctx, "default", batch, now); err != nil {
		t.Fatal(err)
	}
	rows, err := s.DueTelegramUpdates(ctx, "default", now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].UpdateID != 10 || rows[1].UpdateID != 11 {
		t.Fatalf("rows=%+v", rows)
	}
	offset, ok, err := s.GetTelegramOffset(ctx, "default")
	if err != nil || !ok || offset != 11 {
		t.Fatalf("offset=%d ok=%v err=%v", offset, ok, err)
	}
	if err := s.StageTelegramUpdates(ctx, "default", []TelegramUpdateInput{{UpdateID: 12, RawJSON: `{"update_id":12}`}, {UpdateID: 13, RawJSON: `not-json`}}, now); err == nil {
		t.Fatal("expected invalid JSON failure")
	}
	offset, _, _ = s.GetTelegramOffset(ctx, "default")
	if offset != 11 {
		t.Fatalf("atomic staging advanced offset to %d", offset)
	}
	var n int
	if err = s.db.QueryRow(`select count(*) from telegram_updates where update_id=12`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rolled-back rows=%d err=%v", n, err)
	}
}

func TestTelegramUpdatesLifecycleAndStats(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.migrateNotificationOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := s.StageTelegramUpdates(ctx, "default", []TelegramUpdateInput{{1, `{"update_id":1}`}, {2, `{"update_id":2}`}, {3, `{"update_id":3}`}}, now); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.MarkTelegramUpdateProcessed(ctx, "default", 1, now); err != nil || !ok {
		t.Fatalf("processed=%v err=%v", ok, err)
	}
	if ok, err := s.MarkTelegramUpdateFailure(ctx, "default", 2, "retry", now); err != nil || !ok {
		t.Fatalf("retry=%v err=%v", ok, err)
	}
	if ok, err := s.MarkTelegramUpdateDead(ctx, "default", 3, "bad json", now); err != nil || !ok {
		t.Fatalf("dead=%v err=%v", ok, err)
	}
	stats, err := s.TelegramUpdateStats(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 || stats.Processed != 1 || stats.Dead != 1 {
		t.Fatalf("stats=%+v", stats)
	}
	rows, err := s.DueTelegramUpdates(ctx, "default", now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("retry was due too early: %+v", rows)
	}
	rows, err = s.DueTelegramUpdates(ctx, "default", now.Add(time.Second), 10)
	if err != nil || len(rows) != 1 || rows[0].Attempts != 1 {
		t.Fatalf("retry rows=%+v err=%v", rows, err)
	}
}

func TestTelegramInboxSchemaIsDormantV7(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	v, err := s.SchemaVersion(ctx)
	if err != nil || v != SchemaVersion {
		t.Fatalf("version=%d err=%v", v, err)
	}
	if err = s.migrateNotificationOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	v, err = s.SchemaVersion(ctx)
	if err != nil || v != OutboxSchemaVersion {
		t.Fatalf("v7 version=%d err=%v", v, err)
	}
	for _, column := range []string{"bot_ref", "update_id", "raw_json", "status", "attempts", "available_at", "processed_at", "dead_at"} {
		cols, err := s.tableColumns(ctx, "telegram_updates")
		if err != nil || !cols[column] {
			t.Fatalf("missing column %s err=%v", column, err)
		}
	}
}
