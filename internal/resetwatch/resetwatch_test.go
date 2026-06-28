package resetwatch

import (
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/model"
)

func TestFirstObservationSeedsBaselineWithoutEvent(t *testing.T) {
	obs := observation("2026-05-31T12:00:00Z", "2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z")
	decision := Decide(obs, nil, testOptions())
	if len(decision.Events) != 0 {
		t.Fatalf("expected no events, got %d", len(decision.Events))
	}
	state := stateFor(decision, LabelWeeklyLimit)
	if state.StableResetAt.Format(time.RFC3339) != "2026-06-06T21:00:00Z" {
		t.Fatalf("unexpected stable reset: %s", state.StableResetAt.Format(time.RFC3339))
	}
}

func TestEarlyWeeklyResetEmitsOnce(t *testing.T) {
	prev := seededState("2026-05-31T12:00:00Z", "2026-06-06T21:00:00Z")
	obs := observation("2026-06-02T12:00:00Z", "2026-06-09T12:00:00Z", "2026-06-02T17:00:00Z")
	decision := Decide(obs, prev, testOptions())
	if len(decision.Events) != 1 {
		t.Fatalf("expected one event, got %d", len(decision.Events))
	}
	event := decision.Events[0]
	if event.ResetKind != ResetKindEarly {
		t.Fatalf("expected early, got %s", event.ResetKind)
	}
	if event.PrimaryTriggerLabel != LabelWeeklyLimit {
		t.Fatalf("unexpected trigger: %s", event.PrimaryTriggerLabel)
	}
	if event.JokeID != "test-joke" {
		t.Fatalf("unexpected joke id: %s", event.JokeID)
	}
}

func TestLowUsageResetWindowDriftDoesNotEmit(t *testing.T) {
	prev := seededStateWithUsage("2026-06-04T00:27:48Z", "2026-06-11T00:27:48Z", 0)
	obs := observation("2026-06-04T00:32:50Z", "2026-06-11T00:32:50Z", "2026-06-04T05:32:50Z")
	*obs.Windows[0].UsedPercent = 0
	decision := Decide(obs, prev, testOptions())
	if len(decision.Events) != 0 {
		t.Fatalf("expected no drift event, got %#v", decision.Events)
	}
	state := stateFor(decision, LabelWeeklyLimit)
	if state.StableResetAt.Format(time.RFC3339) != "2026-06-11T00:32:50Z" {
		t.Fatalf("expected stable reset to advance with backend drift, got %s", state.StableResetAt.Format(time.RFC3339))
	}
}

func TestNearDueSyntheticZeroResetDoesNotEmitOrAdvanceStable(t *testing.T) {
	prev := seededStateWithUsage("2026-06-10T19:48:06Z", "2026-06-11T10:15:21Z", 24)
	obs := observation("2026-06-10T19:53:07Z", "2026-06-17T19:53:07Z", "2026-06-11T00:53:07Z")
	*obs.Windows[0].UsedPercent = 0
	weeklyPeriod := int64((7 * 24 * time.Hour) / time.Millisecond)
	obs.Windows[0].PeriodDurationMs = &weeklyPeriod
	decision := Decide(obs, prev, testOptions())
	if len(decision.Events) != 0 {
		t.Fatalf("expected no synthetic zero event, got %#v", decision.Events)
	}
	state := stateFor(decision, LabelWeeklyLimit)
	if state.StableResetAt.Format(time.RFC3339) != "2026-06-11T10:15:21Z" {
		t.Fatalf("synthetic zero advanced stable reset: %s", state.StableResetAt.Format(time.RFC3339))
	}
}

