package resetwatch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/model"
)

const (
	ProviderCodex = "codex"

	LabelWeeklyLimit = "Weekly limit"
	LabelFiveHour    = "5h limit"
	LabelSparkWeekly = "Spark weekly"
	LabelSparkFive   = "Spark 5h"
	LabelReviewFive  = "Review 5h"
	LabelReviewWeek  = "Review weekly"

	ResetKindScheduled = "scheduled"
	ResetKindEarly     = "early"

	lowUsageResetDriftPercent = 5
)

type Window struct {
	Label            string
	UsedPercent      *float64
	ResetAt          time.Time
	PeriodDurationMs *int64
}

type Account struct {
	Ref   string
	Label string
	Email string
	Plan  string
}

type Observation struct {
	ProviderID   string
	Account      Account
	ObservedAt   time.Time
	Windows      []Window
	SnapshotJSON []byte
}

type WindowState struct {
	AccountRef       string
	Label            string
	StableResetAt    time.Time
	LastSeenResetAt  time.Time
	LastObservedAt   time.Time
	LastSnapshotJSON []byte
}

type Event struct {
	ID                     string
	ProviderID             string
	Account                Account
	PrimaryTriggerLabel    string
	SecondaryTriggerLabels []string
	ResetKind              string
	PreviousResetAt        time.Time
	CurrentResetAt         time.Time
	PreviousSnapshotJSON   []byte
	CurrentSnapshotJSON    []byte
	JokeID                 string
	DetectedAt             time.Time
}

type WarningEvent struct {
	ID                 string
	ProviderID         string
	Account            Account
	Label              string
	ThresholdRemaining int
	UsedPercent        float64
	RemainingPercent   float64
	ResetAt            time.Time
	SnapshotJSON       []byte
	DetectedAt         time.Time
}

type Decision struct {
	States []WindowState
	Events []Event
}

type Options struct {
	ClockJitter time.Duration
	DueJitter   time.Duration
	JokeChooser JokeChooser
}

type JokeChooser interface {
	Choose(Event) string
}

type JokeChooserFunc func(Event) string

func (f JokeChooserFunc) Choose(event Event) string { return f(event) }

func DefaultOptions() Options {
	return Options{
		ClockJitter: 5 * time.Minute,
		DueJitter:   10 * time.Minute,
		JokeChooser: JokeChooserFunc(func(Event) string { return "tibo-ceiling" }),
	}
}

func Decide(obs Observation, existing map[string]WindowState, opts Options) Decision {
	opts = normalizeOptions(opts)
	byLabel := windowsByLabel(obs.Windows)
	labels := trackedLabels(byLabel)
	states := make([]WindowState, 0, len(labels))
	var event *Event

	for _, label := range labels {
		window := byLabel[label]
		key := StateKey(obs.Account.Ref, label)
		prev, ok := existing[key]
		next := WindowState{
			AccountRef:       obs.Account.Ref,
			Label:            label,
			StableResetAt:    prev.StableResetAt,
			LastSeenResetAt:  window.ResetAt,
			LastObservedAt:   obs.ObservedAt,
			LastSnapshotJSON: cloneBytes(obs.SnapshotJSON),
		}
		if !ok || prev.StableResetAt.IsZero() {
			next.StableResetAt = window.ResetAt
			states = append(states, next)
			continue
		}
		if window.ResetAt.After(prev.StableResetAt.Add(opts.ClockJitter)) {
			next.StableResetAt = window.ResetAt
			if label == LabelWeeklyLimit {
				if !isLowUsageResetDrift(prev, window) {
					ev := newEvent(obs, prev, window.ResetAt, opts)
					event = &ev
				}
			} else if event != nil && isSecondaryWeekly(label) {
				event.SecondaryTriggerLabels = append(event.SecondaryTriggerLabels, label)
			}
		}
		states = append(states, next)
	}
	if event != nil {
		sort.Strings(event.SecondaryTriggerLabels)
		event.ID = EventID(event.ProviderID, event.Account.Ref, event.PrimaryTriggerLabel, event.CurrentResetAt)
		event.JokeID = opts.JokeChooser.Choose(*event)
		return Decision{States: states, Events: []Event{*event}}
	}
	return Decision{States: states}
}

func FromMetricLines(lines []model.MetricLine) []Window {
	windows := make([]Window, 0, len(lines))
	for _, line := range lines {
		if line.Type != "progress" || line.ResetsAt == "" {
			continue
		}
		resetAt, err := time.Parse(time.RFC3339Nano, line.ResetsAt)
		if err != nil {
			resetAt, err = time.Parse(time.RFC3339, line.ResetsAt)
		}
		if err != nil {
			continue
		}
		windows = append(windows, Window{
			Label:            line.Label,
			UsedPercent:      cloneFloat(line.Used),
			ResetAt:          resetAt.UTC(),
			PeriodDurationMs: cloneInt64(line.PeriodDurationMs),
		})
	}
	return windows
}

func SnapshotJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

func StateKey(accountRef, label string) string {
	return accountRef + "\x00" + label
}

func EventID(providerID, accountRef, label string, resetAt time.Time) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		providerID,
		accountRef,
		label,
		resetAt.UTC().Format(time.RFC3339Nano),
	}, "\x00")))
	return "reset_" + hex.EncodeToString(sum[:16])
}

func WarningEventID(providerID, accountRef, label string, resetAt time.Time, thresholdRemaining int) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		providerID,
		accountRef,
		label,
		resetAt.UTC().Format(time.RFC3339Nano),
		fmt.Sprintf("%d", thresholdRemaining),
	}, "\x00")))
	return "warning_" + hex.EncodeToString(sum[:16])
}

