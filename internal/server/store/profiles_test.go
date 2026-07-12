package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func profileFailureSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	var v []any
	for _, q := range []string{
		`select count(*)||':'||coalesce(group_concat(account_ref||provider_id||label||email||plan||updated_at,'|'),'') from accounts`,
		`select count(*)||':'||coalesce(group_concat(profile_ref||provider_id||account_ref||is_current||first_seen_at||last_seen_at,'|'),'') from profile_accounts`,
		`select count(*) from limit_observations`, `select count(*) from policy_states`, `select count(*) from policy_events`, `select count(*) from reset_events`, `select count(*) from limit_warning_events`, `select count(*) from reset_grant_warning_events`, `select count(*) from reset_grant_events`, `select count(*) from notification_outbox`,
		`select coalesce(group_concat(profile_ref||provider_id||label||enabled||is_default||created_at||updated_at,'|'),'') from (select * from profiles order by profile_ref)`,
		`select coalesce(group_concat(profile_ref||coalesce(last_attempt_at,'')||coalesce(last_success_at,'')||coalesce(last_failure_at,'')||consecutive_failures||failure_kind||last_error_code||alert_state||updated_at,'|'),'') from (select * from profile_poll_health order by profile_ref)`} {
		var x any
		if err := s.db.QueryRow(q).Scan(&x); err != nil {
			t.Fatal(err)
		}
		v = append(v, x)
	}
	return fmt.Sprint(v)
}

func TestSyncProfilesDefaultDisableAndHistoryPreservation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"one", "codex", "One", true, true}, {"two", "codex", "Two", true, false}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"two", "codex", "Two renamed", true, true}}); err != nil {
		t.Fatal(err)
	}
	var enabled, def int
	if err := s.db.QueryRow(`select enabled,is_default from profiles where profile_ref='one'`).Scan(&enabled, &def); err != nil || enabled != 0 || def != 0 {
		t.Fatalf("omitted=%d/%d err=%v", enabled, def, err)
	}
	for _, bad := range [][]ProfileSpec{nil, {{"a", "codex", "A", true, false}}, {{"a", "codex", "A", true, true}, {"b", "codex", "B", true, true}}, {{"a", "codex", "A", false, true}}} {
		if err := s.SyncProfiles(ctx, bad); err == nil {
			t.Fatal("invalid profile set accepted")
		}
	}
}

func TestConcurrentProfilesClaimOneAccountHasOneAtomicWinner(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"one", "codex", "One", true, true}, {"two", "codex", "Two", true, false}}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, profile := range []string{"one", "two"} {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			<-start
			in := pollInput(codexPollObservation(base, 81), "telegram:42")
			in.ProfileRef = p
			_, err := s.ApplyCodexPoll(ctx, in)
			results <- err
		}(profile)
	}
	close(start)
	wg.Wait()
	close(results)
	success, owned := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrProfileAccountOwned) {
			owned++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || owned != 1 {
		t.Fatalf("success=%d owned=%d", success, owned)
	}
	var mappings, obs, outbox int
	if err := s.db.QueryRow(`select (select count(*) from profile_accounts where account_ref='acct'),(select count(*) from limit_observations where account_ref='acct'),(select count(*) from notification_outbox where account_ref='acct')`).Scan(&mappings, &obs, &outbox); err != nil {
		t.Fatal(err)
	}
	if mappings != 1 || obs != 1 {
		t.Fatalf("mappings=%d obs=%d outbox=%d", mappings, obs, outbox)
	}
}

