package agentcontext

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/server/store"
)

const maxEventPageSize = 100

func (s *Service) Events(ctx context.Context, request EventPageRequest) (EventPage, error) {
	if err := ctx.Err(); err != nil {
		return EventPage{}, err
	}
	if request.Limit < 1 || request.Limit > maxEventPageSize {
		return EventPage{}, pageError("invalid_limit")
	}
	if request.Mode != "replay" && request.Mode != "latest" && request.Mode != "capture" {
		return EventPage{}, pageError("invalid_mode")
	}
	if request.Mode != "replay" && request.Cursor != "" {
		return EventPage{}, pageError("invalid_cursor")
	}
	after := int64(0)
	var err error
	if request.Mode == "replay" {
		after, err = parseEventCursor(request.Cursor)
		if err != nil {
			return EventPage{}, err
		}
	}
	now := time.Now().UTC()
	if s.config.Clock != nil {
		now = s.config.Clock().UTC()
	}
	st, err := store.OpenReadOnlyContext(ctx, s.config.StorePath)
	if err != nil {
		if ctx.Err() != nil {
			return EventPage{}, ctx.Err()
		}
		if request.Account != "" {
			return EventPage{}, &AccountError{ReasonCode: "account_unavailable"}
		}
		return EventPage{}, pageError("read_error")
	}
	defer func() { _ = st.Close() }()
	account, ok, err := st.ResolveAccount(ctx, request.Account)
	if err != nil {
		if ctx.Err() != nil {
			return EventPage{}, ctx.Err()
		}
		if request.Account != "" {
			return EventPage{}, &AccountError{ReasonCode: "account_unavailable"}
		}
		return EventPage{}, pageError("read_error")
	}
	if !ok {
		if request.Account != "" {
			return EventPage{}, &AccountError{ReasonCode: "account_unavailable"}
		}
		return EventPage{}, pageError("events_unavailable")
	}
	if account.ProviderID != "codex" || account.Ref == "" {
		return EventPage{}, pageError("events_unavailable")
	}

	meta, err := st.LoadPolicyEventReplay(ctx, "codex", account.Ref, 0, 0, 1)
	if err != nil {
		if ctx.Err() != nil {
			return EventPage{}, ctx.Err()
		}
		return EventPage{}, pageError("read_error")
	}
	if request.Mode == "capture" {
		after = meta.HighWater
	}
	if request.Mode == "latest" {
		page, loadErr := st.LoadLatestPolicyEventReplay(ctx, "codex", account.Ref, meta.HighWater, request.Limit)
		if loadErr != nil {
			if ctx.Err() != nil {
				return EventPage{}, ctx.Err()
			}
			return EventPage{}, pageError("read_error")
		}
		return publicReplayPage(page, account.ID, now), nil
	}
	if after > meta.HighWater {
		return EventPage{}, pageError("cursor_future")
	}
	if cursorExpired(after, meta.PrunedThrough) {
		return EventPage{}, pageError("cursor_expired")
	}

	batch, loadErr := st.LoadPolicyEventReplay(ctx, "codex", account.Ref, after, meta.HighWater, request.Limit)
	if loadErr != nil {
		if ctx.Err() != nil {
			return EventPage{}, ctx.Err()
		}
		return EventPage{}, pageError("read_error")
	}
	if cursorExpired(after, batch.PrunedThrough) {
		return EventPage{}, pageError("cursor_expired")
	}
	return publicReplayPage(batch, account.ID, now), nil
}

func cursorExpired(after, prunedThrough int64) bool {
	return prunedThrough > 0 && after < prunedThrough
}

func pageError(reason string) error      { return &EventPageError{ReasonCode: reason} }
func formatEventCursor(seq int64) string { return fmt.Sprintf("v1.%016x", seq) }
func parseEventCursor(value string) (int64, error) {
	if len(value) != 19 || !strings.HasPrefix(value, "v1.") || value[3] > '7' {
		return 0, pageError("invalid_cursor")
	}
	digits := strings.TrimPrefix(value, "v1.")
	for _, r := range digits {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return 0, pageError("invalid_cursor")
		}
	}
	seq, err := strconv.ParseInt(digits, 16, 64)
	if err != nil || seq < 0 {
		return 0, pageError("invalid_cursor")
	}
	return seq, nil
}

func replayRecord(raw store.PolicyReplayEvent) (store.AgentEventRecord, bool) {
	if raw.PolicyEventID == "" {
		return store.AgentEventRecord{}, false
	}
	r, err := store.MinimizeAgentEvent(raw.PolicyEventID, raw.EventKind, raw.RuleKind, raw.ProviderID, raw.PayloadVersion, raw.PayloadJSON, raw.DetectedAt)
	return r, err == nil
}

func publicReplayPage(page store.PolicyReplayPage, accountID string, now time.Time) EventPage {
	out := EventPage{SchemaVersion: EventsSchemaVersion, GeneratedAt: now, AccountID: accountID, Events: []Event{}}
	for _, raw := range page.Events {
		if r, ok := replayRecord(raw); ok {
			if e, ok := minimize(r, accountID); ok {
				out.Events = append(out.Events, e)
			}
		}
	}
	out.Cursor = EventPageCursor{Next: formatEventCursor(page.NextCursor), HighWater: formatEventCursor(page.HighWater)}
	return out
}