func WarningCandidates(obs Observation) []WarningEvent {
	labels := []string{LabelFiveHour, LabelWeeklyLimit}
	byLabel := windowsByLabel(obs.Windows)
	var warnings []WarningEvent
	for _, label := range labels {
		window, ok := byLabel[label]
		if !ok || window.UsedPercent == nil || window.ResetAt.IsZero() {
			continue
		}
		used := clampPercent(*window.UsedPercent)
		remaining := clampPercent(100 - used)
		threshold, ok := warningThreshold(remaining)
		if !ok {
			continue
		}
		event := WarningEvent{
			ProviderID:         providerID(obs.ProviderID),
			Account:            obs.Account,
			Label:              label,
			ThresholdRemaining: threshold,
			UsedPercent:        used,
			RemainingPercent:   remaining,
			ResetAt:            window.ResetAt,
			SnapshotJSON:       cloneBytes(obs.SnapshotJSON),
			DetectedAt:         obs.ObservedAt,
		}
		event.ID = WarningEventID(event.ProviderID, event.Account.Ref, event.Label, event.ResetAt, event.ThresholdRemaining)
		warnings = append(warnings, event)
	}
	return warnings
}

func isLowUsageResetDrift(prev WindowState, current Window) bool {
	if current.UsedPercent == nil {
		return false
	}
	prevUsed, ok := snapshotUsedPercent(prev.LastSnapshotJSON, current.Label)
	if !ok {
		return false
	}
	return clampPercent(prevUsed) <= lowUsageResetDriftPercent &&
		clampPercent(*current.UsedPercent) <= lowUsageResetDriftPercent
}

func snapshotUsedPercent(snapshot []byte, label string) (float64, bool) {
	var decoded struct {
		Lines []model.MetricLine `json:"lines"`
	}
	if err := json.Unmarshal(snapshot, &decoded); err != nil {
		return 0, false
	}
	for _, line := range decoded.Lines {
		if line.Type == "progress" && line.Label == label && line.Used != nil {
			return *line.Used, true
		}
	}
	return 0, false
}

func warningThreshold(remaining float64) (int, bool) {
	switch {
	case remaining <= 0:
		return 0, true
	case remaining <= 5:
		return 5, true
	case remaining <= 10:
		return 10, true
	case remaining <= 20:
		return 20, true
	default:
		return 0, false
	}
}

func clampPercent(value float64) float64 {
	if math.IsNaN(value) {
		return 0
	}
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func normalizeOptions(opts Options) Options {
	def := DefaultOptions()
	if opts.ClockJitter == 0 {
		opts.ClockJitter = def.ClockJitter
	}
	if opts.DueJitter == 0 {
		opts.DueJitter = def.DueJitter
	}
	if opts.JokeChooser == nil {
		opts.JokeChooser = def.JokeChooser
	}
	return opts
}

func windowsByLabel(windows []Window) map[string]Window {
	byLabel := make(map[string]Window, len(windows))
	for _, window := range windows {
		if window.Label == "" || window.ResetAt.IsZero() {
			continue
		}
		byLabel[window.Label] = window
	}
	return byLabel
}

func trackedLabels(byLabel map[string]Window) []string {
	preferred := []string{LabelFiveHour, LabelWeeklyLimit, LabelSparkFive, LabelSparkWeekly, LabelReviewFive, LabelReviewWeek}
	labels := make([]string, 0, len(byLabel))
	seen := make(map[string]bool, len(byLabel))
	for _, label := range preferred {
		if _, ok := byLabel[label]; ok {
			labels = append(labels, label)
			seen[label] = true
		}
	}
	for label := range byLabel {
		if !seen[label] {
			labels = append(labels, label)
		}
	}
	sort.Strings(labels[preferredPrefix(labels, preferred):])
	return labels
}

func preferredPrefix(labels, preferred []string) int {
	n := 0
	for n < len(labels) && n < len(preferred) {
		if labels[n] != preferred[n] {
			return n
		}
		n++
	}
	return n
}

func newEvent(obs Observation, prev WindowState, currentResetAt time.Time, opts Options) Event {
	kind := ResetKindEarly
	if !prev.StableResetAt.IsZero() && !obs.ObservedAt.Before(prev.StableResetAt.Add(-opts.DueJitter)) {
		kind = ResetKindScheduled
	}
	return Event{
		ProviderID:             providerID(obs.ProviderID),
		Account:                obs.Account,
		PrimaryTriggerLabel:    LabelWeeklyLimit,
		ResetKind:              kind,
		PreviousResetAt:        prev.StableResetAt,
		CurrentResetAt:         currentResetAt,
		PreviousSnapshotJSON:   cloneBytes(prev.LastSnapshotJSON),
		CurrentSnapshotJSON:    cloneBytes(obs.SnapshotJSON),
		DetectedAt:             obs.ObservedAt,
		SecondaryTriggerLabels: []string{},
	}
}

func providerID(input string) string {
	if input == "" {
		return ProviderCodex
	}
	return input
}

func isSecondaryWeekly(label string) bool {
	return label == LabelSparkWeekly || label == LabelReviewWeek
}

func cloneBytes(input []byte) []byte {
	if input == nil {
		return nil
	}
	out := make([]byte, len(input))
	copy(out, input)
	return out
}

func cloneFloat(input *float64) *float64 {
	if input == nil {
		return nil
	}
	value := *input
	return &value
}

func cloneInt64(input *int64) *int64 {
	if input == nil {
		return nil
	}
	value := *input
	return &value
}
