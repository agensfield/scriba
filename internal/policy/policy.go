package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

type StateKey struct{ RuleID, Subject string }

func (k StateKey) MarshalText() ([]byte, error) {
	return []byte(k.RuleID + "\x00" + k.Subject), nil
}

type WindowObservation struct {
	Key            string
	Label          string
	UsedPercent    *float64
	ResetAt        time.Time
	PeriodDuration time.Duration
}

type GrantObservation struct {
	ID        string
	Status    string
	Title     string
	ResetType string
	GrantedAt time.Time
	ExpiresAt time.Time
}

type Input struct {
	ProviderID     string
	AccountRef     string
	ObservedAt     time.Time
	Windows        []WindowObservation
	Grants         []GrantObservation
	AvailableCount *int
	Previous       map[StateKey]State
	Bootstrap      bool
}

type State struct {
	StableResetAt        time.Time
	LastResetAt          time.Time
	LastObservedAt       time.Time
	LastUsedPercent      *float64
	ReachedCheckpoints   []int
	KnownGrantIdentities []string
	AvailableGrantCount  int
	GrantExpiresAt       time.Time
}

type EventKind string

const (
	EventRemainingCheckpoint   EventKind = "remaining_checkpoint"
	EventResetTransition       EventKind = "reset_transition"
	EventGrantAvailable        EventKind = "grant_available"
	EventGrantExpiryCheckpoint EventKind = "grant_expiry_checkpoint"
)

const (
	ReasonBootstrap          = "bootstrap_silence"
	ReasonNoMatch            = "no_match"
	ReasonAlreadyReached     = "checkpoint_already_reached"
	ReasonCheckpointReached  = "checkpoint_reached"
	ReasonResetAdvanced      = "reset_advanced"
	ReasonResetUnchanged     = "reset_not_advanced"
	ReasonResetDrift         = "low_usage_reset_drift"
	ReasonSyntheticReset     = "near_due_synthetic_zero"
	ReasonGrantAvailable     = "grant_became_available"
	ReasonGrantKnown         = "grant_already_known"
	ReasonGrantInactive      = "grant_not_available"
	ReasonStaleObservation   = "stale_observation"
	ReasonEqualCountRotation = "equal_count_rotation"
)

type Event struct {
	ID                    string
	RuleID                string
	Kind                  EventKind
	Subject               string
	LegacyLabel           string
	SecondaryLegacyLabels []string
	Checkpoint            int
	UsedPercent           float64
	RemainingPercent      float64
	PreviousResetAt       time.Time
	ResetAt               time.Time
	ResetKind             string
	Grant                 GrantObservation
	AvailableCount        int
	DetectedAt            time.Time
}

type Explanation struct {
	RuleID  string
	Kind    RuleKind
	Subject string
	Emitted bool
	Reason  string
}

type Result struct {
	States       map[StateKey]State
	Events       []Event
	Explanations []Explanation
}

func Evaluate(cfg Config, in Input) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}
	if cfg.Preset == PresetCurrent {
		cfg = CurrentPreset()
	}
	if in.ProviderID == "" {
		in.ProviderID = "codex"
	}
	if err := validateInput(in); err != nil {
		return Result{}, err
	}
	r := Result{States: map[StateKey]State{}}
	for k, v := range in.Previous {
		r.States[k] = cloneState(v)
	}
	windows := map[string]WindowObservation{}
	for _, w := range in.Windows {
		if w.Key != "" {
			windows[w.Key] = w
		}
	}
	grants := normalizeGrants(in.Grants)
	sort.Slice(grants, func(i, j int) bool { return grantIdentity(grants[i]) < grantIdentity(grants[j]) })
	for _, rule := range cfg.Rules {
		switch rule.Kind {
		case KindRemainingCheckpoint:
			evalRemaining(&r, rule, in, windows)
		case KindResetTransition:
			evalReset(&r, rule, in, windows)
		case KindGrantAvailable:
			evalGrants(&r, rule, in, grants)
		case KindGrantExpiryCheckpoint:
			evalExpiry(&r, rule, in, grants)
		}
	}
	return r, nil
}

