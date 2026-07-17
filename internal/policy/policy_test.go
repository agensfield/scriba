package policy

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/resetwatch"
)

func TestParseConfigIsStrictAndCurrentPresetIsIndependent(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{"preset":"current"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Rules) != 4 {
		t.Fatalf("current rules = %d", len(cfg.Rules))
	}
	cfg.Rules[0].Checkpoints[0] = 99
	if got := CurrentPreset().Rules[0].Checkpoints[0]; got != 20 {
		t.Fatalf("preset mutated: %d", got)
	}
	custom, err := ParseConfig([]byte(`{"preset":"custom","rules":[{"id":"x","kind":"remaining_checkpoint","windowKeys":["primary.weekly"],"checkpoints":[20,10]}]}`))
	if err != nil || len(custom.Rules) != 1 || custom.Preset != "" {
		t.Fatalf("custom config = %#v, %v", custom, err)
	}

	bad := []string{
		`{"preset":"future"}`,
		`{"preset":"current","unknown":true}`,
		`{"preset":"current","rules":[]}`,
		`{"rules":[{"id":"x","kind":"remaining_checkpoint","windowKeys":["weekly"],"checkpoints":[5,10]}]}`,
		`{"rules":[{"id":"x","kind":"grant_available","checkpoints":[1]}]}`,
	}
	for _, raw := range bad {
		if _, err := ParseConfig([]byte(raw)); err == nil {
			t.Errorf("accepted invalid config %s", raw)
		}
	}
}

func TestCurrentRemainingMatchesResetwatchCandidate(t *testing.T) {
	now := mustTime("2026-07-12T10:00:00Z")
	resetAt := mustTime("2026-07-12T14:00:00Z")
	used := 96.0
	legacyObs := resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: "acct"}, ObservedAt: now, Windows: []resetwatch.Window{{Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: resetAt}}}
	legacy := resetwatch.WarningCandidates(legacyObs)
	previous := map[StateKey]State{{RuleID: "current.remaining.primary", Subject: "primary.five_hour"}: {LastResetAt: resetAt}}
	got, err := Evaluate(CurrentPreset(), Input{ProviderID: "codex", AccountRef: "acct", ObservedAt: now, Windows: []WindowObservation{{Key: "primary.five_hour", Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: resetAt}}, Previous: previous})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 1 || len(legacy) != 1 {
		t.Fatalf("events policy=%#v legacy=%#v", got.Events, legacy)
	}
	e := got.Events[0]
	if e.ID != legacy[0].ID || e.Checkpoint != legacy[0].ThresholdRemaining || e.RemainingPercent != legacy[0].RemainingPercent {
		t.Fatalf("policy=%#v legacy=%#v", e, legacy[0])
	}
}

func TestRemainingCheckpointIgnoresResetClockJitter(t *testing.T) {
	now := time.Date(2026, 7, 17, 13, 27, 0, 0, time.UTC)
	resetAt := time.Date(2026, 7, 23, 4, 15, 55, 0, time.UTC)
	previous := map[StateKey]State{}
	evaluate := func(at, reset time.Time, used float64, bootstrap bool) Result {
		t.Helper()
		result, err := Evaluate(CurrentPreset(), Input{
			ProviderID: "codex", AccountRef: "acct", ObservedAt: at, Bootstrap: bootstrap,
			Windows:  []WindowObservation{{Key: "primary.weekly", Label: resetwatch.LabelWeeklyLimit, UsedPercent: &used, ResetAt: reset}},
			Previous: previous,
		})
		if err != nil {
			t.Fatal(err)
		}
		previous = result.States
		return result
	}

	if first := evaluate(now, resetAt, 94, true); len(first.Events) != 0 {
		t.Fatalf("bootstrap events=%+v", first.Events)
	}
	checkpoint := evaluate(now.Add(5*time.Minute), resetAt, 95, false)
	if len(checkpoint.Events) != 1 || !checkpoint.Events[0].ResetAt.Equal(resetAt) {
		t.Fatalf("checkpoint events=%+v", checkpoint.Events)
	}
	if forward := evaluate(now.Add(10*time.Minute), resetAt.Add(2*time.Second), 95, false); len(forward.Events) != 0 {
		t.Fatalf("forward jitter events=%+v", forward.Events)
	}
	back := evaluate(now.Add(15*time.Minute), resetAt, 96, false)
	if len(back.Events) != 0 {
		t.Fatalf("backward jitter events=%+v", back.Events)
	}
	state := back.States[StateKey{RuleID: "current.remaining.primary", Subject: "primary.weekly"}]
	if !state.StableResetAt.Equal(resetAt) || !slices.Contains(state.ReachedCheckpoints, 5) {
		t.Fatalf("state=%+v", state)
	}
}