func TestConsecutiveNearDueSyntheticZeroDoesNotAdvanceStable(t *testing.T) {
	prev := seededStateWithUsage("2026-06-10T20:18:02Z", "2026-06-11T10:15:21Z", 0)
	obs := observation("2026-06-10T20:23:02Z", "2026-06-17T20:23:02Z", "2026-06-11T01:23:02Z")
	*obs.Windows[0].UsedPercent = 0
	weeklyPeriod := int64((7 * 24 * time.Hour) / time.Millisecond)
	obs.Windows[0].PeriodDurationMs = &weeklyPeriod
	decision := Decide(obs, prev, testOptions())
	if len(decision.Events) != 0 {
		t.Fatalf("expected no consecutive synthetic zero event, got %#v", decision.Events)
	}
	state := stateFor(decision, LabelWeeklyLimit)
	if state.StableResetAt.Format(time.RFC3339) != "2026-06-11T10:15:21Z" {
		t.Fatalf("consecutive synthetic zero advanced stable reset: %s", state.StableResetAt.Format(time.RFC3339))
	}
}

func TestUsageDropStillEmitsReset(t *testing.T) {
	prev := seededStateWithUsage("2026-06-04T00:22:47Z", "2026-06-07T16:39:20Z", 34)
	obs := observation("2026-06-04T00:27:48Z", "2026-06-11T00:27:48Z", "2026-06-04T05:27:48Z")
	*obs.Windows[0].UsedPercent = 0
	decision := Decide(obs, prev, testOptions())
	if len(decision.Events) != 1 {
		t.Fatalf("expected reset event after usage drop, got %#v", decision.Events)
	}
}

func TestScheduledWeeklyResetEmitsScheduled(t *testing.T) {
	prev := seededState("2026-05-31T12:00:00Z", "2026-06-06T21:00:00Z")
	obs := observation("2026-06-06T21:01:00Z", "2026-06-13T21:00:00Z", "2026-06-07T02:00:00Z")
	decision := Decide(obs, prev, testOptions())
	if len(decision.Events) != 1 {
		t.Fatalf("expected one event, got %d", len(decision.Events))
	}
	if decision.Events[0].ResetKind != ResetKindScheduled {
		t.Fatalf("expected scheduled, got %s", decision.Events[0].ResetKind)
	}
}

func TestBackendFlapBackwardsDoesNotDuplicate(t *testing.T) {
	prev := seededState("2026-06-02T12:00:00Z", "2026-06-09T12:00:00Z")
	obs := observation("2026-06-02T12:02:00Z", "2026-06-06T21:00:00Z", "2026-06-02T17:02:00Z")
	decision := Decide(obs, prev, testOptions())
	if len(decision.Events) != 0 {
		t.Fatalf("expected no event, got %d", len(decision.Events))
	}
	state := stateFor(decision, LabelWeeklyLimit)
	if state.StableResetAt.Format(time.RFC3339) != "2026-06-09T12:00:00Z" {
		t.Fatalf("stable reset moved backwards: %s", state.StableResetAt.Format(time.RFC3339))
	}
}

func TestSparkOnlyResetDoesNotPush(t *testing.T) {
	prev := seededState("2026-05-31T12:00:00Z", "2026-06-06T21:00:00Z")
	prev[StateKey("acct", LabelSparkWeekly)] = WindowState{AccountRef: "acct", Label: LabelSparkWeekly, StableResetAt: parseTime("2026-06-06T21:00:00Z"), LastSnapshotJSON: []byte(`old`)}
	obs := observation("2026-06-02T12:00:00Z", "2026-06-06T21:00:00Z", "2026-06-02T17:00:00Z")
	obs.Windows = append(obs.Windows, Window{Label: LabelSparkWeekly, ResetAt: parseTime("2026-06-09T12:00:00Z")})
	decision := Decide(obs, prev, testOptions())
	if len(decision.Events) != 0 {
		t.Fatalf("expected no event, got %d", len(decision.Events))
	}
}

func TestFiveHourOnlyResetDoesNotPush(t *testing.T) {
	prev := seededState("2026-05-31T12:00:00Z", "2026-06-06T21:00:00Z")
	prev[StateKey("acct", LabelFiveHour)] = WindowState{AccountRef: "acct", Label: LabelFiveHour, StableResetAt: parseTime("2026-05-31T17:00:00Z"), LastSnapshotJSON: []byte(`old`)}
	obs := observation("2026-05-31T13:00:00Z", "2026-06-06T21:00:00Z", "2026-05-31T18:00:00Z")
	decision := Decide(obs, prev, testOptions())
	if len(decision.Events) != 0 {
		t.Fatalf("expected no event, got %d", len(decision.Events))
	}
}