func TestSyncProfilesRacesPollWithLegalSerialOutcome(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"one", "codex", "One", true, true}, {"two", "codex", "Two", true, false}}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		in := pollInput(codexPollObservation(time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC), 70), "")
		in.ProfileRef = "one"
		_, err := s.ApplyCodexPoll(ctx, in)
		errs <- err
	}()
	go func() { <-start; errs <- s.SyncProfiles(ctx, []ProfileSpec{{"two", "codex", "Two", true, true}}) }()
	close(start)
	var pollErr error
	for range 2 {
		err := <-errs
		if err != nil {
			pollErr = err
		}
	}
	if pollErr != nil && !errors.Is(pollErr, ErrProfileDisabled) {
		t.Fatal(pollErr)
	}
	var defaults, enabled, obs int
	if err := s.db.QueryRow(`select (select count(*) from profiles where enabled=1 and is_default=1),(select enabled from profiles where profile_ref='one'),(select count(*) from limit_observations where account_ref='acct')`).Scan(&defaults, &enabled, &obs); err != nil {
		t.Fatal(err)
	}
	if defaults != 1 || enabled != 0 || (obs != 0 && obs != 1) {
		t.Fatalf("defaults=%d enabled=%d obs=%d", defaults, enabled, obs)
	}
}

func TestProfilePollRotationOwnershipReplayAndAttribution(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"one", "codex", "One", true, true}, {"two", "codex", "Two", true, false}}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	first := pollInput(codexPollObservation(base, 70), "telegram:42")
	first.ProfileRef = "one"
	if _, err := s.ApplyCodexPoll(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCodexPoll(ctx, first); err != nil {
		t.Fatal(err)
	}
	conflict := first
	conflict.ProfileRef = "two"
	if _, err := s.ApplyCodexPoll(ctx, conflict); !errors.Is(err, ErrProfileAccountOwned) {
		t.Fatalf("ownership err=%v", err)
	}
	rotated := pollInput(codexPollObservation(base.Add(time.Hour), 81), "telegram:42")
	rotated.ProfileRef = "one"
	rotated.Observation.Account.Ref = "acct-new"
	if _, err := s.ApplyCodexPoll(ctx, rotated); err != nil {
		t.Fatal(err)
	}
	// Replaying the older account must validate ownership without stealing current.
	if _, err := s.ApplyCodexPoll(ctx, first); err != nil {
		t.Fatal(err)
	}
	var oldCurrent, newCurrent int
	if err := s.db.QueryRow(`select (select is_current from profile_accounts where account_ref='acct'),(select is_current from profile_accounts where account_ref='acct-new')`).Scan(&oldCurrent, &newCurrent); err != nil || oldCurrent != 0 || newCurrent != 1 {
		t.Fatalf("rotation=%d/%d err=%v", oldCurrent, newCurrent, err)
	}
	var unattributed int
	if err := s.db.QueryRow(`select count(*) from notification_outbox where account_ref is not null and profile_ref<>'one'`).Scan(&unattributed); err != nil || unattributed != 0 {
		t.Fatalf("unattributed=%d err=%v", unattributed, err)
	}
}

func TestLoadLatestObservationForProfileIsolatesCurrentAccount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"one", "codex", "One", true, true}, {"two", "codex", "Two", true, false}}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 13, 2, 0, 0, 0, time.UTC)
	one := pollInput(codexPollObservation(base, 20), "")
	one.ProfileRef = "one"
	one.Observation.Account.Ref = "acct-one"
	if _, err := s.ApplyCodexPoll(ctx, one); err != nil {
		t.Fatal(err)
	}
	two := pollInput(codexPollObservation(base.Add(time.Hour), 80), "")
	two.ProfileRef = "two"
	two.Observation.Account.Ref = "acct-two"
	if _, err := s.ApplyCodexPoll(ctx, two); err != nil {
		t.Fatal(err)
	}

	selected, ok, err := s.LoadLatestObservationForProfile(ctx, "one")
	if err != nil || !ok || selected.Account.Ref != "acct-one" || !selected.ObservedAt.Equal(base) {
		t.Fatalf("selected=%+v ok=%v err=%v", selected, ok, err)
	}
	if _, ok, err := s.LoadLatestObservationForProfile(ctx, "missing"); err != nil || ok {
		t.Fatalf("missing ok=%v err=%v", ok, err)
	}
	if _, _, err := s.LoadLatestObservationForProfile(ctx, "INVALID"); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("invalid err=%v", err)
	}
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"two", "codex", "Two", true, true}}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.LoadLatestObservationForProfile(ctx, "one"); err != nil || ok {
		t.Fatalf("disabled ok=%v err=%v", ok, err)
	}
}

