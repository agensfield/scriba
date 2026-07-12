package agentcontext

import "time"

const SchemaVersion = "scriba.context.v1"

type Context struct {
	SchemaVersion string     `json:"schemaVersion"`
	GeneratedAt   time.Time  `json:"generatedAt"`
	Sources       []Source   `json:"sources"`
	Providers     []Provider `json:"providers"`
	Events        []Event    `json:"events"`
}

type Provenance struct {
	Source string `json:"source"`
}
type Source struct {
	SourceID     string       `json:"sourceId"`
	Kind         string       `json:"kind"`
	Availability string       `json:"availability"`
	ObservedAt   *time.Time   `json:"observedAt,omitempty"`
	GeneratedAt  *time.Time   `json:"generatedAt,omitempty"`
	AgeMS        *int64       `json:"ageMs,omitempty"`
	Stale        *bool        `json:"stale,omitempty"`
	Provenance   []Provenance `json:"provenance"`
	ReasonCode   string       `json:"reasonCode,omitempty"`
}
type Provider struct {
	ProviderID string    `json:"providerId"`
	Profiles   []Profile `json:"profiles"`
}
type Profile struct {
	ProfileID string   `json:"profileId"`
	Windows   []Window `json:"windows"`
	Budgets   []Budget `json:"budgets"`
	Grants    Grants   `json:"grants"`
	SourceIDs []string `json:"sourceIds"`
}
type Window struct {
	Key                    string     `json:"key"`
	UsedPercent            *float64   `json:"usedPercent"`
	RemainingPercentPoints *float64   `json:"remainingPercentPoints"`
	ResetAt                *time.Time `json:"resetAt"`
}
type Budget struct {
	Key        string   `json:"key"`
	Risk       string   `json:"risk"`
	Confidence string   `json:"confidence"`
	Reasons    []string `json:"reasons"`
}
type Grants struct {
	AvailableCount   int        `json:"availableCount"`
	EarliestExpiryAt *time.Time `json:"earliestExpiryAt,omitempty"`
}
type Event struct {
	SchemaVersion string    `json:"schemaVersion"`
	ID            string    `json:"id"`
	ProviderID    string    `json:"providerId"`
	ProfileID     string    `json:"profileId"`
	Kind          string    `json:"kind"`
	DetectedAt    time.Time `json:"detectedAt"`
	Data          EventData `json:"data"`
}

const EventsSchemaVersion = "scriba.events.v1"

type EventPageRequest struct {
	Mode      string
	Cursor    string
	Limit     int
	ProfileID string
}

type EventPage struct {
	SchemaVersion string          `json:"schemaVersion"`
	GeneratedAt   time.Time       `json:"generatedAt"`
	Events        []Event         `json:"events"`
	Cursor        EventPageCursor `json:"cursor"`
}

type EventPageCursor struct {
	Next      string `json:"next"`
	HighWater string `json:"highWater"`
}

type EventPageError struct {
	ReasonCode string `json:"reasonCode"`
}

type ProfileError struct{ ReasonCode string }

func (e *ProfileError) Error() string { return "profile unavailable: " + e.ReasonCode }

func (e *EventPageError) Error() string { return "event page unavailable: " + e.ReasonCode }

type EventData interface{ isAgentEventData() }
type RemainingCheckpoint struct {
	WindowKey              string     `json:"windowKey"`
	CheckpointPercent      int        `json:"checkpointPercent"`
	UsedPercent            float64    `json:"usedPercent"`
	RemainingPercentPoints float64    `json:"remainingPercentPoints"`
	ResetAt                *time.Time `json:"resetAt,omitempty"`
}
type ResetTransition struct {
	WindowKey       string    `json:"windowKey"`
	ResetKind       string    `json:"resetKind"`
	PreviousResetAt time.Time `json:"previousResetAt"`
	ResetAt         time.Time `json:"resetAt"`
}
type GrantAvailable struct {
	AvailableCount   int        `json:"availableCount"`
	EarliestExpiryAt *time.Time `json:"earliestExpiryAt,omitempty"`
}
type GrantExpiryCheckpoint struct {
	CheckpointDays int       `json:"checkpointDays"`
	ExpiresAt      time.Time `json:"expiresAt"`
}

func (RemainingCheckpoint) isAgentEventData()   {}
func (ResetTransition) isAgentEventData()       {}
func (GrantAvailable) isAgentEventData()        {}
func (GrantExpiryCheckpoint) isAgentEventData() {}