func TestPrimaryWeeklyGroupsSecondaryWindows(t *testing.T) {
	prev := seededState("2026-05-31T12:00:00Z", "2026-06-06T21:00:00Z")
	prev[StateKey("acct", LabelSparkWeekly)] = WindowState{AccountRef: "acct", Label: LabelSparkWeekly, StableResetAt: parseTime("2026-06-06T21:00:00Z"), LastSnapshotJSON: []byte(`old`)}
	obs := observation("2026-06-02T12:00:00Z", "2026-06-09T12:00:00Z", "2026-06-02T17:00:00Z")
	obs.Windows = append(obs.Windows, Window{Label: LabelSparkWeekly, ResetAt: parseTime("2026-06-09T12:00:00Z")})
	decision := Decide(obs, prev, testOptions())
	if len(decision.Events) != 1 {
		t.Fatalf("expected one event, got %d", len(decision.Events))
	}
	if len(decision.Events[0].SecondaryTriggerLabels) != 1 || decision.Events[0].SecondaryTriggerLabels[0] != LabelSparkWeekly {
		t.Fatalf("unexpected secondary labels: %#v", decision.Events[0].SecondaryTriggerLabels)
	}
}

func TestFromMetricLinesKeepsProgressWindows(t *testing.T) {
	used := 42.5
	period := int64(604800000)
	windows := FromMetricLines([]model.MetricLine{
		{Type: "text", Label: "Plan", Text: "Plus"},
		{Type: "progress", Label: LabelWeeklyLimit, Used: &used, ResetsAt: "2026-06-06T21:00:00.123Z", PeriodDurationMs: &period},
		{Type: "progress", Label: LabelFiveHour, ResetsAt: "not-time"},
	})
	if len(windows) != 1 {
		t.Fatalf("expected one parsed window, got %d", len(windows))
	}
	if windows[0].Label != LabelWeeklyLimit {
		t.Fatalf("unexpected label: %s", windows[0].Label)
	}
	if windows[0].UsedPercent == nil || *windows[0].UsedPercent != used {
		t.Fatalf("unexpected used percent: %#v", windows[0].UsedPercent)
	}
	if windows[0].PeriodDurationMs == nil || *windows[0].PeriodDurationMs != period {
		t.Fatalf("unexpected period: %#v", windows[0].PeriodDurationMs)
	}
	if windows[0].ResetAt.Format(time.RFC3339Nano) != "2026-06-06T21:00:00.123Z" {
		t.Fatalf("unexpected reset: %s", windows[0].ResetAt.Format(time.RFC3339Nano))
	}
}

func TestWarningCandidatesUseMostSevereCheckpointPerWindow(t *testing.T) {
	obs := observation("2026-05-31T12:00:00Z", "2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z")
	weeklyUsed := 82.0
	fiveUsed := 96.0
	obs.Windows[0].UsedPercent = &weeklyUsed
	obs.Windows[1].UsedPercent = &fiveUsed
	warnings := WarningCandidates(obs)
	if len(warnings) != 2 {
		t.Fatalf("expected two warnings, got %d", len(warnings))
	}
	if warnings[0].Label != LabelFiveHour || warnings[0].ThresholdRemaining != 5 || warnings[0].RemainingPercent != 4 {
		t.Fatalf("unexpected 5h warning: %#v", warnings[0])
	}
	if warnings[1].Label != LabelWeeklyLimit || warnings[1].ThresholdRemaining != 20 || warnings[1].RemainingPercent != 18 {
		t.Fatalf("unexpected weekly warning: %#v", warnings[1])
	}
}

