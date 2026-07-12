package agentcontext

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/budgetadapter"
	"github.com/agensfield/scriba/internal/cache"
	"github.com/agensfield/scriba/internal/resetwatch"
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
	profile, err := s.selectProfile(request.ProfileID)
	if err != nil {
		return EventPage{}, err
	}

	selected, available, err := s.selectedCodex(ctx, profile)
	if err != nil {
		if ctx.Err() != nil {
			return EventPage{}, ctx.Err()
		}
		return EventPage{}, pageError("read_error")
	}
	if !available || !selected.fromStore || selected.obs.Account.Ref == "" {
		return EventPage{}, pageError("events_unavailable")
	}
	st, err := store.OpenReadOnlyContext(ctx, s.config.StorePath)
	if err != nil {
		if ctx.Err() != nil {
			return EventPage{}, ctx.Err()
		}
		return EventPage{}, pageError("read_error")
	}
	defer func() { _ = st.Close() }()

	meta, err := st.LoadPolicyEventReplay(ctx, "codex", selected.obs.Account.Ref, 0, 0, 1)
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
		page, loadErr := st.LoadLatestPolicyEventReplay(ctx, "codex", selected.obs.Account.Ref, meta.HighWater, request.Limit)
		if loadErr != nil {
			if ctx.Err() != nil {
				return EventPage{}, ctx.Err()
			}
			return EventPage{}, pageError("read_error")
		}
		return publicReplayPage(page, profile, now), nil
	}
	if after > meta.HighWater {
		return EventPage{}, pageError("cursor_future")
	}
	if cursorExpired(after, meta.OldestAvailable, meta.HighWater) {
		return EventPage{}, pageError("cursor_expired")
	}

	batch, loadErr := st.LoadPolicyEventReplay(ctx, "codex", selected.obs.Account.Ref, after, meta.HighWater, request.Limit)
	if loadErr != nil {
		if ctx.Err() != nil {
			return EventPage{}, ctx.Err()
		}
		return EventPage{}, pageError("read_error")
	}
	if cursorExpired(after, batch.OldestAvailable, batch.HighWater) {
		return EventPage{}, pageError("cursor_expired")
	}
	return publicReplayPage(batch, profile, now), nil
}

func cursorExpired(after, oldest, high int64) bool {
	return (oldest > 0 && after < oldest-1) || (oldest == 0 && high > 0 && after < high)
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

func (s *Service) selectedCodex(ctx context.Context, profile string) (candidate, bool, error) {
	var cached candidate
	haveCache := false
	c, err := cache.OpenReadOnlyContext(ctx, s.config.CacheDir)
	if err == nil {
		snapshot, loadErr := c.LoadStatusSnapshotContext(ctx)
		_ = c.Close()
		if loadErr == nil && snapshot != nil {
			cached, haveCache = candidatesFromSnapshot(*snapshot)["codex"]
		}
	}
	if ctx.Err() != nil {
		return candidate{}, false, ctx.Err()
	}
	st, err := store.OpenReadOnlyContext(ctx, s.config.StorePath)
	if err != nil {
		if haveCache {
			return cached, true, nil
		}
		return candidate{}, false, err
	}
	defer func() { _ = st.Close() }()
	var o resetwatch.Observation
	var ok bool
	if len(s.config.ProfileIDs) > 0 {
		o, ok, err = st.LoadLatestObservationForProfile(ctx, profile)
	} else {
		o, ok, err = st.LoadLatestObservationForProvider(ctx, "codex")
	}
	if err != nil {
		return candidate{}, false, err
	}
	stored := candidate{}
	if ok && len(budgetadapter.FromResetwatch(o).Windows) > 0 {
		stored = candidate{obs: o, provenance: "provider-api", fromStore: true, grantAt: o.ObservedAt}
	}
	if len(s.config.ProfileIDs) > 0 {
		return stored, validCandidate(stored), nil
	}
	if validCandidate(stored) && (!haveCache || !stored.obs.ObservedAt.Before(cached.obs.ObservedAt)) {
		return stored, true, nil
	}
	return cached, haveCache, nil
}

func replayRecord(raw store.PolicyReplayEvent) (store.AgentEventRecord, bool) {
	if raw.PolicyEventID == "" {
		return store.AgentEventRecord{}, false
	}
	r, err := store.MinimizeAgentEvent(raw.PolicyEventID, raw.EventKind, raw.RuleKind, raw.ProviderID, raw.PayloadVersion, raw.PayloadJSON, raw.DetectedAt)
	return r, err == nil
}

func publicReplayPage(page store.PolicyReplayPage, profile string, now time.Time) EventPage {
	out := EventPage{SchemaVersion: EventsSchemaVersion, GeneratedAt: now, Events: []Event{}}
	for _, raw := range page.Events {
		if r, ok := replayRecord(raw); ok {
			if e, ok := minimize(r, profile); ok {
				out.Events = append(out.Events, e)
			}
		}
	}
	out.Cursor = EventPageCursor{Next: formatEventCursor(page.NextCursor), HighWater: formatEventCursor(page.HighWater)}
	return out
}
