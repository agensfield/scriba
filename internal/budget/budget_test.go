package budget

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestEvaluateGolden(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	used, old := 20.0, 10.0
	in := Input{ProviderID: " codex ", HistoryState: HistoryAvailable,
		Observation: Observation{ObservedAt: now.Add(-5 * time.Minute), Windows: []WindowObservation{{Key: " FIVE_HOUR ", Label: "5h", UsedPercent: &used, ResetAt: now.Add(2 * time.Hour), PeriodDuration: 5 * time.Hour}}},
		History:     []Observation{{ObservedAt: now.Add(-65 * time.Minute), Windows: []WindowObservation{{Key: "five_hour", UsedPercent: &old, ResetAt: now.Add(2*time.Hour + time.Minute)}}}},
	}
	r := Evaluate(in, now)
	w := r.Windows[0]
	if r.SchemaVersion != "scriba.budget.v1" || r.ProviderID != "codex" || w.Key != "five_hour" {
		t.Fatalf("bad envelope: %#v", r)
	}
	if *w.RecentBurnPercentPointsPerHour != 10 || *w.PaceBurnPercentPointsPerHour != 10 {
		t.Fatalf("bad recent/pace: %#v", w)
	}
	if *w.SafeHourlyAllowancePercentPoints != 40 || *w.SafeDailyAllowancePercentPoints != 80 || w.Risk != "low" || w.Freshness != "fresh" || w.Confidence != "medium" {
		t.Fatalf("bad categories: %#v", w)
	}
	if *w.TemporalMarginMS <= 0 {
		t.Fatalf("expected positive safe temporal margin: %#v", w)
	}
	if _, err := json.Marshal(r); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluateRejectsUnboundedProjection(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	used := 0.001
	r := Evaluate(Input{HistoryState: HistoryEmpty, Observation: Observation{ObservedAt: now, Windows: []WindowObservation{{Key: "weekly", UsedPercent: &used, ResetAt: now.Add(24 * time.Hour), PeriodDuration: 7 * 24 * time.Hour}}}}, now)
	w := r.Windows[0]
	if w.ProjectedExhaustionAt != nil || w.TemporalMarginMS != nil || w.Risk != "unknown" {
		t.Fatalf("unbounded projection must remain unavailable: %#v", w)
	}
}

func TestFreshnessBoundaries(t *testing.T) {
	now := time.Unix(10000, 0)
	cases := []struct {
		age  time.Duration
		want string
	}{{-time.Minute, "fresh"}, {-time.Minute - time.Nanosecond, "unknown"}, {10 * time.Minute, "fresh"}, {10*time.Minute + time.Nanosecond, "aging"}, {30 * time.Minute, "aging"}, {30*time.Minute + time.Nanosecond, "stale"}}
	for _, tc := range cases {
		if got := freshness(now.Add(-tc.age), now); got != tc.want {
			t.Errorf("age %v: %s", tc.age, got)
		}
	}
}

func TestDeterministicSortAndNoNonFinite(t *testing.T) {
	now := time.Unix(10000, 0)
	v := 20.0
	in := Input{HistoryState: HistoryEmpty, Observation: Observation{ObservedAt: now, Windows: []WindowObservation{{Key: " Z ", Label: "b", UsedPercent: &v, ResetAt: now.Add(time.Hour), PeriodDuration: 2 * time.Hour}, {Key: "a", Label: "a", UsedPercent: &v, ResetAt: now.Add(time.Hour), PeriodDuration: 2 * time.Hour}}}}
	a, b := Evaluate(in, now), Evaluate(in, now)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("nondeterministic")
	}
	if a.Windows[0].Key != "a" {
		t.Fatal("not sorted")
	}
	data, _ := json.Marshal(a)
	var x any
	if err := json.Unmarshal(data, &x); err != nil {
		t.Fatal(err)
	}
	for _, w := range a.Windows {
		for _, p := range []*float64{w.UsedPercent, w.RemainingPercentPoints, w.CycleBurnPercentPointsPerHour, w.RecentBurnPercentPointsPerHour, w.PaceBurnPercentPointsPerHour, w.SafeHourlyAllowancePercentPoints, w.SafeDailyAllowancePercentPoints} {
			if p != nil && (math.IsNaN(*p) || math.IsInf(*p, 0)) {
				t.Fatal("non-finite")
			}
		}
	}
}

func FuzzEvaluate(f *testing.F) {
	f.Add(50.0, int64(3600), int64(7200))
	f.Fuzz(func(t *testing.T, used float64, remainingSec, periodSec int64) {
		now := time.Unix(1_700_000_000, 0)
		r := Evaluate(Input{HistoryState: HistoryAvailable, Observation: Observation{ObservedAt: now, Windows: []WindowObservation{{Key: " x ", UsedPercent: &used, ResetAt: now.Add(time.Duration(remainingSec) * time.Second), PeriodDuration: time.Duration(periodSec) * time.Second}}}}, now)
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(data) {
			t.Fatal("invalid JSON")
		}
	})
}
