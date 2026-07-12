package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/policy"
	"github.com/agensfield/scriba/internal/resetwatch"
)

const currentPolicyRevision = "current-v1"

var ErrStaleObservation = errors.New("stale observation")

type CodexPollInput struct {
	Observation        resetwatch.Observation
	NotificationTarget string
	ResetOptions       resetwatch.Options
	CommittedAt        time.Time
}

type CodexPollResult struct {
	Bootstrap                bool
	LegacyDecision           resetwatch.Decision
	PolicyEvents             []policy.Event
	ResetEvents              []resetwatch.Event
	WarningEvents            []resetwatch.WarningEvent
	GrantExpiryWarningEvents []resetwatch.GrantExpiryWarning
	ResetGrantEvents         []resetwatch.ResetGrantEvent
}

// ApplyCodexPoll evaluates and persists a complete Codex poll as one atomic unit.
// The returned events are exactly those newly inserted by this call.
func (s *Store) ApplyCodexPoll(ctx context.Context, input CodexPollInput) (CodexPollResult, error) {
	var empty CodexPollResult
	obs := input.Observation
	if obs.Account.Ref == "" || obs.ObservedAt.IsZero() {
		return empty, errors.New("codex poll requires account ref and observed at")
	}
	if input.CommittedAt.IsZero() {
		return empty, errors.New("codex poll requires committed at")
	}
	obs.ProviderID = providerID(obs.ProviderID)
	if obs.ProviderID != resetwatch.ProviderCodex {
		return empty, fmt.Errorf("codex poll requires provider %q", resetwatch.ProviderCodex)
	}
	cfg := policy.CurrentPreset()
	configJSON, err := json.Marshal(cfg)
	if err != nil {
		return empty, err
	}
	hash := sha256.Sum256(configJSON)
	configHash := hex.EncodeToString(hash[:])

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer func() { _ = tx.Rollback() }()

	latest, ok, err := latestObservationAt(ctx, tx, obs.ProviderID, obs.Account.Ref)
	if err != nil {
		return empty, err
	}
	if ok && obs.ObservedAt.Equal(latest) {
		replay, replayErr := exactObservationReplay(ctx, tx, obs)
		if replayErr != nil {
			return empty, replayErr
		}
		if replay {
			return empty, nil
		}
		return empty, fmt.Errorf("%w: conflicting observation at %s", ErrStaleObservation, latest.Format(time.RFC3339Nano))
	}
	if ok && obs.ObservedAt.Before(latest) {
		return empty, fmt.Errorf("%w: observed at %s precedes %s", ErrStaleObservation, obs.ObservedAt.UTC().Format(time.RFC3339Nano), latest.Format(time.RFC3339Nano))
	}
	legacy, err := loadWindowStatesTx(ctx, tx, obs.Account.Ref)
	if err != nil {
		return empty, err
	}
	previous, err := loadPolicyStatesTx(ctx, tx, obs.ProviderID, obs.Account.Ref, currentPolicyRevision, configHash)
	if err != nil {
		return empty, err
	}
	bootstrap := len(previous) == 0
	in := policyInput(obs, previous, bootstrap)
	result, err := policy.Evaluate(cfg, in)
	if err != nil {
		return empty, err
	}
	if err = upsertPollAccount(ctx, tx, obs, input.CommittedAt); err != nil {
		return empty, err
	}
	if err = savePollObservation(ctx, tx, obs, input.CommittedAt); err != nil {
		return empty, err
	}
	// Keep the v6 read model current for compatibility while policy v1 owns emission.
	resetOptions := input.ResetOptions
	if resetOptions.JokeChooser == nil {
		resetOptions.JokeChooser = resetwatch.DefaultOptions().JokeChooser
	}
	decision := resetwatch.Decide(obs, legacy, resetOptions)
	if err = upsertPollWindowStates(ctx, tx, decision.States, input.CommittedAt); err != nil {
		return empty, err
	}
	available := len(resetwatch.ResetGrantEventCandidates(obs))
	if obs.ResetGrants.AvailableCount != nil {
		available = *obs.ResetGrants.AvailableCount
	}
	if err = upsertPollGrantTracking(ctx, tx, obs, available, input.CommittedAt); err != nil {
		return empty, err
	}
	if err = persistPolicyStates(ctx, tx, obs, result, currentPolicyRevision, configHash, input.CommittedAt); err != nil {
		return empty, err
	}
	inserted, err := persistPolicyEvents(ctx, tx, obs, legacy, result.Events, currentPolicyRevision, configHash, input.NotificationTarget, resetOptions.JokeChooser, input.CommittedAt)
	if err != nil {
		return empty, err
	}
	if s.applyCodexPollFault != nil {
		if err = s.applyCodexPollFault("before_commit"); err != nil {
			return empty, err
		}
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	inserted.Bootstrap = bootstrap
	inserted.LegacyDecision = decision
	return inserted, nil
}

func exactObservationReplay(ctx context.Context, tx *sql.Tx, obs resetwatch.Observation) (bool, error) {
	id := ObservationID(obs)
	var snapshot, label, email, plan string
	err := tx.QueryRowContext(ctx, `select o.snapshot_json,a.label,a.email,a.plan from limit_observations o join accounts a on a.provider_id=o.provider_id and a.account_ref=o.account_ref where o.id=? and o.provider_id=? and o.account_ref=? and o.observed_at=?`, id, obs.ProviderID, obs.Account.Ref, formatTime(obs.ObservedAt)).Scan(&snapshot, &label, &email, &plan)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if snapshot != string(obs.SnapshotJSON) || label != obs.Account.Label || email != obs.Account.Email || plan != obs.Account.Plan {
		return false, nil
	}
	rows, err := tx.QueryContext(ctx, `select label,used_percent,reset_at,period_duration_ms from observed_windows where observation_id=? order by label`, id)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	type storedWindow struct {
		label  string
		used   sql.NullFloat64
		reset  string
		period sql.NullInt64
	}
	var got []storedWindow
	for rows.Next() {
		var w storedWindow
		if err = rows.Scan(&w.label, &w.used, &w.reset, &w.period); err != nil {
			return false, err
		}
		got = append(got, w)
	}
	if err = rows.Err(); err != nil {
		return false, err
	}
	want := append([]resetwatch.Window(nil), obs.Windows...)
	slices.SortFunc(want, func(a, b resetwatch.Window) int { return strings.Compare(a.Label, b.Label) })
	if len(got) != len(want) {
		return false, nil
	}
	for i, w := range want {
		if got[i].label != w.Label || got[i].reset != formatTime(w.ResetAt) || !equalNullableFloat(got[i].used, w.UsedPercent) || !equalNullableInt(got[i].period, w.PeriodDurationMs) {
			return false, nil
		}
	}
	return true, nil
}

func equalNullableFloat(g sql.NullFloat64, want *float64) bool {
	return (want == nil && !g.Valid) || (want != nil && g.Valid && g.Float64 == *want)
}
func equalNullableInt(g sql.NullInt64, want *int64) bool {
	return (want == nil && !g.Valid) || (want != nil && g.Valid && g.Int64 == *want)
}

func latestObservationAt(ctx context.Context, tx *sql.Tx, provider, account string) (time.Time, bool, error) {
	var value sql.NullString
	err := tx.QueryRowContext(ctx, `select max(observed_at) from limit_observations where provider_id=? and account_ref=?`, provider, account).Scan(&value)
	if err != nil || !value.Valid {
		return time.Time{}, false, err
	}
	return parseDBTime(value.String), true, nil
}

func loadWindowStatesTx(ctx context.Context, tx *sql.Tx, account string) (map[string]resetwatch.WindowState, error) {
	rows, err := tx.QueryContext(ctx, `select account_ref,label,stable_reset_at,last_seen_reset_at,last_observed_at,last_snapshot_json from limit_windows where account_ref=?`, account)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]resetwatch.WindowState{}
	for rows.Next() {
		var v resetwatch.WindowState
		var stable, seen, observed, snapshot string
		if err = rows.Scan(&v.AccountRef, &v.Label, &stable, &seen, &observed, &snapshot); err != nil {
			return nil, err
		}
		v.StableResetAt, v.LastSeenResetAt, v.LastObservedAt, v.LastSnapshotJSON = parseDBTime(stable), parseDBTime(seen), parseDBTime(observed), []byte(snapshot)
		out[resetwatch.StateKey(v.AccountRef, v.Label)] = v
	}
	return out, rows.Err()
}

