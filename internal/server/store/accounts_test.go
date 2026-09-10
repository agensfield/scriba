package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/resetwatch"
)

const (
	testSourceA = "src-00000000000000000000"
	testSourceB = "src-11111111111111111111"
	testSourceC = "src-22222222222222222222"
)

func TestRegisterAuthSourceAccountAliasIsAtomicAndSourceScoped(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	checked := time.Date(2026, 9, 10, 20, 0, 0, 0, time.UTC)
	if err := s.SyncAuthSources(ctx, []SourceSpec{{Ref: testSourceA, Enabled: true, Priority: 0}, {Ref: testSourceB, Enabled: true, Priority: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveAuthSource(ctx, testSourceB, resetwatch.Account{Ref: "existing", Email: "existing@example.com"}, checked); err != nil {
		t.Fatal(err)
	}
	cold := resetwatch.Account{Ref: "cold", Email: "cold@example.com", Plan: "plus"}
	if err := s.RegisterAuthSourceAccountAlias(ctx, SourceSpec{Ref: testSourceC, Priority: 2}, cold, "personal", checked.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	account, ok, err := s.ResolveAccount(ctx, "personal")
	if err != nil || !ok || account.Ref != cold.Ref || !account.CredentialsAvailable || !account.LastSeenAt.IsZero() {
		t.Fatalf("cold account=%+v ok=%v err=%v", account, ok, err)
	}
	health, err := s.ListSourceHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bound := map[string]string{}
	for _, source := range health {
		bound[source.SourceRef] = source.AccountRef
	}
	if bound[testSourceB] != "existing" || bound[testSourceC] != "cold" {
		t.Fatalf("source bindings=%v", bound)
	}
	var observations int
	if err := s.db.QueryRow(`select count(*) from limit_observations where account_ref='cold'`).Scan(&observations); err != nil || observations != 0 {
		t.Fatalf("cold observations=%d err=%v", observations, err)
	}
	if err := s.RegisterAuthSourceAccountAlias(ctx, SourceSpec{Ref: "src-33333333333333333333", Priority: 3}, resetwatch.Account{Ref: "invalid"}, "current", checked); !errors.Is(err, ErrInvalidAccountAlias) {
		t.Fatalf("invalid alias err=%v", err)
	}
	var invalidRows int
	if err := s.db.QueryRow(`select count(*) from accounts where account_ref='invalid'`).Scan(&invalidRows); err != nil || invalidRows != 0 {
		t.Fatalf("invalid alias partially registered rows=%d err=%v", invalidRows, err)
	}
}

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

func TestAccountReadsDoNotExhaustSingleConnectionPool(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncAuthSources(ctx, []SourceSpec{{Ref: testSourceA, Enabled: true, Priority: 0}, {Ref: testSourceB, Enabled: true, Priority: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveAuthSource(ctx, testSourceA, resetwatch.Account{Ref: "one", Email: "one@example.com"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveAuthSource(ctx, testSourceB, resetwatch.Account{Ref: "two", Email: "two@example.com"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 8 {
		wg.Add(1)
		go func(resolve bool) {
			defer wg.Done()
			if resolve {
				_, ok, err := s.ResolveAccount(bounded, AccountID("codex", "two"))
				if err == nil && !ok {
					err = errors.New("account not resolved")
				}
				errs <- err
				return
			}
			accounts, err := s.ListAccounts(bounded)
			if err == nil && len(accounts) != 2 {
				err = errors.New("account list incomplete")
			}
			errs <- err
		}(i%2 == 0)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}