func TestProfileBindingCurrentIsMonotonicAndDeterministic(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"one", "codex", "One", true, true}}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, a := range []string{"z", "a", "old"} {
		if _, err = tx.Exec(`insert into accounts values(?,'codex','','','','2026-07-13T00:00:00Z')`, a); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	if err = bindProfileAccount(ctx, tx, "one", "codex", "z", at); err != nil {
		t.Fatal(err)
	}
	if err = bindProfileAccount(ctx, tx, "one", "codex", "a", at); err != nil {
		t.Fatal(err)
	}
	if err = bindProfileAccount(ctx, tx, "one", "codex", "old", at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	var current string
	if err = tx.QueryRow(`select account_ref from profile_accounts where is_current=1`).Scan(&current); err != nil || current != "a" {
		t.Fatalf("current=%q err=%v", current, err)
	}
}

func TestProfileBindingParsesFractionalTimestampWidthsExactly(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"one", "codex", "One", true, true}}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, x := range []struct{ ref, raw string }{{"exact", "2026-07-13T00:00:00Z"}, {"hundredth", "2026-07-13T00:00:00.01Z"}, {"tenth", "2026-07-13T00:00:00.1Z"}, {"nano", "2026-07-13T00:00:00.100000001Z"}} {
		if _, err = tx.Exec(`insert into accounts values(?,'codex','','','',?)`, x.ref, x.raw); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`insert into profile_accounts values('one','codex',?,0,?,?)`, x.ref, x.raw, x.raw); err != nil {
			t.Fatal(err)
		}
	}
	if err = bindProfileAccount(ctx, tx, "one", "codex", "exact", time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	var current string
	if err = tx.QueryRow(`select account_ref from profile_accounts where is_current=1`).Scan(&current); err != nil || current != "nano" {
		t.Fatalf("current=%q err=%v", current, err)
	}
}