func loadPolicyStatesTx(ctx context.Context, tx *sql.Tx, provider, account, revision, hash string) (map[policy.StateKey]policy.State, error) {
	rows, err := tx.QueryContext(ctx, `select rule_id,subject_key,state_json from policy_states where provider_id=? and account_ref=? and policy_revision=? and config_hash=?`, provider, account, revision, hash)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[policy.StateKey]policy.State{}
	for rows.Next() {
		var rule, subject string
		var raw []byte
		var state policy.State
		if err = rows.Scan(&rule, &subject, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &state); err != nil {
			return nil, fmt.Errorf("decode policy state %s/%s: %w", rule, subject, err)
		}
		if subject != "_observation" && emptyPolicyState(state) {
			continue
		}
		out[policy.StateKey{RuleID: rule, Subject: subject}] = state
	}
	return out, rows.Err()
}

func emptyPolicyState(v policy.State) bool {
	return v.StableResetAt.IsZero() && v.LastResetAt.IsZero() && v.LastObservedAt.IsZero() && v.LastUsedPercent == nil && len(v.ReachedCheckpoints) == 0 && len(v.KnownGrantIdentities) == 0 && v.AvailableGrantCount == 0 && v.GrantExpiresAt.IsZero()
}

func policyInput(obs resetwatch.Observation, previous map[policy.StateKey]policy.State, bootstrap bool) policy.Input {
	windows := make([]policy.WindowObservation, 0, len(obs.Windows))
	for _, w := range obs.Windows {
		key := policyWindowKey(w.Label)
		if key == "" {
			continue
		}
		var period time.Duration
		if w.PeriodDurationMs != nil {
			period = time.Duration(*w.PeriodDurationMs) * time.Millisecond
		}
		windows = append(windows, policy.WindowObservation{Key: key, Label: w.Label, UsedPercent: w.UsedPercent, ResetAt: w.ResetAt, PeriodDuration: period})
	}
	grants := make([]policy.GrantObservation, 0, len(obs.ResetGrants.Credits))
	for _, g := range obs.ResetGrants.Credits {
		grants = append(grants, policy.GrantObservation{ID: g.ID, Status: g.Status, Title: g.Title, ResetType: g.ResetType, GrantedAt: g.GrantedAt, ExpiresAt: g.ExpiresAt})
	}
	return policy.Input{ProviderID: obs.ProviderID, AccountRef: obs.Account.Ref, ObservedAt: obs.ObservedAt, Windows: windows, Grants: grants, AvailableCount: obs.ResetGrants.AvailableCount, Previous: previous, Bootstrap: bootstrap}
}

