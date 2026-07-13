package delivery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/server/store"
)

type fakeAdapter struct {
	target   string
	outcomes []Outcome
	seen     []Envelope
	after    func()
}

func (f *fakeAdapter) Target() string { return f.target }
func (f *fakeAdapter) Deliver(_ context.Context, envelope Envelope) Outcome {
	f.seen = append(f.seen, envelope)
	out := f.outcomes[0]
	f.outcomes = f.outcomes[1:]
	if f.after != nil {
		f.after()
	}
	return out
}

type fakeDeliveryStore struct {
	claims     []store.OutboxMessage
	target     string
	successes  int
	retries    int
	terminals  int
	retryAfter time.Duration
	lastError  string
}

func (f *fakeDeliveryStore) ClaimOutboxForTarget(_ context.Context, target string, _ time.Time, _ time.Duration, _ int) ([]store.OutboxMessage, error) {
	f.target = target
	if len(f.claims) == 0 {
		return nil, nil
	}
	claim := f.claims[0]
	f.claims = f.claims[1:]
	return []store.OutboxMessage{claim}, nil
}
func (f *fakeDeliveryStore) FinishOutboxSuccess(context.Context, string, string, string, time.Time) (bool, error) {
	f.successes++
	return true, nil
}
func (f *fakeDeliveryStore) FinishOutboxRetry(_ context.Context, _ store.OutboxMessage, message string, _ time.Time, retryAfter time.Duration) (bool, error) {
	f.retries, f.retryAfter, f.lastError = f.retries+1, retryAfter, message
	return true, nil
}
func (f *fakeDeliveryStore) FinishOutboxTerminal(_ context.Context, _ store.OutboxMessage, message string, _ time.Time) (bool, error) {
	f.terminals, f.lastError = f.terminals+1, message
	return true, nil
}

func deliveryClaim(t *testing.T, id string) store.OutboxMessage {
	t.Helper()
	payload := `{"version":1,"kind":"radar_alert","milestone":50,"probability_24h":0.6,"probability_48h":0,"level":"high","expected_window":"","reasoning_summary":"","checked_at":"","detected_at":"2026-07-13T12:00:00Z","snapshot":{}}`
	return store.OutboxMessage{ID: "outbox-" + id, EventKind: "radar_alert", EventID: id, Source: "test", Target: "webhook:one", PayloadVersion: 1, PayloadJSON: payload, LeaseToken: "lease", Attempts: 1}
}

func TestDispatcherAppliesDeliveredRetryableAndTerminalOutcomes(t *testing.T) {
	st := &fakeDeliveryStore{claims: []store.OutboxMessage{deliveryClaim(t, "one"), deliveryClaim(t, "two"), deliveryClaim(t, "three")}}
	adapter := &fakeAdapter{target: "webhook:one", outcomes: []Outcome{{Disposition: Delivered, ProviderID: "remote-1"}, {Disposition: Retryable, StatusCode: 429, RetryAfter: 30 * time.Minute}, {Disposition: Terminal, StatusCode: 400}}}
	processed, err := (Dispatcher{Store: st, Adapter: adapter}).DispatchOnce(t.Context())
	if err != nil || processed != 3 || st.target != "webhook:one" || st.successes != 1 || st.retries != 1 || st.terminals != 1 || st.retryAfter != 30*time.Minute || st.lastError != "http_400" || len(adapter.seen) != 3 {
		t.Fatalf("processed=%d err=%v store=%+v seen=%d", processed, err, st, len(adapter.seen))
	}
}

func TestDispatcherDeadLettersMalformedPayloadWithoutCallingAdapter(t *testing.T) {
	claim := deliveryClaim(t, "bad")
	claim.PayloadJSON = `{`
	st := &fakeDeliveryStore{claims: []store.OutboxMessage{claim}}
	adapter := &fakeAdapter{target: "webhook:one"}
	processed, err := (Dispatcher{Store: st, Adapter: adapter}).DispatchOnce(t.Context())
	if err != nil || processed != 1 || st.terminals != 1 || st.lastError != "invalid_notification_envelope" || len(adapter.seen) != 0 {
		t.Fatalf("processed=%d err=%v store=%+v seen=%d", processed, err, st, len(adapter.seen))
	}
}

func TestDispatcherDoesNotPersistRawAdapterErrors(t *testing.T) {
	st := &fakeDeliveryStore{claims: []store.OutboxMessage{deliveryClaim(t, "secret")}}
	adapter := &fakeAdapter{target: "ntfy:one", outcomes: []Outcome{{Disposition: Retryable, Err: errors.New("https://secret.example/token")}}}
	if _, err := (Dispatcher{Store: st, Adapter: adapter}).DispatchOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st.lastError != "transport_retryable" {
		t.Fatalf("last error=%q", st.lastError)
	}
}

func TestDispatcherFencesKnownSuccessBeforeReturningCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	st := &fakeDeliveryStore{claims: []store.OutboxMessage{deliveryClaim(t, "shutdown")}}
	adapter := &fakeAdapter{target: "webhook:one", outcomes: []Outcome{{Disposition: Delivered, ProviderID: "remote-1"}}, after: cancel}
	processed, err := (Dispatcher{Store: st, Adapter: adapter}).DispatchOnce(ctx)
	if !errors.Is(err, context.Canceled) || processed != 1 || st.successes != 1 || st.retries != 0 || st.terminals != 0 {
		t.Fatalf("processed=%d err=%v store=%+v", processed, err, st)
	}
}

func TestDispatcherLeavesAmbiguousCanceledTransportClaimLeased(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	st := &fakeDeliveryStore{claims: []store.OutboxMessage{deliveryClaim(t, "ambiguous")}}
	adapter := &fakeAdapter{target: "webhook:one", outcomes: []Outcome{{Disposition: Retryable, Err: context.Canceled}}, after: cancel}
	processed, err := (Dispatcher{Store: st, Adapter: adapter}).DispatchOnce(ctx)
	if !errors.Is(err, context.Canceled) || processed != 0 || st.successes != 0 || st.retries != 0 || st.terminals != 0 {
		t.Fatalf("processed=%d err=%v store=%+v", processed, err, st)
	}
}
