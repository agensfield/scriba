// Package budget derives provider-neutral quota pacing reports.
package budget

import (
	"math"
	"sort"
	"strings"
	"time"
)

type WindowKey string
type HistoryState string

const (
	HistoryAvailable   HistoryState = "available"
	HistoryEmpty       HistoryState = "empty"
	HistoryUnavailable HistoryState = "unavailable"
)

type WindowObservation struct {
	Key            WindowKey
	Label          string
	UsedPercent    *float64
	ResetAt        time.Time
	PeriodDuration time.Duration
}
type Observation struct {
	ObservedAt time.Time
	Windows    []WindowObservation
}
type Input struct {
	ProviderID   string
	Observation  Observation
	History      []Observation
	HistoryState HistoryState
}

type Report struct {
	SchemaVersion string    `json:"schemaVersion"`
	GeneratedAt   time.Time `json:"generatedAt"`
	ProviderID    string    `json:"providerId"`
	ObservedAt    time.Time `json:"observedAt"`
	History       History   `json:"history"`
	Windows       []Window  `json:"windows"`
}
type History struct {
	State       HistoryState `json:"state"`
	SampleCount int          `json:"sampleCount"`
}
type Window struct {
	Key                              WindowKey  `json:"key"`
	Label                            string     `json:"label"`
	UsedPercent                      *float64   `json:"usedPercent"`
	RemainingPercentPoints           *float64   `json:"remainingPercentPoints"`
	ResetAt                          *time.Time `json:"resetAt"`
	PeriodDurationMS                 *int64     `json:"periodDurationMs"`
	TimeRemainingMS                  *int64     `json:"timeRemainingMs"`
	CycleBurnPercentPointsPerHour    *float64   `json:"cycleBurnPercentPointsPerHour"`
	RecentBurnPercentPointsPerHour   *float64   `json:"recentBurnPercentPointsPerHour"`
	PaceBurnPercentPointsPerHour     *float64   `json:"paceBurnPercentPointsPerHour"`
	SafeHourlyAllowancePercentPoints *float64   `json:"safeHourlyAllowancePercentPoints"`
	SafeDailyAllowancePercentPoints  *float64   `json:"safeDailyAllowancePercentPoints"`
	ProjectedExhaustionAt            *time.Time `json:"projectedExhaustionAt"`
	TemporalMarginMS                 *int64     `json:"temporalMarginMs"`
	Risk                             string     `json:"risk"`
	Freshness                        string     `json:"freshness"`
	Confidence                       string     `json:"confidence"`
	Reasons                          []string   `json:"reasons"`
}

func Evaluate(in Input, now time.Time) Report {
	r := Report{SchemaVersion: "scriba.budget.v1", GeneratedAt: now, ProviderID: strings.TrimSpace(in.ProviderID), ObservedAt: in.Observation.ObservedAt, History: History{State: in.HistoryState, SampleCount: len(in.History)}, Windows: make([]Window, 0, len(in.Observation.Windows))}
	for _, o := range in.Observation.Windows {
		r.Windows = append(r.Windows, evaluateWindow(in, o, now))
	}
	sort.SliceStable(r.Windows, func(i, j int) bool {
		if r.Windows[i].Key == r.Windows[j].Key {
			return r.Windows[i].Label < r.Windows[j].Label
		}
		return r.Windows[i].Key < r.Windows[j].Key
	})
	return r
}