func evalRemaining(out *Result, rule Rule, in Input, windows map[string]WindowObservation) {
	for _, subject := range rule.WindowKeys {
		w, ok := windows[subject]
		if !ok || w.UsedPercent == nil || w.ResetAt.IsZero() {
			explain(out, rule, subject, false, ReasonNoMatch)
			continue
		}
		key := StateKey{rule.ID, subject}
		prev, exists := in.Previous[key]
		if stale(in.ObservedAt, prev.LastObservedAt) {
			explain(out, rule, subject, false, ReasonStaleObservation)
			continue
		}
		next := cloneState(prev)
		if !sameTime(prev.LastResetAt, w.ResetAt) {
			next.ReachedCheckpoints = nil
		}
		next.LastResetAt, next.LastObservedAt, next.LastUsedPercent = w.ResetAt.UTC(), in.ObservedAt.UTC(), floatPtr(clamp(*w.UsedPercent))
		remaining := clamp(100 - clamp(*w.UsedPercent))
		cp, matched := checkpoint(rule.Checkpoints, remaining)
		reason, emit := ReasonNoMatch, false
		if matched {
			switch {
			case in.Bootstrap || !exists:
				reason = ReasonBootstrap
			case slices.Contains(next.ReachedCheckpoints, cp):
				reason = ReasonAlreadyReached
			default:
				reason, emit = ReasonCheckpointReached, true
			}
			next.ReachedCheckpoints = addInt(next.ReachedCheckpoints, cp)
		}
		out.States[key] = next
		if emit {
			out.Events = append(out.Events, Event{ID: semanticID("warning", in.ProviderID, in.AccountRef, w.Label, w.ResetAt.UTC().Format(time.RFC3339Nano), fmt.Sprint(cp)), RuleID: rule.ID, Kind: EventRemainingCheckpoint, Subject: subject, LegacyLabel: w.Label, Checkpoint: cp, UsedPercent: clamp(*w.UsedPercent), RemainingPercent: remaining, ResetAt: w.ResetAt.UTC(), DetectedAt: in.ObservedAt.UTC()})
		}
		explain(out, rule, subject, emit, reason)
	}
}

func evalReset(out *Result, rule Rule, in Input, windows map[string]WindowObservation) {
	secondary := resetSecondary(out, rule, in, windows)
	for _, subject := range rule.WindowKeys {
		w, ok := windows[subject]
		if !ok || w.ResetAt.IsZero() {
			explain(out, rule, subject, false, ReasonNoMatch)
			continue
		}
		key := StateKey{rule.ID, subject}
		prev, exists := in.Previous[key]
		if stale(in.ObservedAt, prev.LastObservedAt) {
			explain(out, rule, subject, false, ReasonStaleObservation)
			continue
		}
		next := cloneState(prev)
		next.LastResetAt, next.LastObservedAt, next.LastUsedPercent = w.ResetAt.UTC(), in.ObservedAt.UTC(), copyFloat(w.UsedPercent)
		reason, emit := ReasonResetUnchanged, false
		if in.Bootstrap || !exists || prev.StableResetAt.IsZero() {
			next.StableResetAt = w.ResetAt.UTC()
			reason = ReasonBootstrap
		} else if w.ResetAt.After(prev.StableResetAt.Add(time.Duration(rule.ClockJitterSec) * time.Second)) {
			if synthetic(prev, w, in.ObservedAt, rule) {
				reason = ReasonSyntheticReset
			} else {
				next.StableResetAt = w.ResetAt.UTC()
				if lowUsageDrift(prev, w) {
					reason = ReasonResetDrift
				} else {
					reason, emit = ReasonResetAdvanced, true
				}
			}
		}
		out.States[key] = next
		if emit {
			kind := "early"
			if !in.ObservedAt.Before(prev.StableResetAt.Add(-time.Duration(rule.DueJitterSec) * time.Second)) {
				kind = "scheduled"
			}
			out.Events = append(out.Events, Event{ID: semanticID("reset", in.ProviderID, in.AccountRef, w.Label, w.ResetAt.UTC().Format(time.RFC3339Nano)), RuleID: rule.ID, Kind: EventResetTransition, Subject: subject, LegacyLabel: w.Label, PreviousResetAt: prev.StableResetAt.UTC(), ResetAt: w.ResetAt.UTC(), ResetKind: kind, DetectedAt: in.ObservedAt.UTC()})
			out.Events[len(out.Events)-1].SecondaryLegacyLabels = slices.Clone(secondary)
		}
		explain(out, rule, subject, emit, reason)
	}
}