func policyWindowKey(label string) string {
	switch label {
	case resetwatch.LabelFiveHour:
		return "primary.five_hour"
	case resetwatch.LabelWeeklyLimit:
		return "primary.weekly"
	case resetwatch.LabelSparkFive:
		return "spark.five_hour"
	case resetwatch.LabelSparkWeekly:
		return "spark.weekly"
	case resetwatch.LabelReviewFive:
		return "review.five_hour"
	case resetwatch.LabelReviewWeek:
		return "review.weekly"
	default:
		return ""
	}
}

func policyRuleKind(cfg policy.Config, ruleID string) policy.RuleKind {
	for _, r := range cfg.Rules {
		if r.ID == ruleID {
			return r.Kind
		}
	}
	return ""
}

func upsertPollAccount(ctx context.Context, tx *sql.Tx, obs resetwatch.Observation, committedAt time.Time) error {
	_, err := tx.ExecContext(ctx, `insert into accounts(account_ref,provider_id,label,email,plan,updated_at) values(?,?,?,?,?,?) on conflict(account_ref) do update set provider_id=excluded.provider_id,label=excluded.label,email=excluded.email,plan=excluded.plan,updated_at=excluded.updated_at`, obs.Account.Ref, obs.ProviderID, obs.Account.Label, obs.Account.Email, obs.Account.Plan, formatTime(committedAt))
	return err
}