func TestEnqueueOutboxProfileErrorClassification(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"one", "codex", "One", true, true}, {"off", "codex", "Off", false, false}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`insert into accounts values('acct','codex','','','','2026-07-13T00:00:00Z');insert into accounts values('free','codex','','','','2026-07-13T00:00:00Z');insert into profile_accounts values('one','codex','acct',1,'2026-07-13T00:00:00Z','2026-07-13T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, profile, account string
		want                   error
	}{{"invalid", "BAD", "acct", ErrInvalidProfile}, {"missing", "missing", "acct", ErrProfileMissing}, {"disabled", "off", "acct", ErrProfileDisabled}, {"unbound", "one", "free", ErrProfileAccountUnbound}} {
		t.Run(tc.name, func(t *testing.T) {
			tx, e := s.db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback() }()
			e = EnqueueOutbox(ctx, tx, OutboxEnqueue{EventKind: "reset", Source: "test", ProfileRef: tc.profile, AccountRef: tc.account, EventID: tc.name, Target: "t", PayloadVersion: 1, PayloadJSON: `{}`}, time.Now())
			if !errors.Is(e, tc.want) {
				t.Fatalf("err=%v want=%v", e, tc.want)
			}
			var n int
			if e = tx.QueryRow(`select count(*) from notification_outbox where event_id=?`, tc.name).Scan(&n); e != nil || n != 0 {
				t.Fatalf("rows=%d err=%v", n, e)
			}
		})
	}
	// Existing enabled profile with ownership elsewhere classifies as owned.
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"one", "codex", "One", true, true}, {"two", "codex", "Two", true, false}}); err != nil {
		t.Fatal(err)
	}
	tx, _ := s.db.BeginTx(ctx, nil)
	defer func() { _ = tx.Rollback() }()
	err := EnqueueOutbox(ctx, tx, OutboxEnqueue{EventKind: "reset", Source: "test", ProfileRef: "two", AccountRef: "acct", EventID: "owned", Target: "t", PayloadVersion: 1, PayloadJSON: `{}`}, time.Now())
	if !errors.Is(err, ErrProfileAccountOwned) {
		t.Fatalf("owned err=%v", err)
	}
}

func TestProfileErrorsAreStableAndDoNotEchoInvalidRef(t *testing.T) {
	s := openTestStore(t)
	bad := pollInput(codexPollObservation(time.Now().UTC(), 70), "")
	bad.ProfileRef = "SECRET/BAD"
	_, err := s.ApplyCodexPoll(context.Background(), bad)
	if !errors.Is(err, ErrInvalidProfile) || err.Error() != "invalid profile" {
		t.Fatalf("err=%v", err)
	}
	for _, ref := range []string{"UPPER", "under_score", "leading-", "-trailing", "double--dash"} {
		if err = s.SyncProfiles(context.Background(), []ProfileSpec{{ref, "codex", "Invalid", true, true}}); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("ref=%q sync err=%v", ref, err)
		}
	}
}

func TestProfileFailuresLeaveCompleteStoreSnapshot(t *testing.T) {
	base := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	for name, prepare := range map[string]func(*Store) error{
		"missing": func(s *Store) error {
			in := pollInput(codexPollObservation(base, 70), "")
			in.ProfileRef = "missing"
			_, err := s.ApplyCodexPoll(context.Background(), in)
			return err
		},
		"disabled": func(s *Store) error {
			ctx := context.Background()
			if err := s.SyncProfiles(ctx, []ProfileSpec{{"other", "codex", "Other", true, true}}); err != nil {
				return err
			}
			in := pollInput(codexPollObservation(base, 70), "")
			in.ProfileRef = "default"
			_, err := s.ApplyCodexPoll(ctx, in)
			return err
		},
		"provider mismatch": func(s *Store) error {
			ctx := context.Background()
			if err := s.SyncProfiles(ctx, []ProfileSpec{{"other", "other", "Other", true, true}}); err != nil {
				return err
			}
			in := pollInput(codexPollObservation(base, 70), "")
			in.ProfileRef = "other"
			_, err := s.ApplyCodexPoll(ctx, in)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := openTestStore(t)
			if name != "missing" { // capture after setup, before failing poll
				ctx := context.Background()
				if name == "disabled" {
					if err := s.SyncProfiles(ctx, []ProfileSpec{{"other", "codex", "Other", true, true}}); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := s.SyncProfiles(ctx, []ProfileSpec{{"other", "other", "Other", true, true}}); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := profileFailureSnapshot(t, s)
			var err error
			if name == "missing" {
				err = prepare(s)
			} else {
				in := pollInput(codexPollObservation(base, 70), "")
				if name == "disabled" {
					in.ProfileRef = "default"
				} else {
					in.ProfileRef = "other"
				}
				_, err = s.ApplyCodexPoll(context.Background(), in)
			}
			if err == nil {
				t.Fatal("failure succeeded")
			}
			if after := profileFailureSnapshot(t, s); after != before {
				t.Fatal("failure changed store")
			}
		})
	}
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"one", "codex", "One", true, true}}); err != nil {
		t.Fatal(err)
	}
	before := profileFailureSnapshot(t, s)
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"one", "other", "One", true, true}}); !errors.Is(err, ErrProfileProviderMismatch) {
		t.Fatalf("err=%v", err)
	}
	if profileFailureSnapshot(t, s) != before {
		t.Fatal("provider mutation changed store")
	}
}

func TestSyncProfilesRefusesProviderChangeWithHistory(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p := pollInput(codexPollObservation(time.Now().UTC(), 70), "")
	if _, err := s.ApplyCodexPoll(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncProfiles(ctx, []ProfileSpec{{"default", "other", "Default", true, true}}); err == nil {
		t.Fatal("provider identity changed with history")
	}
}
