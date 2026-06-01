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