func TestRemainingCheckpointRepairsLegacyJitterState(t *testing.T) {
	now := time.Date(2026, 7, 17, 13, 52, 0, 0, time.UTC)
	resetAt := time.Date(2026, 7, 23, 4, 15, 55, 0, time.UTC)
	used := 96.0
	key := StateKey{RuleID: "current.remaining.primary", Subject: "primary.weekly"}
	previous := map[StateKey]State{
		key: {
			LastResetAt:        resetAt.Add(2 * time.Second),
			LastObservedAt:     now.Add(-5 * time.Minute),
			LastUsedPercent:    floatPtr(95),
			ReachedCheckpoints: []int{5},
		},
	}
	result, err := Evaluate(CurrentPreset(), Input{
		ProviderID: "codex", AccountRef: "acct", ObservedAt: now,
		Windows:  []WindowObservation{{Key: "primary.weekly", Label: resetwatch.LabelWeeklyLimit, UsedPercent: &used, ResetAt: resetAt}},
		Previous: previous,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 0 {
		t.Fatalf("repair events=%+v", result.Events)
	}
	if state := result.States[key]; !state.StableResetAt.Equal(resetAt) || !slices.Contains(state.ReachedCheckpoints, 5) {
		t.Fatalf("repaired state=%+v", state)
	}
}

func TestCurrentGrantFixturesMatchLegacyIDs(t *testing.T) {
	now := mustTime("2026-07-12T10:00:00Z")
	g := GrantObservation{ID: "credit-2", Status: "available", Title: "Reset", GrantedAt: mustTime("2026-07-12T09:00:00Z"), ExpiresAt: mustTime("2026-07-15T09:00:00Z")}
	legacyObs := resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: "acct"}, ObservedAt: now, ResetGrants: resetwatch.ResetGrants{Credits: []resetwatch.ResetCredit{{ID: g.ID, Status: g.Status, Title: g.Title, GrantedAt: g.GrantedAt, ExpiresAt: g.ExpiresAt}}}}
	prev := map[StateKey]State{
		{RuleID: "current.grant.available", Subject: "acct"}: {KnownGrantIdentities: []string{"credit-1\x002026-07-01T00:00:00Z\x002026-07-20T00:00:00Z"}},
		{RuleID: "current.grant.expiry", Subject: g.ID}:      {},
	}
	got, err := Evaluate(CurrentPreset(), Input{ProviderID: "codex", AccountRef: "acct", ObservedAt: now, Grants: []GrantObservation{g}, Previous: prev})
	if err != nil {
		t.Fatal(err)
	}
	legacyGrant := resetwatch.ResetGrantEventCandidates(legacyObs)[0]
	legacyExpiry := resetwatch.GrantExpiryWarningCandidates(legacyObs)
	if len(got.Events) != 3 || len(legacyExpiry) != 2 {
		t.Fatalf("policy=%#v legacy expiry=%#v", got.Events, legacyExpiry)
	}
	if got.Events[0].ID != legacyGrant.ID {
		t.Fatalf("grant id %s != %s", got.Events[0].ID, legacyGrant.ID)
	}
	for i := range legacyExpiry {
		if got.Events[i+1].ID != legacyExpiry[i].ID || got.Events[i+1].Checkpoint != legacyExpiry[i].ThresholdDays {
			t.Fatalf("expiry=%#v legacy=%#v", got.Events[i+1], legacyExpiry[i])
		}
	}
}