func evaluateWindow(in Input, o WindowObservation, now time.Time) (w Window) {
	o.Key = normalizeKey(o.Key)
	w = Window{Key: o.Key, Label: o.Label, Risk: "unknown", Freshness: freshness(in.Observation.ObservedAt, now), Confidence: "none", Reasons: []string{}}
	defer func() { sortReasons(w.Reasons) }()
	add := func(s string) {
		for _, v := range w.Reasons {
			if v == s {
				return
			}
		}
		w.Reasons = append(w.Reasons, s)
	}
	switch in.HistoryState {
	case HistoryUnavailable:
		add("history_unavailable")
	case HistoryEmpty:
		add("history_empty")
	}
	if in.Observation.ObservedAt.After(now.Add(time.Minute)) {
		add("observation_in_future")
	}
	if w.Freshness == "stale" {
		add("observation_stale")
	}
	if o.UsedPercent == nil {
		add("used_percent_missing")
		return w
	}
	if !finite(*o.UsedPercent) || *o.UsedPercent < 0 || *o.UsedPercent > 100 {
		add("used_percent_invalid")
		return w
	}
	used := *o.UsedPercent
	remaining := 100 - used
	w.UsedPercent = &used
	w.RemainingPercentPoints = &remaining
	if o.ResetAt.IsZero() {
		add("reset_time_missing")
		return w
	}
	reset := o.ResetAt
	w.ResetAt = &reset
	if o.PeriodDuration == 0 {
		add("period_duration_missing")
		return w
	}
	if o.PeriodDuration < 0 {
		add("period_duration_invalid")
		return w
	}
	pd := o.PeriodDuration.Milliseconds()
	w.PeriodDurationMS = &pd
	tr := o.ResetAt.Sub(now)
	trms := tr.Milliseconds()
	w.TimeRemainingMS = &trms
	if tr <= 0 {
		add("reset_elapsed")
		return w
	}
	cycleStart := o.ResetAt.Add(-o.PeriodDuration)
	elapsed := in.Observation.ObservedAt.Sub(cycleStart)
	if elapsed <= 0 {
		add("cycle_not_started")
	} else {
		v := used / elapsed.Hours()
		w.CycleBurnPercentPointsPerHour = ptr(v)
		add("cycle_estimate_available")
	}
	recent, count, recentSpan, reason := recentEstimate(in, o)
	if reason != "" {
		add(reason)
	}
	if recent != nil {
		w.RecentBurnPercentPointsPerHour = recent
		add("recent_estimate_available")
	}
	pace := maxPtr(w.CycleBurnPercentPointsPerHour, recent)
	w.PaceBurnPercentPointsPerHour = pace
	if pace != nil {
		if recent == nil {
			add("pace_cycle_only")
		} else if w.CycleBurnPercentPointsPerHour == nil {
			add("pace_recent_only")
		} else if *w.CycleBurnPercentPointsPerHour >= *recent {
			add("pace_conservative_cycle")
		} else {
			add("pace_conservative_recent")
		}
	}
	safe := remaining / tr.Hours()
	w.SafeHourlyAllowancePercentPoints = ptr(safe)
	w.SafeDailyAllowancePercentPoints = ptr(safe * 24)
	if pace == nil || *pace <= 0 {
		if pace != nil {
			add("burn_zero")
		}
		add("projection_unavailable")
	} else {
		p := in.Observation.ObservedAt.Add(time.Duration(remaining / (*pace) * float64(time.Hour)))
		w.ProjectedExhaustionAt = &p
		m := o.ResetAt.Sub(p).Milliseconds()
		w.TemporalMarginMS = &m
	}
	w.Confidence = confidence(w, count, recentSpan)
	if w.Freshness == "aging" && w.Confidence != "none" {
		add("confidence_downgraded_aging")
	}
	w.Risk = risk(w, now)
	return w
}