func savePollObservation(ctx context.Context, tx *sql.Tx, obs resetwatch.Observation, committedAt time.Time) error {
	id := ObservationID(obs)
	if _, err := tx.ExecContext(ctx, `insert into limit_observations(id,provider_id,account_ref,observed_at,snapshot_json,created_at) values(?,?,?,?,?,?) on conflict(id) do nothing`, id, obs.ProviderID, obs.Account.Ref, formatTime(obs.ObservedAt), string(obs.SnapshotJSON), formatTime(committedAt)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from observed_windows where observation_id=?`, id); err != nil {
		return err
	}
	for _, w := range obs.Windows {
		if _, err := tx.ExecContext(ctx, `insert into observed_windows(observation_id,label,used_percent,reset_at,period_duration_ms) values(?,?,?,?,?)`, id, w.Label, nullableFloat(w.UsedPercent), formatTime(w.ResetAt), nullableInt64(w.PeriodDurationMs)); err != nil {
			return err
		}
	}
	return nil
}

func upsertPollWindowStates(ctx context.Context, tx *sql.Tx, states []resetwatch.WindowState, committedAt time.Time) error {
	for _, state := range states {
		if _, err := tx.ExecContext(ctx, `insert into limit_windows(account_ref,label,stable_reset_at,last_seen_reset_at,last_observed_at,last_snapshot_json,updated_at) values(?,?,?,?,?,?,?) on conflict(account_ref,label) do update set stable_reset_at=excluded.stable_reset_at,last_seen_reset_at=excluded.last_seen_reset_at,last_observed_at=excluded.last_observed_at,last_snapshot_json=excluded.last_snapshot_json,updated_at=excluded.updated_at`, state.AccountRef, state.Label, formatTime(state.StableResetAt), formatTime(state.LastSeenResetAt), formatTime(state.LastObservedAt), string(state.LastSnapshotJSON), formatTime(committedAt)); err != nil {
			return err
		}
	}
	return nil
}

func upsertPollGrantTracking(ctx context.Context, tx *sql.Tx, obs resetwatch.Observation, available int, committedAt time.Time) error {
	now := formatTime(committedAt)
	_, err := tx.ExecContext(ctx, `insert into reset_grant_tracking_state(account_ref,provider_id,available_count,last_observed_at,created_at,updated_at) values(?,?,?,?,?,?) on conflict(account_ref) do update set provider_id=excluded.provider_id,available_count=excluded.available_count,last_observed_at=excluded.last_observed_at,updated_at=excluded.updated_at`, obs.Account.Ref, obs.ProviderID, available, formatTime(obs.ObservedAt), now, now)
	return err
}

func persistPolicyStates(ctx context.Context, tx *sql.Tx, obs resetwatch.Observation, result policy.Result, revision, hash string, committedAt time.Time) error {
	now := formatTime(committedAt)
	explanations := map[policy.StateKey][]policy.Explanation{}
	for _, x := range result.Explanations {
		k := policy.StateKey{RuleID: x.RuleID, Subject: x.Subject}
		explanations[k] = append(explanations[k], x)
	}
	cfg := policy.CurrentPreset()
	keys := map[policy.StateKey]bool{}
	for key := range result.States {
		keys[key] = true
	}
	for key := range explanations {
		keys[key] = true
	}
	for key := range keys {
		state := result.States[key]
		stateJSON, err := json.Marshal(state)
		if err != nil {
			return err
		}
		evalJSON, err := json.Marshal(explanations[key])
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `insert into policy_states(rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,state_json,evaluation_json,observed_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?) on conflict(provider_id,account_ref,policy_revision,config_hash,rule_id,subject_key) do update set rule_kind=excluded.rule_kind,state_json=excluded.state_json,evaluation_json=excluded.evaluation_json,observed_at=excluded.observed_at,updated_at=excluded.updated_at`, key.RuleID, key.Subject, policyRuleKind(cfg, key.RuleID), obs.ProviderID, obs.Account.Ref, revision, hash, string(stateJSON), string(evalJSON), formatTime(obs.ObservedAt), now, now)
		if err != nil {
			return err
		}
	}
	return nil
}

func persistPolicyEvents(ctx context.Context, tx *sql.Tx, obs resetwatch.Observation, legacy map[string]resetwatch.WindowState, events []policy.Event, revision, hash, target string, chooser resetwatch.JokeChooser, committedAt time.Time) (CodexPollResult, error) {
	var inserted CodexPollResult
	for _, event := range events {
		kind, payload, added, err := insertPolicyLegacyEvent(ctx, tx, obs, legacy, event, target, chooser, committedAt)
		if err != nil {
			return CodexPollResult{}, err
		}
		payloadJSON, err := EncodeOutboxPayload(kind, payload)
		if err != nil {
			return CodexPollResult{}, err
		}
		semantic := revision + "\x00" + hash + "\x00" + event.RuleID + "\x00" + event.Subject + "\x00" + event.ID
		digest := sha256.Sum256([]byte(semantic))
		id := "policy_" + hex.EncodeToString(digest[:16])
		_, err = tx.ExecContext(ctx, `insert into policy_events(id,semantic_key,event_kind,semantic_event_id,rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,payload_version,payload_json,detected_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(id) do nothing`, id, semantic, kind, event.ID, event.RuleID, event.Subject, event.Kind, obs.ProviderID, obs.Account.Ref, revision, hash, 1, payloadJSON, formatTime(event.DetectedAt), formatTime(committedAt))
		if err != nil {
			return CodexPollResult{}, err
		}
		var matches int
		err = tx.QueryRowContext(ctx, `select count(*) from policy_events where id=? and semantic_key=? and event_kind=? and semantic_event_id=? and rule_id=? and subject_key=? and rule_kind=? and provider_id=? and account_ref=? and policy_revision=? and config_hash=? and payload_version=1 and payload_json=? and detected_at=?`, id, semantic, kind, event.ID, event.RuleID, event.Subject, event.Kind, obs.ProviderID, obs.Account.Ref, revision, hash, payloadJSON, formatTime(event.DetectedAt)).Scan(&matches)
		if err != nil {
			return CodexPollResult{}, err
		}
		if matches != 1 {
			return CodexPollResult{}, errors.New("conflicting policy event semantic duplicate")
		}
		if !added {
			continue
		}
		inserted.PolicyEvents = append(inserted.PolicyEvents, event)
		switch v := payload.(type) {
		case resetwatch.Event:
			inserted.ResetEvents = append(inserted.ResetEvents, v)
		case resetwatch.WarningEvent:
			inserted.WarningEvents = append(inserted.WarningEvents, v)
		case resetwatch.GrantExpiryWarning:
			inserted.GrantExpiryWarningEvents = append(inserted.GrantExpiryWarningEvents, v)
		case resetwatch.ResetGrantEvent:
			inserted.ResetGrantEvents = append(inserted.ResetGrantEvents, v)
		}
	}
	return inserted, nil
}

func insertPolicyLegacyEvent(ctx context.Context, tx *sql.Tx, obs resetwatch.Observation, legacy map[string]resetwatch.WindowState, event policy.Event, target string, chooser resetwatch.JokeChooser, committedAt time.Time) (string, any, bool, error) {
	switch event.Kind {
	case policy.EventResetTransition:
		previous := legacy[resetwatch.StateKey(obs.Account.Ref, event.LegacyLabel)]
		legacyEvent := resetwatch.Event{ID: event.ID, ProviderID: obs.ProviderID, Account: obs.Account, PrimaryTriggerLabel: event.LegacyLabel, SecondaryTriggerLabels: event.SecondaryLegacyLabels, ResetKind: event.ResetKind, PreviousResetAt: event.PreviousResetAt, CurrentResetAt: event.ResetAt, PreviousSnapshotJSON: previous.LastSnapshotJSON, CurrentSnapshotJSON: obs.SnapshotJSON, DetectedAt: event.DetectedAt}
		legacyEvent.JokeID = chooser.Choose(legacyEvent)
		added, err := insertResetEventTx(ctx, tx, legacyEvent, target, committedAt)
		return "reset", legacyEvent, added, err
	case policy.EventRemainingCheckpoint:
		v := resetwatch.WarningEvent{ID: event.ID, ProviderID: obs.ProviderID, Account: obs.Account, Label: event.LegacyLabel, ThresholdRemaining: event.Checkpoint, UsedPercent: event.UsedPercent, RemainingPercent: event.RemainingPercent, ResetAt: event.ResetAt, SnapshotJSON: obs.SnapshotJSON, DetectedAt: event.DetectedAt}
		added, err := insertWarningEventTx(ctx, tx, v, target, committedAt)
		return "limit_warning", v, added, err
	case policy.EventGrantAvailable:
		v := resetwatch.ResetGrantEvent{ID: event.ID, ProviderID: obs.ProviderID, Account: obs.Account, CreditID: event.Grant.ID, CreditTitle: event.Grant.Title, ResetType: event.Grant.ResetType, GrantedAt: event.Grant.GrantedAt, ExpiresAt: event.Grant.ExpiresAt, AvailableCount: event.AvailableCount, SnapshotJSON: obs.SnapshotJSON, DetectedAt: event.DetectedAt}
		added, err := insertResetGrantEventTx(ctx, tx, v, target, committedAt)
		return "reset_grant", v, added, err
	case policy.EventGrantExpiryCheckpoint:
		v := resetwatch.GrantExpiryWarning{ID: event.ID, ProviderID: obs.ProviderID, Account: obs.Account, CreditID: event.Grant.ID, CreditTitle: event.Grant.Title, ThresholdDays: event.Checkpoint, ExpiresAt: event.Grant.ExpiresAt, SnapshotJSON: obs.SnapshotJSON, DetectedAt: event.DetectedAt}
		added, err := insertGrantWarningEventTx(ctx, tx, v, target, committedAt)
		return "reset_grant_warning", v, added, err
	default:
		return "", nil, false, fmt.Errorf("unsupported policy event kind %q", event.Kind)
	}
}

func insertResetEventTx(ctx context.Context, tx *sql.Tx, v resetwatch.Event, target string, committedAt time.Time) (bool, error) {
	secondary, err := json.Marshal(v.SecondaryTriggerLabels)
	if err != nil {
		return false, err
	}
	r, err := tx.ExecContext(ctx, `insert into reset_events(id,provider_id,account_ref,account_label,account_email,account_plan,primary_trigger_label,secondary_trigger_labels_json,reset_kind,previous_reset_at,current_reset_at,previous_snapshot_json,current_snapshot_json,joke_id,detected_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(id) do nothing`, v.ID, v.ProviderID, v.Account.Ref, v.Account.Label, v.Account.Email, v.Account.Plan, v.PrimaryTriggerLabel, string(secondary), v.ResetKind, formatTime(v.PreviousResetAt), formatTime(v.CurrentResetAt), string(v.PreviousSnapshotJSON), string(v.CurrentSnapshotJSON), v.JokeID, formatTime(v.DetectedAt), formatTime(committedAt))
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		var matches int
		err = tx.QueryRowContext(ctx, `select count(*) from reset_events where id=? and provider_id=? and account_ref=? and account_label=? and account_email=? and account_plan=? and primary_trigger_label=? and secondary_trigger_labels_json=? and reset_kind=? and previous_reset_at=? and current_reset_at=? and previous_snapshot_json=? and current_snapshot_json=? and joke_id=? and detected_at=?`, v.ID, v.ProviderID, v.Account.Ref, v.Account.Label, v.Account.Email, v.Account.Plan, v.PrimaryTriggerLabel, string(secondary), v.ResetKind, formatTime(v.PreviousResetAt), formatTime(v.CurrentResetAt), string(v.PreviousSnapshotJSON), string(v.CurrentSnapshotJSON), v.JokeID, formatTime(v.DetectedAt)).Scan(&matches)
		if err != nil {
			return false, err
		}
		if matches != 1 {
			return false, errors.New("conflicting reset event semantic duplicate")
		}
	}
	if err = enqueuePollEvent(ctx, tx, "reset", v.ID, v.Account.Ref, target, v, committedAt); err != nil {
		return false, err
	}
	return n == 1, nil
}

func insertWarningEventTx(ctx context.Context, tx *sql.Tx, v resetwatch.WarningEvent, target string, committedAt time.Time) (bool, error) {
	r, err := tx.ExecContext(ctx, `insert into limit_warning_events(id,provider_id,account_ref,account_label,account_email,account_plan,label,threshold_remaining,used_percent,remaining_percent,reset_at,snapshot_json,detected_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(id) do nothing`, v.ID, v.ProviderID, v.Account.Ref, v.Account.Label, v.Account.Email, v.Account.Plan, v.Label, v.ThresholdRemaining, v.UsedPercent, v.RemainingPercent, formatTime(v.ResetAt), string(v.SnapshotJSON), formatTime(v.DetectedAt), formatTime(committedAt))
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		var matches int
		err = tx.QueryRowContext(ctx, `select count(*) from limit_warning_events where id=? and provider_id=? and account_ref=? and account_label=? and account_email=? and account_plan=? and label=? and threshold_remaining=? and used_percent=? and remaining_percent=? and reset_at=? and snapshot_json=? and detected_at=?`, v.ID, v.ProviderID, v.Account.Ref, v.Account.Label, v.Account.Email, v.Account.Plan, v.Label, v.ThresholdRemaining, v.UsedPercent, v.RemainingPercent, formatTime(v.ResetAt), string(v.SnapshotJSON), formatTime(v.DetectedAt)).Scan(&matches)
		if err != nil {
			return false, err
		}
		if matches != 1 {
			return false, errors.New("conflicting warning event semantic duplicate")
		}
	}
	if err = enqueuePollEvent(ctx, tx, "limit_warning", v.ID, v.Account.Ref, target, v, committedAt); err != nil {
		return false, err
	}
	return n == 1, nil
}

func insertGrantWarningEventTx(ctx context.Context, tx *sql.Tx, v resetwatch.GrantExpiryWarning, target string, committedAt time.Time) (bool, error) {
	r, err := tx.ExecContext(ctx, `insert into reset_grant_warning_events(id,provider_id,account_ref,account_label,account_email,account_plan,credit_id,credit_title,threshold_days,expires_at,snapshot_json,detected_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(id) do nothing`, v.ID, v.ProviderID, v.Account.Ref, v.Account.Label, v.Account.Email, v.Account.Plan, v.CreditID, v.CreditTitle, v.ThresholdDays, formatTime(v.ExpiresAt), string(v.SnapshotJSON), formatTime(v.DetectedAt), formatTime(committedAt))
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		var matches int
		err = tx.QueryRowContext(ctx, `select count(*) from reset_grant_warning_events where id=? and provider_id=? and account_ref=? and account_label=? and account_email=? and account_plan=? and credit_id=? and credit_title=? and threshold_days=? and expires_at=? and snapshot_json=? and detected_at=?`, v.ID, v.ProviderID, v.Account.Ref, v.Account.Label, v.Account.Email, v.Account.Plan, v.CreditID, v.CreditTitle, v.ThresholdDays, formatTime(v.ExpiresAt), string(v.SnapshotJSON), formatTime(v.DetectedAt)).Scan(&matches)
		if err != nil {
			return false, err
		}
		if matches != 1 {
			return false, errors.New("conflicting grant warning semantic duplicate")
		}
	}
	if err = enqueuePollEvent(ctx, tx, "reset_grant_warning", v.ID, v.Account.Ref, target, v, committedAt); err != nil {
		return false, err
	}
	return n == 1, nil
}

func insertResetGrantEventTx(ctx context.Context, tx *sql.Tx, v resetwatch.ResetGrantEvent, target string, committedAt time.Time) (bool, error) {
	r, err := tx.ExecContext(ctx, `insert into reset_grant_events(id,provider_id,account_ref,account_label,account_email,account_plan,credit_id,credit_title,reset_type,granted_at,expires_at,available_count,snapshot_json,detected_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(id) do nothing`, v.ID, v.ProviderID, v.Account.Ref, v.Account.Label, v.Account.Email, v.Account.Plan, v.CreditID, v.CreditTitle, v.ResetType, formatTime(v.GrantedAt), formatTime(v.ExpiresAt), v.AvailableCount, string(v.SnapshotJSON), formatTime(v.DetectedAt), formatTime(committedAt))
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		var matches int
		err = tx.QueryRowContext(ctx, `select count(*) from reset_grant_events where id=? and provider_id=? and account_ref=? and account_label=? and account_email=? and account_plan=? and credit_id=? and credit_title=? and reset_type=? and granted_at=? and expires_at=? and available_count=? and snapshot_json=? and detected_at=?`, v.ID, v.ProviderID, v.Account.Ref, v.Account.Label, v.Account.Email, v.Account.Plan, v.CreditID, v.CreditTitle, v.ResetType, formatTime(v.GrantedAt), formatTime(v.ExpiresAt), v.AvailableCount, string(v.SnapshotJSON), formatTime(v.DetectedAt)).Scan(&matches)
		if err != nil {
			return false, err
		}
		if matches != 1 {
			return false, errors.New("conflicting reset grant semantic duplicate")
		}
	}
	if err = enqueuePollEvent(ctx, tx, "reset_grant", v.ID, v.Account.Ref, target, v, committedAt); err != nil {
		return false, err
	}
	return n == 1, nil
}

func enqueuePollEvent(ctx context.Context, tx *sql.Tx, kind, id, account, target string, event any, committedAt time.Time) error {
	if target == "" {
		return nil
	}
	payload, err := EncodeOutboxPayload(kind, event)
	if err != nil {
		return err
	}
	return EnqueueOutbox(ctx, tx, OutboxEnqueue{EventKind: kind, Source: "scriba-v7", AccountRef: account, EventID: id, Target: target, PayloadVersion: 1, PayloadJSON: payload}, committedAt)
}