func resetSecondary(out *Result, rule Rule, in Input, windows map[string]WindowObservation) []string {
	var advanced []string
	for _, subject := range rule.SecondaryWindowKeys {
		w, ok := windows[subject]
		if !ok || w.ResetAt.IsZero() {
			continue
		}
		key := StateKey{rule.ID, subject}
		prev, exists := in.Previous[key]
		if stale(in.ObservedAt, prev.LastObservedAt) {
			continue
		}
		next := cloneState(prev)
		next.LastResetAt, next.LastObservedAt, next.LastUsedPercent = w.ResetAt.UTC(), in.ObservedAt.UTC(), copyFloat(w.UsedPercent)
		if in.Bootstrap || !exists || prev.StableResetAt.IsZero() {
			next.StableResetAt = w.ResetAt.UTC()
		} else if w.ResetAt.After(prev.StableResetAt.Add(time.Duration(rule.ClockJitterSec) * time.Second)) {
			next.StableResetAt = w.ResetAt.UTC()
			advanced = append(advanced, w.Label)
		}
		out.States[key] = next
	}
	sort.Strings(advanced)
	return advanced
}

func evalGrants(out *Result, rule Rule, in Input, grants []GrantObservation) {
	key := StateKey{rule.ID, in.AccountRef}
	prev := in.Previous[key]
	if stale(in.ObservedAt, prev.LastObservedAt) {
		explain(out, rule, in.AccountRef, false, ReasonStaleObservation)
		return
	}
	next := cloneState(prev)
	next.LastObservedAt = in.ObservedAt.UTC()
	known := map[string]bool{}
	for _, id := range prev.KnownGrantIdentities {
		known[id] = true
	}
	available := make([]GrantObservation, 0, len(grants))
	for _, g := range grants {
		if (g.Status != "" && g.Status != "available") || g.ExpiresAt.IsZero() {
			explain(out, rule, g.ID, false, ReasonGrantInactive)
			continue
		}
		available = append(available, g)
	}
	currentCount := len(available)
	if in.AvailableCount != nil {
		currentCount = *in.AvailableCount
	}
	delta := currentCount - prev.AvailableGrantCount
	newGrants := make([]GrantObservation, 0)
	for _, g := range available {
		if !known[grantIdentity(g)] {
			newGrants = append(newGrants, g)
		}
	}
	for i, g := range newGrants {
		emit := !in.Bootstrap && delta > i
		reason := ReasonEqualCountRotation
		if in.Bootstrap {
			reason = ReasonBootstrap
		} else if emit {
			reason = ReasonGrantAvailable
		}
		if emit {
			out.Events = append(out.Events, Event{ID: semanticID("grant", in.ProviderID, in.AccountRef, g.ID, g.GrantedAt.UTC().Format(time.RFC3339Nano), g.ExpiresAt.UTC().Format(time.RFC3339Nano)), RuleID: rule.ID, Kind: EventGrantAvailable, Subject: g.ID, Grant: g, AvailableCount: currentCount, DetectedAt: in.ObservedAt.UTC()})
		}
		explain(out, rule, g.ID, emit, reason)
	}
	if len(newGrants) == 0 {
		explain(out, rule, in.AccountRef, false, func() string {
			if in.Bootstrap {
				return ReasonBootstrap
			}
			return ReasonGrantKnown
		}())
	}
	for _, g := range available {
		id := grantIdentity(g)
		if !known[id] {
			next.KnownGrantIdentities = append(next.KnownGrantIdentities, id)
			known[id] = true
		}
	}
	next.AvailableGrantCount = currentCount
	sort.Strings(next.KnownGrantIdentities)
	out.States[key] = next
}

