package store

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestProfileHealthFencingIsolationPrivacyAndCAS(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"one", "codex", "One", true, true}, {"two", "codex", "Two", true, false}}); err != nil {
		t.Fatal(err)
	}
	a := time.Date(2026, 7, 13, 0, 0, 0, 1, time.UTC)
	b := a.Add(time.Nanosecond)
	if err := s.RecordProfilePollAttempt(ctx, "one", a); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordProfilePollAttempt(ctx, "one", b); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordProfilePollSuccess(ctx, "one", a, a); !errors.Is(err, ErrProfilePollStale) {
		t.Fatalf("stale success=%v", err)
	}
	if err := s.RecordProfilePollFailure(ctx, "one", a, a, "network", "timeout"); !errors.Is(err, ErrProfilePollStale) {
		t.Fatalf("stale failure=%v", err)
	}
	if err := s.AbortProfilePollAttempt(ctx, "one", a); !errors.Is(err, ErrProfilePollStale) {
		t.Fatalf("stale abort=%v", err)
	}
	if err := s.RecordProfilePollFailure(ctx, "one", b, b, "network", "timeout"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordProfilePollFailure(ctx, "one", b, b, "network", "/secret/auth.json"); err == nil {
		t.Fatal("raw error accepted")
	}
	swapped, err := s.CompareAndSwapProfileAlertState(ctx, "one", "ok", "failing")
	if err != nil || !swapped {
		t.Fatalf("cas=%v err=%v", swapped, err)
	}
	swapped, err = s.CompareAndSwapProfileAlertState(ctx, "one", "ok", "failing")
	if err != nil || swapped {
		t.Fatalf("retry cas=%v err=%v", swapped, err)
	}
	c := b.Add(time.Second)
	if err := s.RecordProfilePollAttempt(ctx, "one", c); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordProfilePollSuccess(ctx, "one", c, c.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	health, err := s.ListProfileHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(health) != 3 || health[0].ProfileRef != "one" || health[0].ConsecutiveFailures != 0 || health[0].FailureKind != "" || health[0].AlertState != "failing" || health[2].ProfileRef != "two" || health[2].ConsecutiveFailures != 0 {
		t.Fatalf("health=%+v", health)
	}
}

func TestProfileHealthValidationAbortAndConcurrentFailures(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"one", "codex", "One", true, true}, {"off", "codex", "Off", false, false}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for ref, want := range map[string]error{"BAD": ErrInvalidProfile, "missing": ErrProfileMissing, "off": ErrProfileDisabled} {
		if err := s.RecordProfilePollAttempt(ctx, ref, now); !errors.Is(err, want) {
			t.Fatalf("%s err=%v", ref, err)
		}
	}
	if err := s.RecordProfilePollAttempt(ctx, "one", now); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordProfilePollFailure(ctx, "one", now, now, "network", "timeout"); err != nil {
		t.Fatal(err)
	}
	next := now.Add(time.Second)
	if err := s.RecordProfilePollAttempt(ctx, "one", next); err != nil {
		t.Fatal(err)
	}
	if err := s.AbortProfilePollAttempt(ctx, "one", next); err != nil {
		t.Fatal(err)
	}
	h, _ := s.ListProfileHealth(ctx)
	if h[0].LastAttemptAt == nil || !h[0].LastAttemptAt.Equal(now) {
		t.Fatalf("abort=%+v", h[0])
	}
}

func TestProfileHealthConcurrentFailuresAreIsolatedAndMonotonic(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"one", "codex", "One", true, true}, {"two", "codex", "Two", true, false}}); err != nil {
		t.Fatal(err)
	}
	attempt := time.Now().UTC()
	if err := s.RecordProfilePollAttempt(ctx, "one", attempt); err != nil {
		t.Fatal(err)
	}
	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.RecordProfilePollFailure(ctx, "one", attempt, attempt, "network", "timeout")
		}()
	}
	wg.Wait()
	close(errs)
	winners, stale := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrProfilePollStale):
			stale++
		default:
			t.Fatal(err)
		}
	}
	h, err := s.ListProfileHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var one, two ProfileHealth
	for _, v := range h {
		if v.ProfileRef == "one" {
			one = v
		}
		if v.ProfileRef == "two" {
			two = v
		}
	}
	if winners != 1 || stale != workers-1 || one.ConsecutiveFailures != 1 || two.ConsecutiveFailures != 0 {
		t.Fatalf("winners=%d stale=%d one=%d two=%d", winners, stale, one.ConsecutiveFailures, two.ConsecutiveFailures)
	}
}

