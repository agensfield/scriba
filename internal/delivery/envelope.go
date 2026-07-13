package delivery

import (
	"encoding/json"
	"errors"
	"time"

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
		}{event.Account.Label, event.PrimaryTriggerLabel, event.SecondaryTriggerLabels, event.ResetKind, event.PreviousResetAt, event.CurrentResetAt, event.JokeID}
	case resetwatch.WarningEvent:
		occurredAt = event.DetectedAt
		data = struct {
			AccountLabel       string    `json:"accountLabel,omitempty"`
			Label              string    `json:"label"`
			ThresholdRemaining int       `json:"thresholdRemaining"`
			UsedPercent        float64   `json:"usedPercent"`
			RemainingPercent   float64   `json:"remainingPercent"`
			ResetAt            time.Time `json:"resetAt"`
		}{event.Account.Label, event.Label, event.ThresholdRemaining, event.UsedPercent, event.RemainingPercent, event.ResetAt}
	case resetwatch.GrantExpiryWarning:
		occurredAt = event.DetectedAt
		data = struct {
			AccountLabel  string    `json:"accountLabel,omitempty"`
			CreditTitle   string    `json:"creditTitle,omitempty"`
			ThresholdDays int       `json:"thresholdDays"`
			ExpiresAt     time.Time `json:"expiresAt"`
		}{event.Account.Label, event.CreditTitle, event.ThresholdDays, event.ExpiresAt}
	case resetwatch.ResetGrantEvent:
		occurredAt = event.DetectedAt
		data = struct {
			AccountLabel   string    `json:"accountLabel,omitempty"`
			CreditTitle    string    `json:"creditTitle,omitempty"`
			ResetType      string    `json:"resetType,omitempty"`
			GrantedAt      time.Time `json:"grantedAt"`
			ExpiresAt      time.Time `json:"expiresAt"`
			AvailableCount int       `json:"availableCount"`
		}{event.Account.Label, event.CreditTitle, event.ResetType, event.GrantedAt, event.ExpiresAt, event.AvailableCount}
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
		}{event.Milestone, event.Probability24H, event.Probability48H, event.Level, event.ExpectedWindow, event.ReasoningSummary, event.CheckedAt}
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
