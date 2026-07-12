package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
)

type fakeOutboxStore struct {
	claims    []store.OutboxMessage
	target    string
	limits    []int
	leases    []time.Duration
	succeeded []string
	failed    []string
}

func (f *fakeOutboxStore) ClaimOutboxForTarget(_ context.Context, target string, _ time.Time, lease time.Duration, limit int) ([]store.OutboxMessage, error) {
	f.target = target
	f.limits = append(f.limits, limit)
	f.leases = append(f.leases, lease)
	if len(f.claims) == 0 {
		return nil, nil
	}
	claim := f.claims[0]
	f.claims = f.claims[1:]
	return []store.OutboxMessage{claim}, nil
}
func (f *fakeOutboxStore) FinishOutboxSuccess(_ context.Context, id, _, _ string, _ time.Time) (bool, error) {
	f.succeeded = append(f.succeeded, id)
	return true, nil
}
func (f *fakeOutboxStore) FinishOutboxFailure(_ context.Context, claim store.OutboxMessage, _ string, _ time.Time) (bool, error) {
	f.failed = append(f.failed, claim.ID)
	return true, nil
}

func TestDurableNotificationsOnlyWakeOutbox(t *testing.T) {
	s := &Service{outboxWake: make(chan struct{}, 1)}
	if err := s.NotifyReset(context.Background(), resetwatch.Event{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.outboxWake:
	default:
		t.Fatal("reset notification did not wake outbox worker")
	}
}

func TestOutboxMalformedPayloadDoesNotStarveBatch(t *testing.T) {
	payload, err := store.EncodeOutboxPayload("reset", resetwatch.Event{ID: "good", PreviousSnapshotJSON: []byte(`{}`), CurrentSnapshotJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOutboxStore{claims: []store.OutboxMessage{
		{ID: "bad", EventKind: "reset", PayloadVersion: 1, PayloadJSON: `{`, Target: "telegram:123", LeaseToken: "a", Attempts: 1},
		{ID: "good", EventKind: "reset", PayloadVersion: 1, PayloadJSON: payload, Target: "telegram:123", LeaseToken: "b", Attempts: 1},
	}}
	s := &Service{cfg: BotConfig{ChatID: 123}, deliveries: f}
	s.retryDeliveriesOnce(context.Background())
	if f.target != "telegram:123" {
		t.Fatalf("claim target = %q", f.target)
	}
	if len(f.failed) != 1 || f.failed[0] != "bad" {
		t.Fatalf("failed = %#v", f.failed)
	}
	if len(f.succeeded) != 1 || f.succeeded[0] != "good" {
		t.Fatalf("succeeded = %#v", f.succeeded)
	}
	if len(f.limits) != 3 {
		t.Fatalf("claim calls = %d, want two rows then empty", len(f.limits))
	}
	for i, limit := range f.limits {
		if limit != 1 {
			t.Fatalf("claim %d limit = %d", i, limit)
		}
		if f.leases[i] <= telegramAPITimeout {
			t.Fatalf("claim %d lease = %s, must exceed send timeout %s", i, f.leases[i], telegramAPITimeout)
		}
	}
}