func TestWarningCandidatesSkipComfortableUsage(t *testing.T) {
	obs := observation("2026-05-31T12:00:00Z", "2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z")
	weeklyUsed := 50.0
	fiveUsed := 79.0
	obs.Windows[0].UsedPercent = &weeklyUsed
	obs.Windows[1].UsedPercent = &fiveUsed
	warnings := WarningCandidates(obs)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %#v", warnings)
	}
}

func TestResetGrantsFromSnapshotJSONKeepsPerCreditExpiry(t *testing.T) {
	grants := ResetGrantsFromSnapshotJSON([]byte(`{
		"lines": [
			{"type":"amount","label":"Reset grants","value":2},
			{"type":"text","label":"Grant expiry","value":"2026-07-13T01:20:48Z"}
		],
		"resetCredits": [
			{"id":"redeemed","status":"redeemed","expiresAt":"2026-07-01T00:00:00Z"},
			{"id":"credit_2","status":"available","title":"Rate limit reset","grantedAt":"2026-06-13T01:20:48Z","expiresAt":"2026-07-13T01:20:48Z"},
			{"id":"credit_1","status":"available","title":"Rate limit reset","grantedAt":"2026-06-12T01:20:48Z","expiresAt":"2026-07-12T01:20:48Z"}
		]
	}`))
	if grants.AvailableCount == nil || *grants.AvailableCount != 2 {
		t.Fatalf("unexpected count: %#v", grants.AvailableCount)
	}
	if got := grants.ExpiresAt.Format(time.RFC3339); got != "2026-07-12T01:20:48Z" {
		t.Fatalf("unexpected earliest expiry: %s", got)
	}
	if len(grants.Credits) != 2 || grants.Credits[0].ID != "credit_2" || grants.Credits[1].ID != "credit_1" {
		t.Fatalf("unexpected credits: %#v", grants.Credits)
	}
}

func TestGrantExpiryWarningCandidatesUsePerCreditThresholds(t *testing.T) {
	obs := observation("2026-06-01T12:00:00Z", "2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z")
	obs.ResetGrants = ResetGrants{
		Credits: []ResetCredit{
			{ID: "credit_1", Status: "available", Title: "Rate limit reset", ExpiresAt: parseTime("2026-06-06T11:00:00Z")},
			{ID: "credit_2", Status: "available", Title: "Rate limit reset", ExpiresAt: parseTime("2026-06-04T11:00:00Z")},
			{ID: "credit_3", Status: "redeemed", Title: "Rate limit reset", ExpiresAt: parseTime("2026-06-02T11:00:00Z")},
		},
	}
	warnings := GrantExpiryWarningCandidates(obs)
	if len(warnings) != 3 {
		t.Fatalf("expected three warnings, got %#v", warnings)
	}
	if warnings[0].CreditID != "credit_2" || warnings[0].ThresholdDays != 5 {
		t.Fatalf("unexpected first warning: %#v", warnings[0])
	}
	if warnings[1].CreditID != "credit_2" || warnings[1].ThresholdDays != 3 {
		t.Fatalf("unexpected second warning: %#v", warnings[1])
	}
	if warnings[2].CreditID != "credit_1" || warnings[2].ThresholdDays != 5 {
		t.Fatalf("unexpected third warning: %#v", warnings[2])
	}
	if warnings[0].ID == warnings[1].ID || warnings[1].ID == warnings[2].ID {
		t.Fatalf("warning IDs should be per credit and threshold: %#v", warnings)
	}
}

func TestCatalogJokeChooserIsDeterministicAndToneAware(t *testing.T) {
	event := Event{ID: "reset_test"}
	chooser := CatalogJokeChooser{
		Tone: "spicy",
		Jokes: []Joke{
			{ID: "normal", Tone: "normal", Text: "normal"},
			{ID: "spicy-a", Tone: "spicy", Text: "spicy a"},
			{ID: "spicy-b", Tone: "spicy", Text: "spicy b"},
		},
	}
	first := chooser.Choose(event)
	second := chooser.Choose(event)
	if first != second {
		t.Fatalf("choice changed: %s != %s", first, second)
	}
	if first != "spicy-a" && first != "spicy-b" {
		t.Fatalf("expected spicy joke, got %s", first)
	}
}