func evalExpiry(out *Result, rule Rule, in Input, grants []GrantObservation) {
	for _, g := range grants {
		if (g.Status != "" && g.Status != "available") || !g.ExpiresAt.After(in.ObservedAt) {
			explain(out, rule, g.ID, false, ReasonGrantInactive)
			continue
		}
		key := StateKey{rule.ID, g.ID}
		prev := in.Previous[key]
		if stale(in.ObservedAt, prev.LastObservedAt) {
			explain(out, rule, g.ID, false, ReasonStaleObservation)
			continue
		}
		next := cloneState(prev)
		if !prev.GrantExpiresAt.IsZero() && !prev.GrantExpiresAt.Equal(g.ExpiresAt) {
			next.ReachedCheckpoints = nil
		}
		days := g.ExpiresAt.Sub(in.ObservedAt)
		reached := durationCheckpoints(rule.Checkpoints, days)
		reason, emitted := ReasonNoMatch, false
		for _, cp := range reached {
			if slices.Contains(next.ReachedCheckpoints, cp) {
				reason = ReasonAlreadyReached
				continue
			}
			next.ReachedCheckpoints = addInt(next.ReachedCheckpoints, cp)
			if in.Bootstrap {
				reason = ReasonBootstrap
				continue
			}
			reason, emitted = ReasonCheckpointReached, true
			out.Events = append(out.Events, Event{ID: semanticID("grant_warning", in.ProviderID, in.AccountRef, g.ID, g.ExpiresAt.UTC().Format(time.RFC3339Nano), fmt.Sprint(cp)), RuleID: rule.ID, Kind: EventGrantExpiryCheckpoint, Subject: g.ID, Checkpoint: cp, Grant: g, DetectedAt: in.ObservedAt.UTC()})
		}
		next.LastObservedAt = in.ObservedAt.UTC()
		next.GrantExpiresAt = g.ExpiresAt.UTC()
		out.States[key] = next
		explain(out, rule, g.ID, emitted, reason)
	}
}