func TestBootstrapSilencesEveryRuleAndSeedsDeterministicState(t *testing.T) {
	now := mustTime("2026-07-12T10:00:00Z")
	used := 96.0
	in := Input{ProviderID: "codex", AccountRef: "acct", ObservedAt: now, Bootstrap: true, Windows: []WindowObservation{{Key: "primary.five_hour", Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: now.Add(5 * time.Hour)}, {Key: "primary.weekly", Label: resetwatch.LabelWeeklyLimit, UsedPercent: &used, ResetAt: now.Add(7 * 24 * time.Hour), PeriodDuration: 7 * 24 * time.Hour}}, Grants: []GrantObservation{{ID: "g", Status: "available", ExpiresAt: now.Add(24 * time.Hour)}}}
	a, err := Evaluate(CurrentPreset(), in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Evaluate(CurrentPreset(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Events) != 0 {
		t.Fatalf("bootstrap emitted %#v", a.Events)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("nondeterministic results\na=%#v\nb=%#v", a, b)
	}
	for _, x := range a.Explanations {
		if x.Reason != ReasonBootstrap {
			t.Fatalf("unexpected explanation %#v", x)
		}
	}
}

func TestResetTransitionMatchesCurrentHeuristics(t *testing.T) {
	now := mustTime("2026-07-12T10:00:00Z")
	oldReset := mustTime("2026-07-13T10:00:00Z")
	nextReset := mustTime("2026-07-19T10:00:00Z")
	used := 40.0
	prevUsed := 34.0
	prev := map[StateKey]State{{RuleID: "current.reset.weekly", Subject: "primary.weekly"}: {StableResetAt: oldReset, LastResetAt: oldReset, LastObservedAt: now.Add(-time.Hour), LastUsedPercent: &prevUsed}}
	got, err := Evaluate(CurrentPreset(), Input{ProviderID: "codex", AccountRef: "acct", ObservedAt: now, Windows: []WindowObservation{{Key: "primary.weekly", Label: resetwatch.LabelWeeklyLimit, UsedPercent: &used, ResetAt: nextReset, PeriodDuration: 7 * 24 * time.Hour}}, Previous: prev})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 1 || got.Events[0].ResetKind != "early" {
		t.Fatalf("events=%#v", got.Events)
	}
	want := resetwatch.EventID("codex", "acct", resetwatch.LabelWeeklyLimit, nextReset)
	if got.Events[0].ID != want {
		t.Fatalf("id=%s want=%s", got.Events[0].ID, want)
	}
}

func TestCurrentResetGroupsSecondaryWeeklyTransitions(t *testing.T) {
	now := mustTime("2026-07-12T10:00:00Z")
	oldReset := mustTime("2026-07-13T10:00:00Z")
	nextReset := mustTime("2026-07-19T10:00:00Z")
	used := 40.0
	previous := map[StateKey]State{
		{RuleID: "current.reset.weekly", Subject: "primary.weekly"}: {StableResetAt: oldReset},
		{RuleID: "current.reset.weekly", Subject: "spark.weekly"}:   {StableResetAt: oldReset},
	}
	got, err := Evaluate(CurrentPreset(), Input{ProviderID: "codex", AccountRef: "acct", ObservedAt: now, Previous: previous, Windows: []WindowObservation{
		{Key: "primary.weekly", Label: resetwatch.LabelWeeklyLimit, UsedPercent: &used, ResetAt: nextReset},
		{Key: "spark.weekly", Label: resetwatch.LabelSparkWeekly, UsedPercent: &used, ResetAt: nextReset},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 1 || !reflect.DeepEqual(got.Events[0].SecondaryLegacyLabels, []string{resetwatch.LabelSparkWeekly}) {
		t.Fatalf("events=%#v", got.Events)
	}
}

func TestEvaluationJSONContainsNoWallClockOrNondeterministicFields(t *testing.T) {
	r, err := Evaluate(CurrentPreset(), Input{AccountRef: "acct", ObservedAt: mustTime("2026-01-01T00:00:00Z"), Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(r); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapThenSameObservationStaysSilent(t *testing.T) {
	now := mustTime("2026-07-12T10:00:00Z")
	used := 96.0
	in := Input{AccountRef: "acct", ObservedAt: now, Bootstrap: true, Windows: []WindowObservation{{Key: "primary.five_hour", Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: now.Add(5 * time.Hour)}}, Grants: []GrantObservation{{ID: "g1", Status: "available", ExpiresAt: now.Add(3 * 24 * time.Hour)}}}
	first, err := Evaluate(CurrentPreset(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.Bootstrap = false
	in.Previous = first.States
	in.ObservedAt = now.Add(time.Minute)
	second, err := Evaluate(CurrentPreset(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 0 || len(second.Events) != 0 {
		t.Fatalf("first=%#v second=%#v", first.Events, second.Events)
	}
}

func TestFirstWindowAppearanceAfterBootstrapIsSilent(t *testing.T) {
	now := mustTime("2026-07-12T10:00:00Z")
	first, err := Evaluate(CurrentPreset(), Input{AccountRef: "acct", ObservedAt: now, Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	first.States[StateKey{RuleID: "current.remaining.primary", Subject: "primary.five_hour"}] = State{}
	used := 90.0
	second, err := Evaluate(CurrentPreset(), Input{AccountRef: "acct", ObservedAt: now.Add(time.Minute), Previous: first.States, Windows: []WindowObservation{{Key: "primary.five_hour", Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: now.Add(5 * time.Hour)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 0 {
		t.Fatalf("first window appearance emitted %#v", second.Events)
	}
}

func TestGrantCountRotationAndPostBootstrapIncrease(t *testing.T) {
	now := mustTime("2026-07-12T10:00:00Z")
	g1 := GrantObservation{ID: "g1", Status: "available", GrantedAt: now.Add(-time.Hour), ExpiresAt: now.Add(10 * 24 * time.Hour)}
	base, err := Evaluate(CurrentPreset(), Input{AccountRef: "acct", ObservedAt: now, Bootstrap: true, Grants: []GrantObservation{g1}})
	if err != nil {
		t.Fatal(err)
	}
	g2 := GrantObservation{ID: "g2", Status: "available", GrantedAt: now, ExpiresAt: now.Add(11 * 24 * time.Hour)}
	rotation, err := Evaluate(CurrentPreset(), Input{AccountRef: "acct", ObservedAt: now.Add(time.Minute), Previous: base.States, Grants: []GrantObservation{g2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rotation.Events) != 0 {
		t.Fatalf("equal-count rotation emitted %#v", rotation.Events)
	}
	g3 := GrantObservation{ID: "g3", Status: "available", GrantedAt: now, ExpiresAt: now.Add(12 * 24 * time.Hour)}
	increase, err := Evaluate(CurrentPreset(), Input{AccountRef: "acct", ObservedAt: now.Add(2 * time.Minute), Previous: rotation.States, Grants: []GrantObservation{g2, g3}})
	if err != nil {
		t.Fatal(err)
	}
	if len(increase.Events) != 1 || increase.Events[0].Kind != EventGrantAvailable || increase.Events[0].Subject != "g3" {
		t.Fatalf("increase events %#v", increase.Events)
	}
}

func TestNewPostBootstrapGrantExpiryIsNotSuppressedByMissingSubjectState(t *testing.T) {
	now := mustTime("2026-07-12T10:00:00Z")
	g := GrantObservation{ID: "new-grant", Status: "available", GrantedAt: now, ExpiresAt: now.Add(2 * 24 * time.Hour)}
	accountKey := StateKey{RuleID: "current.grant.available", Subject: "acct"}
	r, err := Evaluate(CurrentPreset(), Input{AccountRef: "acct", ObservedAt: now, Previous: map[StateKey]State{accountKey: {LastObservedAt: now.Add(-time.Hour)}}, Grants: []GrantObservation{g}})
	if err != nil {
		t.Fatal(err)
	}
	var checkpoints []int
	for _, e := range r.Events {
		if e.Kind == EventGrantExpiryCheckpoint {
			checkpoints = append(checkpoints, e.Checkpoint)
		}
	}
	if !reflect.DeepEqual(checkpoints, []int{5, 3}) {
		t.Fatalf("expiry checkpoints %#v events=%#v", checkpoints, r.Events)
	}
}

func TestChangedGrantExpiryResetsCheckpointsAndFallbackMatchesResetwatch(t *testing.T) {
	now := mustTime("2026-07-12T10:00:00Z")
	g := GrantObservation{Title: "Reset", Status: "available", GrantedAt: now.Add(-time.Hour), ExpiresAt: now.Add(2 * 24 * time.Hour)}
	normalized := normalizeGrants([]GrantObservation{g})[0]
	legacy := resetwatch.ResetGrantEventCandidates(resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: "acct"}, ObservedAt: now, ResetGrants: resetwatch.ResetGrants{Credits: []resetwatch.ResetCredit{{Title: g.Title, Status: g.Status, GrantedAt: g.GrantedAt, ExpiresAt: g.ExpiresAt}}}})[0]
	if normalized.ID != legacy.CreditID {
		t.Fatalf("fallback %s != %s", normalized.ID, legacy.CreditID)
	}
	key := StateKey{RuleID: "current.grant.expiry", Subject: normalized.ID}
	prev := map[StateKey]State{key: {LastObservedAt: now.Add(-time.Hour), GrantExpiresAt: now.Add(10 * 24 * time.Hour), ReachedCheckpoints: []int{5, 3, 1}}}
	r, err := Evaluate(CurrentPreset(), Input{AccountRef: "acct", ObservedAt: now, Previous: prev, Grants: []GrantObservation{g}})
	if err != nil {
		t.Fatal(err)
	}
	var expiry []Event
	for _, e := range r.Events {
		if e.Kind == EventGrantExpiryCheckpoint {
			expiry = append(expiry, e)
		}
	}
	if len(expiry) != 2 || expiry[0].Checkpoint != 5 || expiry[1].Checkpoint != 3 {
		t.Fatalf("expiry events %#v", expiry)
	}
}

func TestStaleObservationsAndUnsafeDuplicatesAreRejectedOrIgnored(t *testing.T) {
	now := mustTime("2026-07-12T10:00:00Z")
	used := 99.0
	key := StateKey{RuleID: "current.remaining.primary", Subject: "primary.five_hour"}
	prev := map[StateKey]State{key: {LastObservedAt: now, LastResetAt: now.Add(5 * time.Hour)}}
	r, err := Evaluate(CurrentPreset(), Input{AccountRef: "acct", ObservedAt: now.Add(-time.Minute), Previous: prev, Windows: []WindowObservation{{Key: "primary.five_hour", Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: now.Add(5 * time.Hour)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Events) != 0 || r.Explanations[0].Reason != ReasonStaleObservation || !reflect.DeepEqual(r.States[key], prev[key]) {
		t.Fatalf("stale result %#v", r)
	}
	bad := []Input{
		{AccountRef: "acct", ObservedAt: now, Windows: []WindowObservation{{Key: "primary.weekly", Label: "Weekly limit"}, {Key: "primary.weekly", Label: "Weekly limit"}}},
		{AccountRef: "bad\x00ref", ObservedAt: now},
		{AccountRef: "acct", ObservedAt: now, Grants: []GrantObservation{{ID: "g", ExpiresAt: now.Add(time.Hour)}, {ID: "g", ExpiresAt: now.Add(time.Hour)}}},
	}
	for _, in := range bad {
		if _, err := Evaluate(CurrentPreset(), in); err == nil {
			t.Errorf("accepted %#v", in)
		}
	}
	if _, err := Evaluate(Config{Preset: PresetCurrent, Rules: []Rule{{ID: "x"}}}, Input{AccountRef: "acct", ObservedAt: now}); err == nil {
		t.Fatal("accepted current preset with rules")
	}
}

func TestGrantCountIncreaseEmitsEveryNewCandidateInResetwatchOrder(t *testing.T) {
	now := mustTime("2026-07-12T10:00:00Z")
	count := 2
	grants := []GrantObservation{
		{ID: "z", Status: "available", GrantedAt: now.Add(-time.Hour), ExpiresAt: now.Add(10 * 24 * time.Hour)},
		{ID: "a", Status: "available", GrantedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(11 * 24 * time.Hour)},
	}
	prev := map[StateKey]State{{RuleID: "current.grant.available", Subject: "acct"}: {AvailableGrantCount: 1, LastObservedAt: now.Add(-time.Hour)}}
	r, err := Evaluate(CurrentPreset(), Input{AccountRef: "acct", ObservedAt: now, AvailableCount: &count, Previous: prev, Grants: grants})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range r.Events {
		if e.Kind == EventGrantAvailable {
			got = append(got, e.Subject)
		}
	}
	if !reflect.DeepEqual(got, []string{"a", "z"}) {
		t.Fatalf("availability order %#v", got)
	}
}

func TestExpiryOrderingMatchesResetwatchAcrossGrants(t *testing.T) {
	now := mustTime("2026-07-12T10:00:00Z")
	grants := []GrantObservation{
		{ID: "b", Status: "available", ExpiresAt: now.Add(2 * 24 * time.Hour)},
		{ID: "a", Status: "available", ExpiresAt: now.Add(2 * 24 * time.Hour)},
		{ID: "early", Status: "available", ExpiresAt: now.Add(23 * time.Hour)},
	}
	r, err := Evaluate(CurrentPreset(), Input{AccountRef: "acct", ObservedAt: now, Previous: map[StateKey]State{}, Grants: grants})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range r.Events {
		if e.Kind == EventGrantExpiryCheckpoint {
			got = append(got, e.Subject+":"+fmt.Sprint(e.Checkpoint))
		}
	}
	warnings := resetwatch.GrantExpiryWarningCandidates(resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: "acct"}, ObservedAt: now, ResetGrants: resetwatch.ResetGrants{Credits: []resetwatch.ResetCredit{{ID: "b", Status: "available", ExpiresAt: grants[0].ExpiresAt}, {ID: "a", Status: "available", ExpiresAt: grants[1].ExpiresAt}, {ID: "early", Status: "available", ExpiresAt: grants[2].ExpiresAt}}}})
	var want []string
	for _, w := range warnings {
		want = append(want, w.CreditID+":"+fmt.Sprint(w.ThresholdDays))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestObservationWatermarkBlocksOlderAbsentSubjectReappearance(t *testing.T) {
	t1 := mustTime("2026-07-12T10:00:00Z")
	newer, err := Evaluate(CurrentPreset(), Input{AccountRef: "acct", ObservedAt: t1, Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	used := 99.0
	older, err := Evaluate(CurrentPreset(), Input{AccountRef: "acct", ObservedAt: t1.Add(-time.Hour), Previous: newer.States, Windows: []WindowObservation{{Key: "primary.five_hour", Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: t1.Add(4 * time.Hour)}}, Grants: []GrantObservation{{ID: "old", Status: "available", ExpiresAt: t1.Add(2 * 24 * time.Hour)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Events) != 0 {
		t.Fatalf("older reappearance emitted %#v", older.Events)
	}
	for _, x := range older.Explanations {
		if x.Subject == observationWatermarkSubject && x.Reason != ReasonStaleObservation {
			t.Fatalf("watermark explanation %#v", x)
		}
	}
}

func TestCustomDottedWindowAndConfigSafety(t *testing.T) {
	cfg := Config{Rules: []Rule{{ID: "custom.remaining", Kind: KindRemainingCheckpoint, WindowKeys: []string{"provider.team.weekly"}, Checkpoints: []int{10}}}}
	now := mustTime("2026-07-12T10:00:00Z")
	used := 95.0
	first, err := Evaluate(cfg, Input{AccountRef: "acct", ObservedAt: now, Bootstrap: true, Windows: []WindowObservation{{Key: "provider.team.weekly", Label: "Team weekly", UsedPercent: &used, ResetAt: now.Add(7 * 24 * time.Hour)}}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Evaluate(cfg, Input{AccountRef: "acct", ObservedAt: now.Add(time.Minute), Previous: first.States, Windows: []WindowObservation{{Key: "provider.team.weekly", Label: "Team weekly", UsedPercent: &used, ResetAt: now.Add(7 * 24 * time.Hour)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 0 {
		t.Fatalf("repeat emitted %#v", second.Events)
	}
	bad := Config{Rules: []Rule{{ID: "bad.reset", Kind: KindResetTransition, WindowKeys: []string{"same.key"}, SecondaryWindowKeys: []string{"same.key"}}}}
	if err := bad.Validate(); err == nil {
		t.Fatal("accepted overlapping window keys")
	}
	if int64(^uint(0)>>1) > maxJitterSeconds {
		huge := int(maxJitterSeconds + 1)
		bad = Config{Rules: []Rule{{ID: "bad.jitter", Kind: KindResetTransition, WindowKeys: []string{"x.y"}, ClockJitterSec: huge}}}
		if err := bad.Validate(); err == nil {
			t.Fatal("accepted overflowing jitter")
		}
	}
}

func TestInvalidPersistedStateRejected(t *testing.T) {
	now := mustTime("2026-07-12T10:00:00Z")
	negative := -1
	bad := []map[StateKey]State{
		{{RuleID: "current.grant.available", Subject: "acct"}: {AvailableGrantCount: -1}},
		{{RuleID: "current.remaining.primary", Subject: "primary.weekly"}: {LastUsedPercent: func() *float64 { v := math.NaN(); return &v }()}},
		{{RuleID: "current.grant.available", Subject: "acct"}: {KnownGrantIdentities: []string{"unsafe"}}},
		{{RuleID: "unknown.rule", Subject: "acct"}: {AvailableGrantCount: negative}},
	}
	for _, previous := range bad {
		if _, err := Evaluate(CurrentPreset(), Input{AccountRef: "acct", ObservedAt: now, Previous: previous}); err == nil {
			t.Errorf("accepted %#v", previous)
		}
	}
}

func mustTime(v string) time.Time {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		panic(err)
	}
	return t
}
