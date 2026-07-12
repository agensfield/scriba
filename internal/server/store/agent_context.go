package store

import (
	"context"
	"fmt"
	"time"

	"github.com/agensfield/scriba/internal/budget"
	"github.com/agensfield/scriba/internal/budgetadapter"
	"github.com/agensfield/scriba/internal/policy"
	"github.com/agensfield/scriba/internal/resetwatch"
)

const maxRecentPolicyEvents = 100

// AgentEventRecord is the minimized policy-event data exposed to read-only
// context builders. Account, policy, semantic-key, and delivery fields stay
// behind the store boundary.
type AgentEventRecord struct {
	ID                     string
	Kind                   policy.EventKind
	ProviderID             string
	WindowKey              budget.WindowKey
	Checkpoint             int
	UsedPercent            *float64
	RemainingPercentPoints *float64
	ResetKind              string
	PreviousResetAt        *time.Time
	ResetAt                *time.Time
	AvailableCount         *int
	ExpiresAt              *time.Time
	DetectedAt             time.Time
}

// LoadAgentEvents returns newest-first policy events with a stable tie
// break. The result is always bounded to at most 100 rows.
func (s *Store) LoadAgentEvents(ctx context.Context, providerID, accountRef string, limit int) ([]AgentEventRecord, error) {
	if providerID == "" || accountRef == "" {
		return nil, fmt.Errorf("agent event provider and account are required")
	}
	if limit <= 0 || limit > maxRecentPolicyEvents {
		limit = maxRecentPolicyEvents
	}
	rows, err := s.db.QueryContext(ctx, `
select id,event_kind,rule_kind,provider_id,payload_version,payload_json,detected_at
from policy_events
where provider_id = ? and account_ref = ?
order by detected_at desc,id desc
limit ?`, providerID, accountRef, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	events := make([]AgentEventRecord, 0, limit)
	for rows.Next() {
		var id, legacyKind, ruleKind, providerID, payloadJSON, detectedAt string
		var payloadVersion int
		if err := rows.Scan(&id, &legacyKind, &ruleKind, &providerID, &payloadVersion, &payloadJSON, &detectedAt); err != nil {
			return nil, err
		}
		event, err := MinimizeAgentEvent(id, legacyKind, ruleKind, providerID, payloadVersion, payloadJSON, parseDBTime(detectedAt))
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func MinimizeAgentEvent(id, legacyKind, ruleKind, providerID string, payloadVersion int, payloadJSON string, detectedAt time.Time) (AgentEventRecord, error) {
	event := AgentEventRecord{ID: id, Kind: policy.EventKind(ruleKind), ProviderID: providerID, DetectedAt: detectedAt}
	payload, err := DecodeOutboxPayload(OutboxMessage{EventKind: legacyKind, EventID: id, PayloadVersion: payloadVersion, PayloadJSON: payloadJSON})
	if err != nil {
		return AgentEventRecord{}, fmt.Errorf("decode policy event %s: %w", id, err)
	}
	windowKey := func(label string) error {
		key, ok := budgetadapter.WindowKey(providerID, label)
		if !ok {
			return fmt.Errorf("unsupported policy event window label")
		}
		event.WindowKey = key
		return nil
	}
	ptrTime := func(value time.Time) *time.Time {
		if value.IsZero() {
			return nil
		}
		return &value
	}
	switch value := payload.(type) {
	case resetwatch.WarningEvent:
		if event.Kind != policy.EventRemainingCheckpoint {
			return AgentEventRecord{}, fmt.Errorf("policy event kind mismatch")
		}
		if err := windowKey(value.Label); err != nil {
			return AgentEventRecord{}, err
		}
		event.Checkpoint = value.ThresholdRemaining
		event.UsedPercent = &value.UsedPercent
		event.RemainingPercentPoints = &value.RemainingPercent
		event.ResetAt = ptrTime(value.ResetAt)
	case resetwatch.Event:
		if event.Kind != policy.EventResetTransition {
			return AgentEventRecord{}, fmt.Errorf("policy event kind mismatch")
		}
		if err := windowKey(value.PrimaryTriggerLabel); err != nil {
			return AgentEventRecord{}, err
		}
		event.ResetKind = value.ResetKind
		event.PreviousResetAt = ptrTime(value.PreviousResetAt)
		event.ResetAt = ptrTime(value.CurrentResetAt)
	case resetwatch.ResetGrantEvent:
		if event.Kind != policy.EventGrantAvailable {
			return AgentEventRecord{}, fmt.Errorf("policy event kind mismatch")
		}
		event.AvailableCount = &value.AvailableCount
		event.ExpiresAt = ptrTime(value.ExpiresAt)
	case resetwatch.GrantExpiryWarning:
		if event.Kind != policy.EventGrantExpiryCheckpoint {
			return AgentEventRecord{}, fmt.Errorf("policy event kind mismatch")
		}
		event.Checkpoint = value.ThresholdDays
		event.ExpiresAt = ptrTime(value.ExpiresAt)
	default:
		return AgentEventRecord{}, fmt.Errorf("unsupported policy event payload %T", payload)
	}
	return event, nil
}
