package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/resetwatch"
)

const (
	testSourceA = "src-00000000000000000000"
	testSourceB = "src-11111111111111111111"
)

func TestAccountRegistryDiscoveryAliasAndSourceRotation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncAuthSources(ctx, []SourceSpec{{Ref: testSourceA, Enabled: true, Priority: 1}, {Ref: testSourceB, Enabled: true, Priority: 0}}); err != nil {
		t.Fatal(err)
	}
	first := resetwatch.Account{Ref: "private-one", Email: "one@example.com", Plan: "plus"}
	second := resetwatch.Account{Ref: "private-two", Email: "two@example.com", Plan: "pro"}
	checked := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	if err := s.ObserveAuthSource(ctx, testSourceA, first, checked); err != nil {
		t.Fatal(err)
	}
	a, ok, err := s.ResolveAccount(ctx, AccountID("codex", first.Ref))
	if err != nil || !ok || !a.LastSeenAt.IsZero() || !a.CredentialsAvailable {
		t.Fatalf("discovered account=%+v ok=%v err=%v", a, ok, err)
	}
	if err = s.SetAccountAlias(ctx, a.ID, "personal"); err != nil {
		t.Fatal(err)
	}
	if err = s.ObserveAuthSource(ctx, testSourceA, second, checked.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = s.ObserveAuthSource(ctx, testSourceB, first, checked.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = s.SyncAuthSources(ctx, []SourceSpec{{Ref: testSourceA, Enabled: true, Priority: 1}, {Ref: testSourceB, Enabled: true, Priority: 0}}); err != nil {
		t.Fatal(err)
	}
	current, ok, err := s.ResolveAccount(ctx, "current")
	if err != nil || !ok || current.Ref != first.Ref || current.Alias != "personal" || current.DisplayName() != "personal" {
		t.Fatalf("current=%+v ok=%v err=%v", current, ok, err)
	}
	old, ok, err := s.ResolveAccount(ctx, "personal")
	if err != nil || !ok || old.Ref != first.Ref || old.Alias != "personal" {
		t.Fatalf("alias changed during rotation: %+v ok=%v err=%v", old, ok, err)
	}
	if err = s.ObserveAuthSource(ctx, testSourceB, resetwatch.Account{}, checked.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	old, ok, err = s.ResolveAccount(ctx, "personal")
	if err != nil || !ok || old.CredentialsAvailable {
		t.Fatalf("historical account=%+v ok=%v err=%v", old, ok, err)
	}
}

func TestAccountSelectorsNeverFallBackAndAliasesAreUnique(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncAuthSources(ctx, []SourceSpec{{Ref: testSourceA, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveAuthSource(ctx, testSourceA, resetwatch.Account{Ref: "one", Email: "one@example.com"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	a, _, _ := s.ResolveAccount(ctx, "current")
	if err := s.SetAccountAlias(ctx, a.ID, "main"); err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{"missing", "acct-ffffffffffffffffffff"} {
		if got, ok, err := s.ResolveAccount(ctx, selector); err != nil || ok || got.Ref != "" {
			t.Fatalf("selector %q fell back: %+v ok=%v err=%v", selector, got, ok, err)
		}
	}
	for _, alias := range []string{"current", "acct-ffffffffffffffffffff", "Upper", "two--parts"} {
		if err := s.SetAccountAlias(ctx, a.ID, alias); !errors.Is(err, ErrInvalidAccountAlias) {
			t.Fatalf("alias %q err=%v", alias, err)
		}
	}
}

func TestPollObservationAdvancesFreshnessWithoutOverwritingAlias(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncAuthSources(ctx, []SourceSpec{{Ref: testSourceA, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	checked := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	account := resetwatch.Account{Ref: "one", Email: "before@example.com"}
	if err := s.ObserveAuthSource(ctx, testSourceA, account, checked); err != nil {
		t.Fatal(err)
	}
	a, _, _ := s.ResolveAccount(ctx, "current")
	if err := s.SetAccountAlias(ctx, a.ID, "stable-name"); err != nil {
		t.Fatal(err)
	}
	obs := codexPollObservation(checked.Add(time.Hour), 50)
	obs.Account.Ref, obs.Account.Email = account.Ref, "after@example.com"
	in := pollInput(obs, "")
	in.SourceRef = testSourceA
	if _, err := s.ApplyCodexPoll(ctx, in); err != nil {
		t.Fatal(err)
	}
	a, ok, err := s.ResolveAccount(ctx, "stable-name")
	if err != nil || !ok || a.Alias != "stable-name" || !a.LastSeenAt.Equal(obs.ObservedAt) || a.Email != "after@example.com" {
		t.Fatalf("account=%+v ok=%v err=%v", a, ok, err)
	}
}

func TestSourceHealthFencing(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncAuthSources(ctx, []SourceSpec{{Ref: testSourceA, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	if err := s.RecordSourcePollAttempt(ctx, testSourceA, at); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSourcePollFailure(ctx, testSourceA, at, at.Add(time.Second), SourceFailureNetwork, SourceErrorTimeout); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSourcePollSuccess(ctx, testSourceA, at, at.Add(2*time.Second)); !errors.Is(err, ErrSourcePollStale) {
		t.Fatalf("late success err=%v", err)
	}
	health, err := s.ListSourceHealth(ctx)
	if err != nil || len(health) != 1 || health[0].ConsecutiveFailures != 1 || health[0].FailureKind != SourceFailureNetwork {
		t.Fatalf("health=%+v err=%v", health, err)
	}
}