func explain(out *Result, r Rule, subject string, emit bool, reason string) {
	out.Explanations = append(out.Explanations, Explanation{r.ID, r.Kind, subject, emit, reason})
}
func checkpoint(cps []int, remaining float64) (int, bool) {
	for i := len(cps) - 1; i >= 0; i-- {
		if remaining <= float64(cps[i]) {
			return cps[i], true
		}
	}
	return 0, false
}
func durationCheckpoints(cps []int, d time.Duration) []int {
	var reached []int
	for _, cp := range cps {
		if d <= time.Duration(cp)*24*time.Hour {
			reached = append(reached, cp)
		}
	}
	return reached
}
func semanticID(prefix string, fields ...string) string {
	h := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return prefix + "_" + hex.EncodeToString(h[:16])
}
func clamp(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
func copyFloat(v *float64) *float64 {
	if v == nil {
		return nil
	}
	return floatPtr(clamp(*v))
}
func floatPtr(v float64) *float64  { return &v }
func sameTime(a, b time.Time) bool { return !a.IsZero() && a.Equal(b) }
func addInt(v []int, n int) []int {
	if !slices.Contains(v, n) {
		v = append(v, n)
		sort.Sort(sort.Reverse(sort.IntSlice(v)))
	}
	return v
}
func cloneState(v State) State {
	v.LastUsedPercent = copyFloat(v.LastUsedPercent)
	v.ReachedCheckpoints = slices.Clone(v.ReachedCheckpoints)
	v.KnownGrantIdentities = slices.Clone(v.KnownGrantIdentities)
	return v
}

func stale(observed, previous time.Time) bool {
	return !previous.IsZero() && observed.Before(previous)
}

func validateInput(in Input) error {
	if !canonicalIdentifier.MatchString(in.ProviderID) {
		return fmt.Errorf("providerID must be a canonical identifier")
	}
	if !safeExternalIdentifier(in.AccountRef) {
		return fmt.Errorf("accountRef must be a safe identifier")
	}
	if in.ObservedAt.IsZero() {
		return fmt.Errorf("observedAt is required")
	}
	windowKeys := map[string]bool{}
	for i, w := range in.Windows {
		if !canonicalIdentifier.MatchString(w.Key) {
			return fmt.Errorf("windows[%d].key must be a canonical identifier", i)
		}
		if windowKeys[w.Key] {
			return fmt.Errorf("duplicate window key %q", w.Key)
		}
		windowKeys[w.Key] = true
		if w.Label == "" || strings.TrimSpace(w.Label) != w.Label || strings.ContainsRune(w.Label, '\x00') {
			return fmt.Errorf("windows[%d].label must be safe and trimmed", i)
		}
	}
	identities := map[string]bool{}
	grantIDs := map[string]bool{}
	for i, g := range normalizeGrants(in.Grants) {
		if !safeExternalIdentifier(g.ID) {
			return fmt.Errorf("grants[%d].id must be a safe identifier", i)
		}
		if grantIDs[g.ID] {
			return fmt.Errorf("duplicate grant id %q", g.ID)
		}
		grantIDs[g.ID] = true
		identity := grantIdentity(g)
		if identities[identity] {
			return fmt.Errorf("duplicate grant semantic identity %q", identity)
		}
		identities[identity] = true
		if strings.ContainsRune(g.Title, '\x00') || strings.ContainsRune(g.ResetType, '\x00') || strings.ContainsRune(g.Status, '\x00') {
			return fmt.Errorf("grants[%d] contains NUL", i)
		}
	}
	if in.AvailableCount != nil && (*in.AvailableCount < 0 || *in.AvailableCount < availableGrantLen(in.Grants)) {
		return fmt.Errorf("availableCount is inconsistent with grants")
	}
	for k := range in.Previous {
		if !canonicalIdentifier.MatchString(k.RuleID) || !safeExternalIdentifier(k.Subject) {
			return fmt.Errorf("previous state contains unsafe key")
		}
	}
	return nil
}

func availableGrantLen(grants []GrantObservation) int {
	n := 0
	for _, g := range grants {
		if (g.Status == "" || g.Status == "available") && !g.ExpiresAt.IsZero() {
			n++
		}
	}
	return n
}

func safeExternalIdentifier(s string) bool {
	if s == "" || strings.TrimSpace(s) != s || strings.ContainsRune(s, '\x00') {
		return false
	}
	for _, r := range s {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func normalizeGrants(in []GrantObservation) []GrantObservation {
	out := slices.Clone(in)
	for i := range out {
		if out[i].ID == "" {
			out[i].ID = grantFallbackID(out[i])
		}
	}
	return out
}

func grantFallbackID(g GrantObservation) string {
	h := sha256.Sum256([]byte(strings.Join([]string{g.Title, g.ExpiresAt.UTC().Format(time.RFC3339Nano), g.GrantedAt.UTC().Format(time.RFC3339Nano)}, "\x00")))
	return "credit_" + hex.EncodeToString(h[:8])
}

func grantIdentity(g GrantObservation) string {
	return strings.Join([]string{g.ID, g.GrantedAt.UTC().Format(time.RFC3339Nano), g.ExpiresAt.UTC().Format(time.RFC3339Nano)}, "\x00")
}
func lowUsageDrift(prev State, w WindowObservation) bool {
	return prev.LastUsedPercent != nil && w.UsedPercent != nil && clamp(*prev.LastUsedPercent) <= 5 && clamp(*w.UsedPercent) <= 5
}
func synthetic(prev State, w WindowObservation, observed time.Time, r Rule) bool {
	if w.UsedPercent == nil || clamp(*w.UsedPercent) > 5 || prev.StableResetAt.IsZero() || w.PeriodDuration <= 0 {
		return false
	}
	due := time.Duration(r.DueJitterSec) * time.Second
	if !observed.Before(prev.StableResetAt.Add(-due)) || prev.StableResetAt.Sub(observed) > 24*time.Hour {
		return false
	}
	delta := w.ResetAt.Sub(observed.Add(w.PeriodDuration))
	if delta < 0 {
		delta = -delta
	}
	return delta <= time.Duration(r.ClockJitterSec)*time.Second
}
