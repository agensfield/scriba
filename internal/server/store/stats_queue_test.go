package store

import (
	"context"
	"testing"
	"time"
)

func TestStatsSummarizesOutboxAndTelegramInbox(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour)
	_, err := s.db.Exec(`insert into notification_outbox(id,event_kind,source,event_id,target,payload_version,payload_json,status,attempts,available_at,lease_token,lease_expires_at,created_at,updated_at) values
('pending','reset','test','e1','telegram:1',1,'{}','pending',2,?,null,null,?,?),
('leased','reset','test','e2','telegram:1',1,'{}','leased',3,?,'lease',?,?,?)`, formatTime(now.Add(-time.Minute)), formatTime(old), formatTime(old), formatTime(now), formatTime(now.Add(-time.Minute)), formatTime(old), formatTime(old))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.StageTelegramUpdates(ctx, "bot", []TelegramUpdateInput{{UpdateID: 1, RawJSON: `{}`}, {UpdateID: 2, RawJSON: `{}`}}, old); err != nil {
		t.Fatal(err)
	}
	if ok, e := s.MarkTelegramUpdateProcessed(ctx, "bot", 2, now); e != nil || !ok {
		t.Fatalf("processed ok=%v err=%v", ok, e)
	}
	if ok, e := s.MarkTelegramUpdateFailure(ctx, "bot", 1, "retry", old); e != nil || !ok {
		t.Fatalf("retry ok=%v err=%v", ok, e)
	}

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Outbox.Pending != 1 || stats.Outbox.Leased != 1 || stats.Outbox.DuePending != 1 || stats.Outbox.ExpiredLeases != 1 || stats.Outbox.Attempts != 5 {
		t.Fatalf("outbox: %#v", stats.Outbox)
	}
	if stats.Outbox.OldestPendingAt == nil || stats.Outbox.OldestPendingAge < time.Hour {
		t.Fatalf("oldest outbox: %#v", stats.Outbox)
	}
	if stats.TelegramInbox.Pending != 1 || stats.TelegramInbox.Processed != 1 || stats.TelegramInbox.Due != 1 || stats.TelegramInbox.Attempts != 1 {
		t.Fatalf("inbox: %#v", stats.TelegramInbox)
	}
}
