package delivery

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agensfield/scriba/internal/budget"
	"github.com/agensfield/scriba/internal/radar"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
)

const SchemaVersion = "scriba.notification.v1"

type Envelope struct {
	SchemaVersion string          `json:"schemaVersion"`
	EventID       string          `json:"eventId"`
	EventKind     string          `json:"eventKind"`
	Source        string          `json:"source"`
	ProfileID     string          `json:"profileId,omitempty"`
	OccurredAt    time.Time       `json:"occurredAt"`
	Data          json.RawMessage `json:"data"`
}

func FromOutbox(message store.OutboxMessage) (Envelope, error) {
	payload, err := store.DecodeOutboxPayload(message)
	if err != nil {
		return Envelope{}, err
	}
	var occurredAt time.Time
	var data any
	switch event := payload.(type) {
	case resetwatch.Event:
		occurredAt = event.DetectedAt
		data = struct {
			AccountLabel           string    `json:"accountLabel,omitempty"`
			PrimaryTriggerLabel    string    `json:"primaryTriggerLabel"`
			SecondaryTriggerLabels []string  `json:"secondaryTriggerLabels"`
			ResetKind              string    `json:"resetKind"`
			PreviousResetAt        time.Time `json:"previousResetAt"`
			CurrentResetAt         time.Time `json:"currentResetAt"`
			JokeID                 string    `json:"jokeId,omitempty"`
		}{bounded(event.Account.Label, 128), bounded(event.PrimaryTriggerLabel, 128), boundedStrings(event.SecondaryTriggerLabels, 8, 128), bounded(event.ResetKind, 64), event.PreviousResetAt, event.CurrentResetAt, bounded(event.JokeID, 128)}
	case resetwatch.WarningEvent:
		occurredAt = event.DetectedAt
		data = struct {
			AccountLabel       string    `json:"accountLabel,omitempty"`
			Label              string    `json:"label"`
			ThresholdRemaining int       `json:"thresholdRemaining"`
			UsedPercent        float64   `json:"usedPercent"`
			RemainingPercent   float64   `json:"remainingPercent"`
			ResetAt            time.Time `json:"resetAt"`
		}{bounded(event.Account.Label, 128), bounded(event.Label, 128), event.ThresholdRemaining, event.UsedPercent, event.RemainingPercent, event.ResetAt}
	case budget.PacingAlert:
		occurredAt = event.DetectedAt
		data = struct {
			AccountLabel          string    `json:"accountLabel,omitempty"`
			WindowKey             string    `json:"windowKey"`
			Label                 string    `json:"label"`
			Risk                  string    `json:"risk"`
			Confidence            string    `json:"confidence"`
			UsedPercent           float64   `json:"usedPercent"`
			RemainingPercent      float64   `json:"remainingPercent"`
			PacePerHour           float64   `json:"pacePercentPointsPerHour"`
			SafePerHour           float64   `json:"safePercentPointsPerHour"`
			ProjectedExhaustionAt time.Time `json:"projectedExhaustionAt"`
			ResetAt               time.Time `json:"resetAt"`
		}{bounded(event.AccountLabel, 128), bounded(event.WindowKey, 128), bounded(event.Label, 128), event.Risk, event.Confidence, event.UsedPercent, event.RemainingPercentPoints, event.PacePercentPointsPerHour, event.SafePercentPointsPerHour, event.ProjectedExhaustionAt, event.ResetAt}
	case resetwatch.GrantExpiryWarning:
		occurredAt = event.DetectedAt
		data = struct {
			AccountLabel  string    `json:"accountLabel,omitempty"`
			CreditTitle   string    `json:"creditTitle,omitempty"`
			ThresholdDays int       `json:"thresholdDays"`
			ExpiresAt     time.Time `json:"expiresAt"`
		}{bounded(event.Account.Label, 128), bounded(event.CreditTitle, 128), event.ThresholdDays, event.ExpiresAt}
	case resetwatch.ResetGrantEvent:
		occurredAt = event.DetectedAt
		data = struct {
			AccountLabel   string    `json:"accountLabel,omitempty"`
			CreditTitle    string    `json:"creditTitle,omitempty"`
			ResetType      string    `json:"resetType,omitempty"`
			GrantedAt      time.Time `json:"grantedAt"`
			ExpiresAt      time.Time `json:"expiresAt"`
			AvailableCount int       `json:"availableCount"`
		}{bounded(event.Account.Label, 128), bounded(event.CreditTitle, 128), bounded(event.ResetType, 64), event.GrantedAt, event.ExpiresAt, event.AvailableCount}
	case radar.ProbabilityAlert:
		occurredAt = event.DetectedAt
		data = struct {
			Milestone        int     `json:"milestone"`
			Probability24H   float64 `json:"probability24h"`
			Probability48H   float64 `json:"probability48h"`
			Level            string  `json:"level,omitempty"`
			ExpectedWindow   string  `json:"expectedWindow,omitempty"`
			ReasoningSummary string  `json:"reasoningSummary,omitempty"`
			CheckedAt        string  `json:"checkedAt,omitempty"`
		}{event.Milestone, event.Probability24H, event.Probability48H, bounded(event.Level, 64), bounded(event.ExpectedWindow, 256), bounded(event.ReasoningSummary, 2048), bounded(event.CheckedAt, 128)}
	default:
		return Envelope{}, errors.New("unsupported notification payload")
	}
	if occurredAt.IsZero() {
		return Envelope{}, errors.New("notification occurrence time is required")
	}
	body, err := json.Marshal(data)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{SchemaVersion: SchemaVersion, EventID: message.EventID, EventKind: message.EventKind, Source: message.Source, ProfileID: message.ProfileRef, OccurredAt: occurredAt.UTC(), Data: body}, nil
}

func Marshal(envelope Envelope) ([]byte, error) {
	if envelope.SchemaVersion != SchemaVersion || envelope.EventID == "" || envelope.EventKind == "" || envelope.Source == "" || envelope.OccurredAt.IsZero() || !json.Valid(envelope.Data) {
		return nil, errors.New("invalid notification envelope")
	}
	return json.Marshal(envelope)
}

func bounded(value string, limit int) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func boundedStrings(values []string, count, size int) []string {
	if len(values) > count {
		values = values[:count]
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = bounded(value, size)
	}
	return out
}
