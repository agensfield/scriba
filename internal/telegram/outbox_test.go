package telegram

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"

	"github.com/agensfield/scriba/internal/radar"
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

type timeShiftedOutboxStore struct {
	*store.Store
	now time.Time
}

func (s *timeShiftedOutboxStore) ClaimOutboxForTarget(ctx context.Context, target string, _ time.Time, lease time.Duration, limit int) ([]store.OutboxMessage, error) {
	return s.Store.ClaimOutboxForTarget(ctx, target, s.now, lease, limit)
}

func (s *timeShiftedOutboxStore) FinishOutboxSuccess(ctx context.Context, id, token, providerID string, _ time.Time) (bool, error) {
	return s.Store.FinishOutboxSuccess(ctx, id, token, providerID, s.now)
}

func (s *timeShiftedOutboxStore) FinishOutboxFailure(ctx context.Context, claim store.OutboxMessage, message string, _ time.Time) (bool, error) {
	return s.Store.FinishOutboxFailure(ctx, claim, message, s.now)
}

func TestPolicyTransitionRetriesThroughRealOutboxToTelegram(t *testing.T) {
	ctx := context.Background()
	state, err := store.Open(filepath.Join(t.TempDir(), "scriba.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })

	base := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	period := int64((5 * time.Hour) / time.Millisecond)
	observation := func(at time.Time, used float64) resetwatch.Observation {
		return resetwatch.Observation{
			ProviderID:   resetwatch.ProviderCodex,
			Account:      resetwatch.Account{Ref: "acct", Label: "Test"},
			ObservedAt:   at,
			SnapshotJSON: []byte(`{"source":"fixture"}`),
			Windows:      []resetwatch.Window{{Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: at.Add(5 * time.Hour), PeriodDurationMs: &period}},
		}
	}
	apply := func(obs resetwatch.Observation) store.CodexPollResult {
		t.Helper()
		result, applyErr := state.ApplyCodexPoll(ctx, store.CodexPollInput{
			Observation:        obs,
			NotificationTarget: "telegram:123",
			ResetOptions:       resetwatch.DefaultOptions(),
			CommittedAt:        obs.ObservedAt.Add(time.Second),
		})
		if applyErr != nil {
			t.Fatal(applyErr)
		}
		return result
	}
	if got := apply(observation(base, 70)); !got.PolicyBootstrap || len(got.PolicyEvents) != 0 {
		t.Fatalf("bootstrap=%+v", got)
	}
	transition := apply(observation(base.Add(15*time.Minute), 81))
	if len(transition.PolicyEvents) != 1 || len(transition.WarningEvents) != 1 {
		t.Fatalf("transition=%+v", transition)
	}

	var requests atomic.Int32
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		request := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if request == 1 {
			time.Sleep(500 * time.Millisecond)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42,"date":0,"chat":{"id":123,"type":"private"}}}`))
	}))
	t.Cleanup(telegram.Close)

	deliveries := &timeShiftedOutboxStore{Store: state, now: base.Add(16 * time.Minute)}
	svc, err := newBotService(
		BotConfig{Token: "test", ChatID: 123},
		nil,
		nil,
		deliveries,
		radar.Client{},
		tgbot.WithServerURL(telegram.URL),
		tgbot.WithSkipGetMe(),
	)
	if err != nil {
		t.Fatal(err)
	}
	svc.apiTimeout = 100 * time.Millisecond

	svc.retryDeliveriesOnce(ctx)
	rows, err := state.ListOutbox(ctx, store.OutboxFilter{Target: "telegram:123", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("after failure rows=%+v err=%v", rows, err)
	}
	if rows[0].Status != "pending" || rows[0].Attempts != 1 || rows[0].LastError == "" {
		t.Fatalf("after failure=%+v", rows[0])
	}

	deliveries.now = deliveries.now.Add(store.OutboxBackoff(rows[0].Attempts))
	svc.retryDeliveriesOnce(ctx)
	rows, err = state.ListOutbox(ctx, store.OutboxFilter{Target: "telegram:123", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("after retry rows=%+v err=%v", rows, err)
	}
	if requests.Load() != 2 || rows[0].Status != "delivered" || rows[0].Attempts != 2 || rows[0].ProviderMessageID != "42" || rows[0].LastError != "" {
		t.Fatalf("requests=%d after retry=%+v", requests.Load(), rows[0])
	}
}
