package store

import (
	"context"
	"time"
)

const maxRecentPolicyEvents = 100

// AgentEventRecord is the minimized policy-event data exposed to read-only
// context builders. Account, policy, semantic-key, and delivery fields stay
// behind the store boundary.
type AgentEventRecord struct {
	ID             string
	EventKind      string
	ProviderID     string
	PayloadVersion int
	PayloadJSON    string
	DetectedAt     time.Time
}

// LoadAgentEvents returns newest-first policy events with a stable tie
// break. The result is always bounded to at most 100 rows.
func (s *Store) LoadAgentEvents(ctx context.Context, limit int) ([]AgentEventRecord, error) {
	if limit <= 0 || limit > maxRecentPolicyEvents {
		limit = maxRecentPolicyEvents
	}
	rows, err := s.db.QueryContext(ctx, `
select id,event_kind,provider_id,payload_version,payload_json,detected_at
from policy_events
order by detected_at desc,id desc
limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	events := make([]AgentEventRecord, 0, limit)
	for rows.Next() {
		var event AgentEventRecord
		var detectedAt string
		if err := rows.Scan(
			&event.ID, &event.EventKind, &event.ProviderID, &event.PayloadVersion,
			&event.PayloadJSON, &detectedAt,
		); err != nil {
			return nil, err
		}
		event.DetectedAt = parseDBTime(detectedAt)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}
