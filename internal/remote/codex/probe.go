package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/codexauth"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
)

var (
	usageURL              = "https://chatgpt.com/backend-api/wham/usage"
	rateLimitResetCredits = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits" // #nosec G101 -- Endpoint URL, not a credential.
	profileURL            = "https://chatgpt.com/backend-api/wham/profiles/me"
)

type usageResponse struct {
	PlanType             string       `json:"plan_type"`
	RateLimit            *rateLimit   `json:"rate_limit"`
	CodeReviewRateLimit  *rateLimit   `json:"code_review_rate_limit"`
	AdditionalRateLimits []namedLimit `json:"additional_rate_limits"`
	Credits              *struct {
		HasCredits bool `json:"has_credits"`
		Unlimited  bool `json:"unlimited"`
		Balance    any  `json:"balance"`
	} `json:"credits"`
	RateLimitResetCredits *struct {
		AvailableCount int `json:"available_count"`
	} `json:"rate_limit_reset_credits"`
}

type namedLimit struct {
	LimitName      string     `json:"limit_name"`
	MeteredFeature string     `json:"metered_feature"`
	RateLimit      *rateLimit `json:"rate_limit"`
}

type rateLimit struct {
	PrimaryWindow   *window `json:"primary_window"`
	SecondaryWindow *window `json:"secondary_window"`
}

