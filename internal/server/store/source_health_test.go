package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSourceHealthValidationDisabledIsolationAndCAS(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncAuthSources(ctx, []SourceSpec{{Ref: testSourceA, Enabled: true, Priority: 0}, {Ref: testSourceB, Enabled: false, Priority: 1}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	for ref, want := range map[string]error{"BAD": ErrInvalidSource, "src-22222222222222222222": ErrSourceMissing, testSourceB: ErrSourceDisabled} {
		if err := s.RecordSourcePollAttempt(ctx, ref, now); !errors.Is(err, want) {
			t.Fatalf("ref=%q err=%v want=%v", ref, err, want)
		}
	}
	if err := s.RecordSourcePollAttempt(ctx, testSourceA, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSourcePollFailure(ctx, testSourceA, now, now.Add(time.Second), SourceFailureNetwork, SourceErrorTimeout); err != nil {
		t.Fatal(err)
	}
	swapped, err := s.CompareAndSwapSourceAlertState(ctx, testSourceA, "ok", "failing")
	if err != nil || !swapped {
		t.Fatalf("first CAS swapped=%v err=%v", swapped, err)
	}
	swapped, err = s.CompareAndSwapSourceAlertState(ctx, testSourceA, "ok", "failing")
	if err != nil || swapped {
		t.Fatalf("second CAS swapped=%v err=%v", swapped, err)
	}
	health, err := s.ListSourceHealth(ctx)
	if err != nil || len(health) != 2 {
		t.Fatalf("health=%+v err=%v", health, err)
	}
	if health[0].SourceRef != testSourceA || health[0].ConsecutiveFailures != 1 || health[0].AlertState != "failing" {
		t.Fatalf("source A=%+v", health[0])
	}
	if health[1].SourceRef != testSourceB || health[1].ConsecutiveFailures != 0 || health[1].AlertState != "ok" {
		t.Fatalf("source B=%+v", health[1])
	}
}

func TestSourceHealthAbortRestoresLatestTerminal(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncAuthSources(ctx, []SourceSpec{{Ref: testSourceA, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	if err := s.RecordSourcePollAttempt(ctx, testSourceA, base); err != nil {
		t.Fatal(err)
	}
	completed := base.Add(time.Second)
	if err := s.RecordSourcePollSuccess(ctx, testSourceA, base, completed); err != nil {
		t.Fatal(err)
	}
	pending := base.Add(2 * time.Second)
	if err := s.RecordSourcePollAttempt(ctx, testSourceA, pending); err != nil {
		t.Fatal(err)
	}
	if err := s.AbortSourcePollAttempt(ctx, testSourceA, pending); err != nil {
		t.Fatal(err)
	}
	health, err := s.ListSourceHealth(ctx)
	if err != nil || len(health) != 1 || health[0].LastAttemptAt == nil || !health[0].LastAttemptAt.Equal(completed) {
		t.Fatalf("health=%+v err=%v", health, err)
	}
	if err := s.RecordSourcePollFailure(ctx, testSourceA, pending, pending.Add(time.Second), SourceFailureNetwork, SourceErrorTimeout); !errors.Is(err, ErrSourcePollStale) {
		t.Fatalf("aborted completion err=%v", err)
	}
	if err := s.RecordSourcePollAttempt(ctx, testSourceA, completed); !errors.Is(err, ErrSourcePollStale) {
		t.Fatalf("stale attempt err=%v", err)
	}
}

func TestSourceHealthCompletionFenceHasOneWinner(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncAuthSources(ctx, []SourceSpec{{Ref: testSourceA, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	attempt := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	if err := s.RecordSourcePollAttempt(ctx, testSourceA, attempt); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs <- s.RecordSourcePollSuccess(ctx, testSourceA, attempt, attempt.Add(time.Second))
	}()
	go func() {
		defer wg.Done()
		errs <- s.RecordSourcePollFailure(ctx, testSourceA, attempt, attempt.Add(2*time.Second), SourceFailureNetwork, SourceErrorTimeout)
	}()
	wg.Wait()
	close(errs)
	var completed, stale int
	for err := range errs {
		switch {
		case err == nil:
			completed++
		case errors.Is(err, ErrSourcePollStale):
			stale++
		default:
			t.Fatal(err)
		}
	}
	if completed != 1 || stale != 1 {
		t.Fatalf("completed=%d stale=%d", completed, stale)
	}
}
