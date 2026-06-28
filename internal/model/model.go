package model

import "encoding/json"

const SchemaVersion = "scriba.v1"

type SourceProvenance struct {
	Kind       string `json:"kind"`
	ProviderID string `json:"providerId"`
	FetchedAt  string `json:"fetchedAt,omitempty"`
	CacheAgeMs *int64 `json:"cacheAgeMs,omitempty"`
	Stale      bool   `json:"stale,omitempty"`
	Error      string `json:"error,omitempty"`
}

type MetricFormat struct {
	Kind   string `json:"kind"`
	Suffix string `json:"suffix,omitempty"`
}

type MetricLine struct {
	Type             string             `json:"type"`
	Label            string             `json:"label"`
	Value            any                `json:"value,omitempty"`
	Format           *MetricFormat      `json:"format,omitempty"`
	Used             *float64           `json:"used,omitempty"`
	Limit            *float64           `json:"limit,omitempty"`
	ResetsAt         string             `json:"resetsAt,omitempty"`
	PeriodDurationMs *int64             `json:"periodDurationMs,omitempty"`
	Text             string             `json:"text,omitempty"`
	Provenance       []SourceProvenance `json:"provenance,omitempty"`
}

type ProviderSnapshot struct {
	ProviderID  string             `json:"providerId"`
	DisplayName string             `json:"displayName"`
	State       string             `json:"state"`
	Plan        string             `json:"plan,omitempty"`
	Lines       []MetricLine       `json:"lines"`
	Provenance  []SourceProvenance `json:"provenance"`
}

type StatusSnapshot struct {
	SchemaVersion string             `json:"schemaVersion"`
	GeneratedAt   string             `json:"generatedAt"`
	Providers     []ProviderSnapshot `json:"providers"`
}

type TokenUsage struct {
	InputTokens           int64 `json:"inputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
	CacheCreationTokens   int64 `json:"cacheCreationTokens"`
	CacheReadTokens       int64 `json:"cacheReadTokens"`
	CachedInputTokens     int64 `json:"cachedInputTokens"`
	ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
	TotalTokens           int64 `json:"totalTokens"`
}

type ModelBreakdown struct {
	TokenUsage
	Model        string   `json:"model"`
	CostUSD      *float64 `json:"costUSD"`
	PricingState string   `json:"pricingState"`
}

type ReportTotals struct {
	TokenUsage
	CostUSD *float64 `json:"costUSD"`
}

type DailyReportRow struct {
	Date       string `json:"date"`
	ProviderID string `json:"providerId"`
	ReportTotals
	Models  []ModelBreakdown `json:"models"`
	Project string           `json:"project,omitempty"`
}

type WeeklyReportRow struct {
	Week       string `json:"week"`
	ProviderID string `json:"providerId"`
	ReportTotals
	Models  []ModelBreakdown `json:"models"`
	Project string           `json:"project,omitempty"`
}

type MonthlyReportRow struct {
	Month      string `json:"month"`
	ProviderID string `json:"providerId"`
	ReportTotals
	Models  []ModelBreakdown `json:"models"`
	Project string           `json:"project,omitempty"`
}

type SessionReportRow struct {
	SessionID    string `json:"sessionId"`
	ProviderID   string `json:"providerId"`
	LastActivity string `json:"lastActivity"`
	ProjectPath  string `json:"projectPath,omitempty"`
	Directory    string `json:"directory,omitempty"`
	SessionFile  string `json:"sessionFile,omitempty"`
	ReportTotals
	Models []ModelBreakdown `json:"models"`
}

type BlockReportRow struct {
	ID                  string `json:"id"`
	ProviderID          string `json:"providerId"`
	StartTime           string `json:"startTime"`
	EndTime             string `json:"endTime"`
	ActualEndTime       string `json:"actualEndTime,omitempty"`
	IsActive            bool   `json:"isActive"`
	IsGap               bool   `json:"isGap"`
	Entries             int    `json:"entries"`
	UsageLimitResetTime string `json:"usageLimitResetTime,omitempty"`
	ReportTotals
	Models []ModelBreakdown `json:"models"`
}

type ScannerStats struct {
	Files              int      `json:"files"`
	Bytes              int64    `json:"bytes"`
	Lines              int      `json:"lines"`
	Events             int      `json:"events"`
	InvalidLines       int      `json:"invalidLines"`
	Duplicates         int      `json:"duplicates"`
	MissingDirectories []string `json:"missingDirectories"`
}

func (s ScannerStats) MarshalJSON() ([]byte, error) {
	type alias ScannerStats
	a := alias(s)
	if a.MissingDirectories == nil {
		a.MissingDirectories = []string{}
	}
	return json.Marshal(a)
}

type LocalUsageEvent struct {
	ProviderID            string   `json:"providerId"`
	SessionID             string   `json:"sessionId"`
	Timestamp             string   `json:"timestamp"`
	Model                 string   `json:"model"`
	Project               string   `json:"project,omitempty"`
	ProjectPath           string   `json:"projectPath,omitempty"`
	Directory             string   `json:"directory,omitempty"`
	SessionFile           string   `json:"sessionFile,omitempty"`
	InputTokens           int64    `json:"inputTokens"`
	OutputTokens          int64    `json:"outputTokens"`
	CacheCreationTokens   int64    `json:"cacheCreationTokens"`
	CacheReadTokens       int64    `json:"cacheReadTokens"`
	CachedInputTokens     int64    `json:"cachedInputTokens"`
	ReasoningOutputTokens int64    `json:"reasoningOutputTokens"`
	TotalTokens           int64    `json:"totalTokens"`
	CostUSD               *float64 `json:"costUSD"`
	UniqueKey             string   `json:"uniqueKey,omitempty"`
	SourcePath            string   `json:"sourcePath"`
	IsFallbackModel       bool     `json:"isFallbackModel,omitempty"`
}