func TestResetGrantEventCandidatesUseAvailableCredits(t *testing.T) {
	obs := observation("2026-06-01T12:00:00Z", "2026-06-06T21:00:00Z", "2026-05-31T17:00:00Z")
	count := 2
	obs.ResetGrants = ResetGrants{
		AvailableCount: &count,
		Credits: []ResetCredit{
			{ID: "credit_2", Status: "available", Title: "Full reset", ResetType: "codex_rate_limits", GrantedAt: parseTime("2026-06-13T01:20:48Z"), ExpiresAt: parseTime("2026-07-13T01:20:48Z")},
			{ID: "redeemed", Status: "redeemed", Title: "Full reset", GrantedAt: parseTime("2026-06-12T01:20:48Z"), ExpiresAt: parseTime("2026-07-12T01:20:48Z")},
			{ID: "credit_1", Status: "available", Title: "Full reset", ResetType: "codex_rate_limits", GrantedAt: parseTime("2026-06-12T01:20:48Z"), ExpiresAt: parseTime("2026-07-12T01:20:48Z")},
		},
	}
	events := ResetGrantEventCandidates(obs)
	if len(events) != 2 {
		t.Fatalf("expected two grant events, got %#v", events)
	}
	if events[0].CreditID != "credit_1" || events[1].CreditID != "credit_2" {
		t.Fatalf("expected events ordered by grant time, got %#v", events)
	}
	if events[0].AvailableCount != 2 || events[0].ResetType != "codex_rate_limits" {
		t.Fatalf("unexpected event metadata: %#v", events[0])
	}
	if events[0].ID == events[1].ID {
		t.Fatalf("event IDs should be per credit: %#v", events)
	}
}

func observation(observedAt, weeklyReset, fiveReset string) Observation {
	weekly := 51.0
	five := 96.0
	return Observation{
		ProviderID: "codex",
		Account:    Account{Ref: "acct", Label: "personal"},
		ObservedAt: parseTime(observedAt),
		Windows: []Window{
			{Label: LabelWeeklyLimit, UsedPercent: &weekly, ResetAt: parseTime(weeklyReset)},
			{Label: LabelFiveHour, UsedPercent: &five, ResetAt: parseTime(fiveReset)},
		},
		SnapshotJSON: []byte(`{"snapshot":true}`),
	}
}

func seededState(observedAt, weeklyReset string) map[string]WindowState {
	return map[string]WindowState{
		StateKey("acct", LabelWeeklyLimit): {
			AccountRef:       "acct",
			Label:            LabelWeeklyLimit,
			StableResetAt:    parseTime(weeklyReset),
			LastSeenResetAt:  parseTime(weeklyReset),
			LastObservedAt:   parseTime(observedAt),
			LastSnapshotJSON: []byte(`{"snapshot":"old"}`),
		},
	}
}

func seededStateWithUsage(observedAt, weeklyReset string, used float64) map[string]WindowState {
	state := seededState(observedAt, weeklyReset)
	key := StateKey("acct", LabelWeeklyLimit)
	weekly := state[key]
	weekly.LastSnapshotJSON = snapshotWithWeeklyUsage(weeklyReset, used)
	state[key] = weekly
	return state
}

func snapshotWithWeeklyUsage(weeklyReset string, used float64) []byte {
	limit := 100.0
	return SnapshotJSON(struct {
		Lines []model.MetricLine `json:"lines"`
	}{
		Lines: []model.MetricLine{
			{Type: "progress", Label: LabelWeeklyLimit, Used: &used, Limit: &limit, ResetsAt: weeklyReset},
		},
	})
}

func stateFor(decision Decision, label string) WindowState {
	for _, state := range decision.States {
		if state.Label == label {
			return state
		}
	}
	return WindowState{}
}

func testOptions() Options {
	return Options{ClockJitter: time.Minute, DueJitter: 10 * time.Minute, JokeChooser: JokeChooserFunc(func(Event) string { return "test-joke" })}
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}