type window struct {
	UsedPercent        float64 `json:"used_percent"`
	ResetAt            int64   `json:"reset_at"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
}

type resetCreditsResponse struct {
	AvailableCount int           `json:"available_count"`
	Credits        []resetCredit `json:"credits"`
}

type resetCredit struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	ResetType string `json:"reset_type"`
	Title     string `json:"title"`
	GrantedAt string `json:"granted_at"`
	ExpiresAt string `json:"expires_at"`
}

type ProfileResult struct {
	SchemaVersion string                   `json:"schemaVersion,omitempty"`
	ProviderID    string                   `json:"providerId"`
	Source        string                   `json:"source"`
	Profile       Profile                  `json:"profile"`
	Stats         ProfileStats             `json:"stats"`
	Metadata      ProfileMetadata          `json:"metadata"`
	Provenance    []model.SourceProvenance `json:"provenance,omitempty"`
	AuthState     remote.AuthState         `json:"authState"`
}

type Profile struct {
	Username          string `json:"username,omitempty"`
	DisplayName       string `json:"displayName,omitempty"`
	ProfilePictureURL string `json:"profilePictureUrl,omitempty"`
}

type ProfileMetadata struct {
	StatsAsOf   string `json:"statsAsOf,omitempty"`
	GeneratedAt string `json:"generatedAt,omitempty"`
	StatsError  any    `json:"statsError,omitempty"`
}

type ProfileStats struct {
	LifetimeTokens              int64         `json:"lifetimeTokens,omitempty"`
	PeakDailyTokens             int64         `json:"peakDailyTokens,omitempty"`
	CurrentStreakDays           int           `json:"currentStreakDays,omitempty"`
	LongestStreakDays           int           `json:"longestStreakDays,omitempty"`
	LongestRunningTurnSec       int64         `json:"longestRunningTurnSec,omitempty"`
	FastModeUsagePercentage     float64       `json:"fastModeUsagePercentage,omitempty"`
	MostUsedReasoningEffort     string        `json:"mostUsedReasoningEffort,omitempty"`
	MostUsedReasoningEffortPct  float64       `json:"mostUsedReasoningEffortPercentage,omitempty"`
	TotalThreads                int64         `json:"totalThreads,omitempty"`
	TotalSkillsUsed             int64         `json:"totalSkillsUsed,omitempty"`
	UniqueSkillsUsed            int64         `json:"uniqueSkillsUsed,omitempty"`
	WorkspaceRank               *int64        `json:"workspaceRank,omitempty"`
	WorkspaceTotalUserCount     *int64        `json:"workspaceTotalUserCount,omitempty"`
	TopInvocations              []Invocation  `json:"topInvocations,omitempty"`
	DailyUsageBuckets           []UsageBucket `json:"dailyUsageBuckets,omitempty"`
	WeeklyUsageBuckets          []UsageBucket `json:"weeklyUsageBuckets,omitempty"`
	CumulativeDailyUsageBuckets []UsageBucket `json:"cumulativeDailyUsageBuckets,omitempty"`
}

type UsageBucket struct {
	StartDate string `json:"startDate"`
	Tokens    int64  `json:"tokens"`
}

type Invocation struct {
	Type       string `json:"type,omitempty"`
	PluginID   string `json:"pluginId,omitempty"`
	PluginName string `json:"pluginName,omitempty"`
	SkillID    string `json:"skillId,omitempty"`
	SkillName  string `json:"skillName,omitempty"`
	UsageCount int64  `json:"usageCount,omitempty"`
}

type profileResponse struct {
	Profile struct {
		Username          string `json:"username"`
		DisplayName       string `json:"display_name"`
		ProfilePictureURL string `json:"profile_picture_url"`
	} `json:"profile"`
	Stats struct {
		LifetimeTokens              int64                `json:"lifetime_tokens"`
		PeakDailyTokens             int64                `json:"peak_daily_tokens"`
		CurrentStreakDays           int                  `json:"current_streak_days"`
		LongestStreakDays           int                  `json:"longest_streak_days"`
		LongestRunningTurnSec       int64                `json:"longest_running_turn_sec"`
		FastModeUsagePercentage     float64              `json:"fast_mode_usage_percentage"`
		MostUsedReasoningEffort     string               `json:"most_used_reasoning_effort"`
		MostUsedReasoningEffortPct  float64              `json:"most_used_reasoning_effort_percentage"`
		TotalThreads                int64                `json:"total_threads"`
		TotalSkillsUsed             int64                `json:"total_skills_used"`
		UniqueSkillsUsed            int64                `json:"unique_skills_used"`
		WorkspaceRank               *int64               `json:"workspace_rank"`
		WorkspaceTotalUserCount     *int64               `json:"workspace_total_user_count"`
		TopInvocations              []profileInvocation  `json:"top_invocations"`
		DailyUsageBuckets           []profileUsageBucket `json:"daily_usage_buckets"`
		WeeklyUsageBuckets          []profileUsageBucket `json:"weekly_usage_buckets"`
		CumulativeDailyUsageBuckets []profileUsageBucket `json:"cumulative_daily_usage_buckets"`
	} `json:"stats"`
	Metadata struct {
		StatsAsOf   string `json:"stats_as_of"`
		GeneratedAt string `json:"generated_at"`
		StatsError  any    `json:"stats_error"`
	} `json:"metadata"`
}

type profileUsageBucket struct {
	StartDate string `json:"start_date"`
	Tokens    int64  `json:"tokens"`
}

type profileInvocation struct {
	Type       string `json:"type"`
	PluginID   string `json:"plugin_id"`
	PluginName string `json:"plugin_name"`
	SkillID    string `json:"skill_id"`
	SkillName  string `json:"skill_name"`
	UsageCount int64  `json:"usage_count"`
}

func Probe(includeHTTP bool) (remote.ProbeResult, error) {
	return ProbeContext(context.Background(), includeHTTP)
}

func AuthPaths() []string {
	return codexauth.AuthPaths()
}

func ProbeContext(ctx context.Context, includeHTTP bool) (remote.ProbeResult, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	auth, err := loadAuth(ctx, nil, false)
	if err != nil {
		return remote.ProbeResult{}, err
	}
	if !auth.OK {
		return remote.ProbeResult{
			ProviderID: "codex",
			Lines:      []model.MetricLine{{Type: "badge", Label: "Codex API", Text: "Auth unavailable"}},
			Provenance: []model.SourceProvenance{{Kind: "provider-api", ProviderID: "codex", FetchedAt: now, Error: auth.Error}},
			AuthState:  auth,
		}, nil
	}
	if !includeHTTP {
		return remote.ProbeResult{ProviderID: "codex", AuthState: auth}, nil
	}
	result, err := FetchLimits(ctx, http.DefaultClient)
	if err != nil {
		return remote.ProbeResult{}, err
	}
	return result, nil
}

func FetchProfile(ctx context.Context, client *http.Client) (ProfileResult, error) {
	if client == nil {
		client = http.DefaultClient
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	auth, err := loadAuth(ctx, client, false)
	if err != nil {
		return ProfileResult{}, err
	}
	if !auth.OK {
		return ProfileResult{
			ProviderID: "codex",
			Source:     "chatgpt-codex-profile-backend",
			Provenance: []model.SourceProvenance{{Kind: "provider-api", ProviderID: "codex", FetchedAt: now, Error: auth.Error}},
			AuthState:  auth,
		}, nil
	}
	parsed, err := fetchProfile(ctx, client, auth)
	if isAuthHTTPError(err) {
		auth, err = loadAuth(ctx, client, true)
		if err != nil {
			return ProfileResult{}, err
		}
		if !auth.OK {
			return ProfileResult{ProviderID: "codex", Source: "chatgpt-codex-profile-backend", AuthState: auth}, nil
		}
		parsed, err = fetchProfile(ctx, client, auth)
	}
	if err != nil {
		return ProfileResult{}, err
	}
	return profileResult(parsed, auth, now), nil
}

func FetchLimits(ctx context.Context, client *http.Client) (remote.ProbeResult, error) {
	if client == nil {
		client = http.DefaultClient
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	auth, err := loadAuth(ctx, client, false)
	if err != nil {
		return remote.ProbeResult{}, err
	}
	if !auth.OK {
		return remote.ProbeResult{
			ProviderID: "codex",
			Lines:      []model.MetricLine{{Type: "badge", Label: "Codex API", Text: "Auth unavailable"}},
			Provenance: []model.SourceProvenance{{Kind: "provider-api", ProviderID: "codex", FetchedAt: now, Error: auth.Error}},
			AuthState:  auth,
		}, nil
	}
	parsed, err := fetchUsage(ctx, client, auth)
	if isAuthHTTPError(err) {
		auth, err = loadAuth(ctx, client, true)
		if err != nil {
			return remote.ProbeResult{}, err
		}
		if !auth.OK {
			return remote.ProbeResult{ProviderID: "codex", AuthState: auth}, nil
		}
		parsed, err = fetchUsage(ctx, client, auth)
	}
	if err != nil {
		return remote.ProbeResult{}, err
	}
	resetCredits, resetCreditsOK := fetchResetCreditsIfAvailable(ctx, client, auth, parsed)
	return remote.ProbeResult{
		ProviderID:   "codex",
		Lines:        linesFromUsageResponse(parsed, resetCredits, resetCreditsOK),
		ResetCredits: remoteResetCredits(resetCredits, resetCreditsOK),
		Provenance:   []model.SourceProvenance{{Kind: "provider-api", ProviderID: "codex", FetchedAt: now}},
		AuthState:    auth,
	}, nil
}

func profileResult(parsed profileResponse, auth remote.AuthState, fetchedAt string) ProfileResult {
	return ProfileResult{
		ProviderID: "codex",
		Source:     "chatgpt-codex-profile-backend",
		Profile: Profile{
			Username:          parsed.Profile.Username,
			DisplayName:       parsed.Profile.DisplayName,
			ProfilePictureURL: parsed.Profile.ProfilePictureURL,
		},
		Stats: ProfileStats{
			LifetimeTokens:              parsed.Stats.LifetimeTokens,
			PeakDailyTokens:             parsed.Stats.PeakDailyTokens,
			CurrentStreakDays:           parsed.Stats.CurrentStreakDays,
			LongestStreakDays:           parsed.Stats.LongestStreakDays,
			LongestRunningTurnSec:       parsed.Stats.LongestRunningTurnSec,
			FastModeUsagePercentage:     parsed.Stats.FastModeUsagePercentage,
			MostUsedReasoningEffort:     parsed.Stats.MostUsedReasoningEffort,
			MostUsedReasoningEffortPct:  parsed.Stats.MostUsedReasoningEffortPct,
			TotalThreads:                parsed.Stats.TotalThreads,
			TotalSkillsUsed:             parsed.Stats.TotalSkillsUsed,
			UniqueSkillsUsed:            parsed.Stats.UniqueSkillsUsed,
			WorkspaceRank:               parsed.Stats.WorkspaceRank,
			WorkspaceTotalUserCount:     parsed.Stats.WorkspaceTotalUserCount,
			TopInvocations:              profileInvocations(parsed.Stats.TopInvocations),
			DailyUsageBuckets:           profileUsageBuckets(parsed.Stats.DailyUsageBuckets),
			WeeklyUsageBuckets:          profileUsageBuckets(parsed.Stats.WeeklyUsageBuckets),
			CumulativeDailyUsageBuckets: profileUsageBuckets(parsed.Stats.CumulativeDailyUsageBuckets),
		},
		Metadata: ProfileMetadata{
			StatsAsOf:   parsed.Metadata.StatsAsOf,
			GeneratedAt: parsed.Metadata.GeneratedAt,
			StatsError:  parsed.Metadata.StatsError,
		},
		Provenance: []model.SourceProvenance{{Kind: "provider-api", ProviderID: "codex", FetchedAt: fetchedAt}},
		AuthState:  auth,
	}
}

func profileUsageBuckets(input []profileUsageBucket) []UsageBucket {
	buckets := make([]UsageBucket, 0, len(input))
	for _, bucket := range input {
		buckets = append(buckets, UsageBucket(bucket))
	}
	return buckets
}

func profileInvocations(input []profileInvocation) []Invocation {
	invocations := make([]Invocation, 0, len(input))
	for _, invocation := range input {
		invocations = append(invocations, Invocation(invocation))
	}
	return invocations
}

func linesFromUsageResponse(parsed usageResponse, resetCredits resetCreditsResponse, resetCreditsOK bool) []model.MetricLine {
	var lines []model.MetricLine
	if parsed.PlanType != "" {
		lines = append(lines, model.MetricLine{Type: "badge", Label: "Plan", Text: parsed.PlanType})
	}
	lines = appendRateLimitLines(lines, "", parsed.RateLimit)
	for _, limit := range parsed.AdditionalRateLimits {
		lines = appendRateLimitLines(lines, additionalLimitLabel(limit), limit.RateLimit)
	}
	lines = appendRateLimitLines(lines, "Review", parsed.CodeReviewRateLimit)
	if parsed.Credits != nil && parsed.Credits.HasCredits {
		if parsed.Credits.Unlimited {
			lines = append(lines, model.MetricLine{Type: "badge", Label: "Credits", Text: "unlimited"})
		} else if balance, ok := number(parsed.Credits.Balance); ok {
			lines = append(lines, model.MetricLine{Type: "amount", Label: "Credits left", Value: balance, Format: &model.MetricFormat{Kind: "count", Suffix: "credits"}})
		}
	}
	if resetCreditsOK {
		lines = append(lines, resetGrantLine(resetCredits.AvailableCount))
		if expiry := earliestAvailableResetExpiry(resetCredits.Credits); expiry != "" {
			lines = append(lines, model.MetricLine{Type: "text", Label: "Grant expiry", Value: expiry})
		}
	} else if parsed.RateLimitResetCredits != nil {
		lines = append(lines, resetGrantLine(parsed.RateLimitResetCredits.AvailableCount))
	}
	return lines
}

func fetchResetCreditsIfAvailable(ctx context.Context, client *http.Client, auth remote.AuthState, parsed usageResponse) (resetCreditsResponse, bool) {
	if parsed.RateLimitResetCredits == nil || parsed.RateLimitResetCredits.AvailableCount <= 0 {
		return resetCreditsResponse{}, false
	}
	credits, err := fetchResetCredits(ctx, client, auth)
	if err != nil {
		return resetCreditsResponse{}, false
	}
	return credits, true
}

func fetchResetCredits(ctx context.Context, client *http.Client, auth remote.AuthState) (resetCreditsResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rateLimitResetCredits, nil)
	if err != nil {
		return resetCreditsResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	req.Header.Set("Accept", "application/json")
	if auth.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", auth.AccountID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return resetCreditsResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resetCreditsResponse{}, fmt.Errorf("codex reset credits request failed: %d", resp.StatusCode)
	}
	var parsed resetCreditsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return resetCreditsResponse{}, err
	}
	return parsed, nil
}

func appendRateLimitLines(lines []model.MetricLine, prefix string, limit *rateLimit) []model.MetricLine {
	if limit == nil {
		return lines
	}
	if limit.PrimaryWindow != nil {
		lines = append(lines, progressLine(limitLabel(prefix, "5h limit"), *limit.PrimaryWindow))
	}
	if limit.SecondaryWindow != nil {
		lines = append(lines, progressLine(limitLabel(prefix, "Weekly limit"), *limit.SecondaryWindow))
	}
	return lines
}

func additionalLimitLabel(limit namedLimit) string {
	name := strings.ToLower(limit.LimitName)
	feature := strings.ToLower(limit.MeteredFeature)
	if strings.Contains(name, "spark") || strings.Contains(feature, "bengalfox") {
		return "Spark"
	}
	if limit.LimitName != "" {
		return limit.LimitName
	}
	if limit.MeteredFeature != "" {
		return limit.MeteredFeature
	}
	return "Additional"
}

func limitLabel(prefix, label string) string {
	if prefix == "" {
		return label
	}
	if label == "5h limit" {
		return prefix + " 5h"
	}
	if label == "Weekly limit" {
		return prefix + " weekly"
	}
	return prefix + " " + label
}

func resetGrantLine(count int) model.MetricLine {
	value := float64(count)
	return model.MetricLine{Type: "amount", Label: "Reset grants", Value: value, Format: &model.MetricFormat{Kind: "count", Suffix: "available"}}
}

func earliestAvailableResetExpiry(credits []resetCredit) string {
	var earliest time.Time
	for _, credit := range credits {
		if credit.Status != "" && credit.Status != "available" {
			continue
		}
		if credit.ExpiresAt == "" {
			continue
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, credit.ExpiresAt)
		if err != nil {
			continue
		}
		if earliest.IsZero() || expiresAt.Before(earliest) {
			earliest = expiresAt
		}
	}
	if earliest.IsZero() {
		return ""
	}
	return earliest.UTC().Format(time.RFC3339Nano)
}

func remoteResetCredits(resetCredits resetCreditsResponse, ok bool) []remote.ResetCredit {
	if !ok || len(resetCredits.Credits) == 0 {
		return nil
	}
	credits := make([]remote.ResetCredit, 0, len(resetCredits.Credits))
	for _, credit := range resetCredits.Credits {
		credits = append(credits, remote.ResetCredit{
			ID:        credit.ID,
			Status:    credit.Status,
			ResetType: credit.ResetType,
			Title:     credit.Title,
			GrantedAt: credit.GrantedAt,
			ExpiresAt: credit.ExpiresAt,
		})
	}
	return credits
}

type usageHTTPError struct {
	status int
}

func (e usageHTTPError) Error() string {
	return fmt.Sprintf("codex usage request failed: %d", e.status)
}

type profileHTTPError struct {
	status int
}

func (e profileHTTPError) Error() string {
	return fmt.Sprintf("codex profile request failed: %d", e.status)
}

func fetchProfile(ctx context.Context, client *http.Client, auth remote.AuthState) (profileResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, profileURL, nil)
	if err != nil {
		return profileResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	req.Header.Set("Accept", "application/json")
	if auth.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", auth.AccountID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return profileResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return profileResponse{}, profileHTTPError{status: resp.StatusCode}
	}
	var parsed profileResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return profileResponse{}, err
	}
	return parsed, nil
}

func fetchUsage(ctx context.Context, client *http.Client, auth remote.AuthState) (usageResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	if err != nil {
		return usageResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	req.Header.Set("Accept", "application/json")
	if auth.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", auth.AccountID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return usageResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return usageResponse{}, usageHTTPError{status: resp.StatusCode}
	}
	var parsed usageResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return usageResponse{}, err
	}
	return parsed, nil
}

func isAuthHTTPError(err error) bool {
	var httpErr usageHTTPError
	if err != nil && errors.As(err, &httpErr) && (httpErr.status == http.StatusUnauthorized || httpErr.status == http.StatusForbidden) {
		return true
	}
	var profileErr profileHTTPError
	return err != nil && errors.As(err, &profileErr) && (profileErr.status == http.StatusUnauthorized || profileErr.status == http.StatusForbidden)
}

func loadAuth(ctx context.Context, client *http.Client, forceRefresh bool) (remote.AuthState, error) {
	creds, err := codexauth.Load(ctx, codexauth.LoadOptions{Client: client, ForceRefresh: forceRefresh})
	if err != nil {
		return remote.AuthState{}, err
	}
	return remote.AuthState{
		OK:          creds.OK,
		Error:       creds.Error,
		Source:      creds.Source,
		Email:       creds.Email,
		AccessToken: creds.AccessToken,
		AccountID:   creds.AccountID,
	}, nil
}

func progressLine(label string, w window) model.MetricLine {
	used := w.UsedPercent
	limit := 100.0
	var resetsAt string
	if w.ResetAt > 0 {
		resetsAt = time.Unix(w.ResetAt, 0).UTC().Format(time.RFC3339Nano)
	}
	var period *int64
	if w.LimitWindowSeconds > 0 {
		ms := w.LimitWindowSeconds * 1000
		period = &ms
	}
	return model.MetricLine{Type: "progress", Label: label, Used: &used, Limit: &limit, Format: &model.MetricFormat{Kind: "percent"}, ResetsAt: resetsAt, PeriodDurationMs: period}
}

func number(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case string:
		parsed, err := strconv.ParseFloat(v, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