func TestProfileHealthCompletionIsOneShotAndChronological(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"one", "codex", "One", true, true}}); err != nil {
		t.Fatal(err)
	}
	attempt := time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC)
	if err := s.RecordProfilePollAttempt(ctx, "one", attempt); err != nil {
		t.Fatal(err)
	}
	for _, completed := range []time.Time{{}, attempt.Add(-time.Nanosecond)} {
		if err := s.RecordProfilePollSuccess(ctx, "one", attempt, completed); !errors.Is(err, ErrProfilePollStale) {
			t.Fatalf("invalid completion %s: %v", completed, err)
		}
	}

	errCh := make(chan error, 2)
	go func() { errCh <- s.RecordProfilePollSuccess(ctx, "one", attempt, attempt.Add(time.Second)) }()
	go func() {
		errCh <- s.RecordProfilePollFailure(ctx, "one", attempt, attempt.Add(2*time.Second), ProfileFailureNetwork, ProfileErrorTimeout)
	}()
	winners, stale := 0, 0
	for range 2 {
		err := <-errCh
		if err == nil {
			winners++
		} else if errors.Is(err, ErrProfilePollStale) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if winners != 1 || stale != 1 {
		t.Fatalf("winners=%d stale=%d", winners, stale)
	}
	if err := s.RecordProfilePollAttempt(ctx, "one", attempt); !errors.Is(err, ErrProfilePollStale) {
		t.Fatalf("completed attempt reopened: %v", err)
	}
	if err := s.RecordProfilePollFailure(ctx, "one", attempt, attempt.Add(3*time.Second), ProfileFailureNetwork, ProfileErrorTimeout); !errors.Is(err, ErrProfilePollStale) {
		t.Fatalf("second completion accepted: %v", err)
	}

	health, err := s.ListProfileHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	terminal := latestProfileTime(health[0].LastSuccessAt, health[0].LastFailureAt)
	if terminal == nil {
		t.Fatal("terminal outcome missing")
	}
	if err := s.RecordProfilePollAttempt(ctx, "one", terminal.Add(-time.Nanosecond)); !errors.Is(err, ErrProfilePollStale) {
		t.Fatalf("attempt before completion accepted: %v", err)
	}
}

func TestProfileHealthAbortRestoresLatestTerminalAndRollsBackMalformedState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"one", "codex", "One", true, true}, {"empty", "codex", "Empty", true, false}}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2030, 7, 13, 1, 0, 0, 0, time.UTC)
	if err := s.RecordProfilePollAttempt(ctx, "one", base); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordProfilePollSuccess(ctx, "one", base, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	failureAttempt := base.Add(2 * time.Second)
	if err := s.RecordProfilePollAttempt(ctx, "one", failureAttempt); err != nil {
		t.Fatal(err)
	}
	latestTerminal := base.Add(4 * time.Second)
	if err := s.RecordProfilePollFailure(ctx, "one", failureAttempt, latestTerminal, ProfileFailureNetwork, ProfileErrorTimeout); err != nil {
		t.Fatal(err)
	}
	pending := base.Add(5 * time.Second)
	if err := s.RecordProfilePollAttempt(ctx, "one", pending); err != nil {
		t.Fatal(err)
	}
	if err := s.AbortProfilePollAttempt(ctx, "one", pending); err != nil {
		t.Fatal(err)
	}

	emptyAttempt := base.Add(10 * time.Second)
	if err := s.RecordProfilePollAttempt(ctx, "empty", emptyAttempt); err != nil {
		t.Fatal(err)
	}
	if err := s.AbortProfilePollAttempt(ctx, "empty", emptyAttempt); err != nil {
		t.Fatal(err)
	}

	var oneAttempt, emptyLast sql.NullString
	var updated string
	if err := s.db.QueryRow(`select last_attempt_at,updated_at from profile_poll_health where profile_ref='one'`).Scan(&oneAttempt, &updated); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`select last_attempt_at from profile_poll_health where profile_ref='empty'`).Scan(&emptyLast); err != nil {
		t.Fatal(err)
	}
	if !oneAttempt.Valid || oneAttempt.String != formatTime(latestTerminal) || emptyLast.Valid {
		t.Fatalf("one=%v empty=%v", oneAttempt, emptyLast)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, updated)
	if err != nil || updatedAt.Before(pending) {
		t.Fatalf("updated=%q err=%v", updated, err)
	}

	malformedAttempt := base.Add(20 * time.Second)
	if _, err = s.db.Exec(`update profile_poll_health set last_attempt_at=?,last_success_at='broken' where profile_ref='one'`, formatTime(malformedAttempt)); err != nil {
		t.Fatal(err)
	}
	if err = s.AbortProfilePollAttempt(ctx, "one", malformedAttempt); err == nil {
		t.Fatal("malformed terminal timestamp accepted")
	}
	var after string
	if err = s.db.QueryRow(`select last_attempt_at from profile_poll_health where profile_ref='one'`).Scan(&after); err != nil || after != formatTime(malformedAttempt) {
		t.Fatalf("rollback attempt=%q err=%v", after, err)
	}
}
