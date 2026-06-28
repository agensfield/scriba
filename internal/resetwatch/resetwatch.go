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
	LabelResetGrants = "Reset grants"
	LabelGrantExpiry = "Grant expiry"

	ResetKindScheduled = "scheduled"
	ResetKindEarly     = "early"

	lowUsageResetDriftPercent   = 5
	nearDueZeroResetDriftWindow = 24 * time.Hour
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
	ResetGrants  ResetGrants
	SnapshotJSON []byte
}

type ResetGrants struct {
	AvailableCount *int
	ExpiresAt      time.Time
	Credits        []ResetCredit
}

type ResetCredit struct {
	ID        string
	Status    string
	ResetType string
	Title     string
	GrantedAt time.Time
	ExpiresAt time.Time
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

type GrantExpiryWarning struct {
	ID            string
	ProviderID    string
	Account       Account
	CreditID      string
	CreditTitle   string
	ThresholdDays int
	ExpiresAt     time.Time
	SnapshotJSON  []byte
	DetectedAt    time.Time
}

type ResetGrantEvent struct {
	ID             string
	ProviderID     string
	Account        Account
	CreditID       string
	CreditTitle    string
	ResetType      string
	GrantedAt      time.Time
	ExpiresAt      time.Time
	AvailableCount int
	SnapshotJSON   []byte
	DetectedAt     time.Time
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
			syntheticNearDueZero := false
			if label == LabelWeeklyLimit {
				syntheticNearDueZero = isNearDueSyntheticZeroReset(prev, window, obs.ObservedAt, opts)
				if !syntheticNearDueZero {
					next.StableResetAt = window.ResetAt
				}
				if !syntheticNearDueZero && !isLowUsageResetDrift(prev, window) {
					ev := newEvent(obs, prev, window.ResetAt, opts)
					event = &ev
				}
			} else {
				next.StableResetAt = window.ResetAt
			}
			if label != LabelWeeklyLimit && event != nil && isSecondaryWeekly(label) {
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

func ResetGrantsFromMetricLines(lines []model.MetricLine) ResetGrants {
	var grants ResetGrants
	for _, line := range lines {
		switch line.Label {
		case LabelResetGrants:
			if count, ok := intValue(line.Value); ok {
				grants.AvailableCount = &count
			}
		case LabelGrantExpiry:
			value, ok := line.Value.(string)
			if !ok || value == "" {
				continue
			}
			expiresAt, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				expiresAt, err = time.Parse(time.RFC3339, value)
			}
			if err == nil {
				grants.ExpiresAt = expiresAt.UTC()
			}
		}
	}
	return grants
}

func ResetGrantsFromSnapshotJSON(data []byte) ResetGrants {
	var payload struct {
		Lines        []model.MetricLine `json:"lines"`
		ResetCredits []struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			ResetType string `json:"resetType"`
			Title     string `json:"title"`
			GrantedAt string `json:"grantedAt"`
			ExpiresAt string `json:"expiresAt"`
		} `json:"resetCredits"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ResetGrants{}
	}
	grants := ResetGrantsFromMetricLines(payload.Lines)
	for _, credit := range payload.ResetCredits {
		if credit.Status != "" && credit.Status != "available" {
			continue
		}
		expiresAt, ok := parseRFC3339Time(credit.ExpiresAt)
		if !ok {
			continue
		}
		grantedAt, _ := parseRFC3339Time(credit.GrantedAt)
		grants.Credits = append(grants.Credits, ResetCredit{
			ID:        credit.ID,
			Status:    credit.Status,
			ResetType: credit.ResetType,
			Title:     credit.Title,
			GrantedAt: grantedAt,
			ExpiresAt: expiresAt,
		})
		if grants.ExpiresAt.IsZero() || expiresAt.Before(grants.ExpiresAt) {
			grants.ExpiresAt = expiresAt
		}
	}
	if grants.AvailableCount == nil && len(grants.Credits) > 0 {
		count := len(grants.Credits)
		grants.AvailableCount = &count
	}
	return grants
}

func SnapshotJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

func intValue(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		if math.Trunc(v) == v {
			return int(v), true
		}
	case json.Number:
		parsed, err := v.Int64()
		return int(parsed), err == nil
	}
	return 0, false
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

func GrantExpiryWarningID(providerID, accountRef, creditID string, expiresAt time.Time, thresholdDays int) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		providerID,
		accountRef,
		creditID,
		expiresAt.UTC().Format(time.RFC3339Nano),
		fmt.Sprintf("%d", thresholdDays),
	}, "\x00")))
	return "grant_warning_" + hex.EncodeToString(sum[:16])
}

func ResetGrantEventID(providerID, accountRef, creditID string, grantedAt, expiresAt time.Time) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		providerID,
		accountRef,
		creditID,
		grantedAt.UTC().Format(time.RFC3339Nano),
		expiresAt.UTC().Format(time.RFC3339Nano),
	}, "\x00")))
	return "grant_" + hex.EncodeToString(sum[:16])
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

func ResetGrantEventCandidates(obs Observation) []ResetGrantEvent {
	now := obs.ObservedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	available := 0
	if obs.ResetGrants.AvailableCount != nil {
		available = *obs.ResetGrants.AvailableCount
	} else {
		available = len(obs.ResetGrants.Credits)
	}
	events := []ResetGrantEvent{}
	for _, credit := range obs.ResetGrants.Credits {
		if credit.Status != "" && credit.Status != "available" {
			continue
		}
		if credit.ExpiresAt.IsZero() {
			continue
		}
		creditID := credit.ID
		if creditID == "" {
			creditID = resetCreditFallbackID(credit)
		}
		event := ResetGrantEvent{
			ProviderID:     providerID(obs.ProviderID),
			Account:        obs.Account,
			CreditID:       creditID,
			CreditTitle:    credit.Title,
			ResetType:      credit.ResetType,
			GrantedAt:      credit.GrantedAt,
			ExpiresAt:      credit.ExpiresAt,
			AvailableCount: available,
			SnapshotJSON:   cloneBytes(obs.SnapshotJSON),
			DetectedAt:     now,
		}
		event.ID = ResetGrantEventID(event.ProviderID, event.Account.Ref, event.CreditID, event.GrantedAt, event.ExpiresAt)
		events = append(events, event)
	}
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].GrantedAt.Equal(events[j].GrantedAt) {
			return events[i].GrantedAt.Before(events[j].GrantedAt)
		}
		if !events[i].ExpiresAt.Equal(events[j].ExpiresAt) {
			return events[i].ExpiresAt.Before(events[j].ExpiresAt)
		}
		return events[i].CreditID < events[j].CreditID
	})
	return events
}

func GrantExpiryWarningCandidates(obs Observation) []GrantExpiryWarning {
	now := obs.ObservedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	thresholds := []int{5, 3, 1}
	warnings := []GrantExpiryWarning{}
	for _, credit := range obs.ResetGrants.Credits {
		if credit.Status != "" && credit.Status != "available" {
			continue
		}
		if credit.ExpiresAt.IsZero() || !credit.ExpiresAt.After(now) {
			continue
		}
		creditID := credit.ID
		if creditID == "" {
			creditID = resetCreditFallbackID(credit)
		}
		remaining := credit.ExpiresAt.Sub(now)
		for _, thresholdDays := range thresholds {
			if remaining > time.Duration(thresholdDays)*24*time.Hour {
				continue
			}
			warning := GrantExpiryWarning{
				ProviderID:    providerID(obs.ProviderID),
				Account:       obs.Account,
				CreditID:      creditID,
				CreditTitle:   credit.Title,
				ThresholdDays: thresholdDays,
				ExpiresAt:     credit.ExpiresAt,
				SnapshotJSON:  cloneBytes(obs.SnapshotJSON),
				DetectedAt:    now,
			}
			warning.ID = GrantExpiryWarningID(warning.ProviderID, warning.Account.Ref, warning.CreditID, warning.ExpiresAt, warning.ThresholdDays)
			warnings = append(warnings, warning)
		}
	}
	sort.SliceStable(warnings, func(i, j int) bool {
		if !warnings[i].ExpiresAt.Equal(warnings[j].ExpiresAt) {
			return warnings[i].ExpiresAt.Before(warnings[j].ExpiresAt)
		}
		return warnings[i].ThresholdDays > warnings[j].ThresholdDays
	})
	return warnings
}

func resetCreditFallbackID(credit ResetCredit) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		credit.Title,
		credit.ExpiresAt.UTC().Format(time.RFC3339Nano),
		credit.GrantedAt.UTC().Format(time.RFC3339Nano),
	}, "\x00")))
	return "credit_" + hex.EncodeToString(sum[:8])
}

func isLowUsageResetDrift(prev WindowState, current Window) bool {
	if current.UsedPercent == nil {
		return false
	}
	prevWindow, ok := snapshotWindow(prev.LastSnapshotJSON, current.Label)
	if !ok || prevWindow.UsedPercent == nil {
		return false
	}
	return clampPercent(*prevWindow.UsedPercent) <= lowUsageResetDriftPercent &&
		clampPercent(*current.UsedPercent) <= lowUsageResetDriftPercent
}

func parseRFC3339Time(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, value)
	}
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func isNearDueSyntheticZeroReset(prev WindowState, current Window, observedAt time.Time, opts Options) bool {
	if current.UsedPercent == nil || clampPercent(*current.UsedPercent) > lowUsageResetDriftPercent {
		return false
	}
	if prev.StableResetAt.IsZero() {
		return false
	}
	if !observedAt.Before(prev.StableResetAt.Add(-opts.DueJitter)) {
		return false
	}
	if prev.StableResetAt.Sub(observedAt) > nearDueZeroResetDriftWindow {
		return false
	}
	return resetAnchoredAtObservedPeriod(current, observedAt, opts.ClockJitter)
}

func resetAnchoredAtObservedPeriod(current Window, observedAt time.Time, tolerance time.Duration) bool {
	if current.PeriodDurationMs == nil || *current.PeriodDurationMs <= 0 || observedAt.IsZero() || current.ResetAt.IsZero() {
		return false
	}
	expected := observedAt.Add(time.Duration(*current.PeriodDurationMs) * time.Millisecond)
	delta := current.ResetAt.Sub(expected)
	if delta < 0 {
		delta = -delta
	}
	return delta <= tolerance
}

func snapshotWindow(snapshot []byte, label string) (Window, bool) {
	var decoded struct {
		Lines []model.MetricLine `json:"lines"`
	}
	if err := json.Unmarshal(snapshot, &decoded); err != nil {
		return Window{}, false
	}
	for _, window := range FromMetricLines(decoded.Lines) {
		if window.Label == label {
			return window, true
		}
	}
	return Window{}, false
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