func recentEstimate(in Input, o WindowObservation) (*float64, int, time.Duration, string) {
	var eligible []Observation
	mismatch, decrease := false, false
	for _, h := range in.History {
		d := in.Observation.ObservedAt.Sub(h.ObservedAt)
		if d < 10*time.Minute || d > 24*time.Hour {
			continue
		}
		for _, x := range h.Windows {
			if normalizeKey(x.Key) != o.Key {
				continue
			}
			if x.ResetAt.Sub(o.ResetAt) > 5*time.Minute || o.ResetAt.Sub(x.ResetAt) > 5*time.Minute {
				mismatch = true
				continue
			}
			if x.UsedPercent == nil || !finite(*x.UsedPercent) {
				continue
			}
			if o.UsedPercent != nil && *x.UsedPercent > *o.UsedPercent {
				decrease = true
				continue
			}
			eligible = append(eligible, Observation{ObservedAt: h.ObservedAt, Windows: []WindowObservation{x}})
		}
	}
	if len(eligible) == 0 {
		if decrease {
			return nil, 0, 0, "history_counter_decrease"
		}
		if mismatch {
			return nil, 0, 0, "history_reset_mismatch"
		}
		if in.HistoryState == HistoryAvailable {
			return nil, 0, 0, "history_insufficient"
		}
		return nil, 0, 0, ""
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].ObservedAt.Before(eligible[j].ObservedAt) })
	oldest := eligible[0]
	delta := *o.UsedPercent - *oldest.Windows[0].UsedPercent
	v := delta / in.Observation.ObservedAt.Sub(oldest.ObservedAt).Hours()
	return ptr(v), len(eligible), in.Observation.ObservedAt.Sub(oldest.ObservedAt), ""
}

func confidence(w Window, count int, recentSpan time.Duration) string {
	if w.PaceBurnPercentPointsPerHour == nil || w.Freshness == "stale" || w.Freshness == "unknown" {
		return "none"
	}
	c := "low"
	if w.CycleBurnPercentPointsPerHour != nil && w.RecentBurnPercentPointsPerHour != nil && count >= 2 {
		c = "medium"
	}
	if c == "medium" && count >= 4 && recentSpan >= time.Hour {
		c = "high"
	}
	if w.Freshness == "aging" {
		if c == "high" {
			return "medium"
		}
		if c == "medium" {
			return "low"
		}
	}
	return c
}
func risk(w Window, now time.Time) string {
	if w.Freshness == "stale" || w.Freshness == "unknown" || w.RemainingPercentPoints == nil || w.TimeRemainingMS == nil || *w.TimeRemainingMS <= 0 {
		return "unknown"
	}
	if *w.RemainingPercentPoints == 0 || (w.ProjectedExhaustionAt != nil && !w.ProjectedExhaustionAt.After(now)) {
		return "critical"
	}
	if w.PaceBurnPercentPointsPerHour != nil && w.SafeHourlyAllowancePercentPoints != nil {
		if *w.PaceBurnPercentPointsPerHour >= *w.SafeHourlyAllowancePercentPoints {
			return "high"
		}
		if *w.PaceBurnPercentPointsPerHour >= .8**w.SafeHourlyAllowancePercentPoints || *w.RemainingPercentPoints <= 20 {
			return "elevated"
		}
	} else if *w.RemainingPercentPoints <= 20 {
		return "elevated"
	}
	return "low"
}
func freshness(observed, now time.Time) string {
	age := now.Sub(observed)
	if age < -time.Minute {
		return "unknown"
	}
	if age <= 10*time.Minute {
		return "fresh"
	}
	if age <= 30*time.Minute {
		return "aging"
	}
	return "stale"
}
func normalizeKey(k WindowKey) WindowKey {
	return WindowKey(strings.ToLower(strings.TrimSpace(string(k))))
}
func sortReasons(reasons []string) {
	order := []string{"history_unavailable", "history_empty", "history_insufficient", "history_reset_mismatch", "history_counter_decrease", "observation_in_future", "observation_stale", "used_percent_missing", "used_percent_invalid", "reset_time_missing", "reset_elapsed", "period_duration_missing", "period_duration_invalid", "cycle_not_started", "cycle_estimate_available", "recent_estimate_available", "pace_cycle_only", "pace_recent_only", "pace_conservative_cycle", "pace_conservative_recent", "burn_zero", "projection_unavailable", "confidence_downgraded_aging"}
	rank := make(map[string]int, len(order))
	for i, v := range order {
		rank[v] = i
	}
	sort.SliceStable(reasons, func(i, j int) bool { return rank[reasons[i]] < rank[reasons[j]] })
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func ptr[T any](v T) *T     { return &v }
func maxPtr(a, b *float64) *float64 {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if *a >= *b {
		return a
	}
	return b
}
