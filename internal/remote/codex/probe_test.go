package codex

import (
	"encoding/json"
	"testing"

	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
)

func TestLinesFromUsageResponseUsesAdditionalSparkLimits(t *testing.T) {
	payload := []byte(`{
		"plan_type": "pro",
		"rate_limit": {
			"primary_window": {"used_percent": 5, "reset_at": 1781651408, "limit_window_seconds": 18000},
			"secondary_window": {"used_percent": 14, "reset_at": 1782063907, "limit_window_seconds": 604800}
		},
		"additional_rate_limits": [
			{
				"limit_name": "GPT-5.3-Codex-Spark",
				"metered_feature": "codex_bengalfox",
				"rate_limit": {
					"primary_window": {"used_percent": 0, "reset_at": 1781666971, "limit_window_seconds": 18000},
					"secondary_window": {"used_percent": 0, "reset_at": 1782253771, "limit_window_seconds": 604800}
				}
			}
		],
		"rate_limit_reset_credits": {"available_count": 1}
	}`)
	var parsed usageResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("unmarshal usage response: %v", err)
	}

	lines := linesFromUsageResponse(parsed, resetCreditsResponse{}, false)

	assertProgress(t, lines, "5h limit", 5, "2026-06-16T23:10:08Z")
	assertProgress(t, lines, "Weekly limit", 14, "2026-06-21T17:45:07Z")
	assertProgress(t, lines, "Spark 5h", 0, "2026-06-17T03:29:31Z")
	assertProgress(t, lines, "Spark weekly", 0, "2026-06-23T22:29:31Z")
	assertAmount(t, lines, "Reset grants", 1, "available")
}

func TestLinesFromUsageResponseSupportsTemporaryNoFiveHourShape(t *testing.T) {
	payload := []byte(`{
		"rate_limit": {
			"primary_window": {"used_percent": 0, "reset_at": 1784492145, "limit_window_seconds": 604800},
			"secondary_window": null
		},
		"additional_rate_limits": [{
			"limit_name": "GPT-5.3-Codex-Spark",
			"metered_feature": "codex_bengalfox",
			"rate_limit": {
				"primary_window": {"used_percent": 0, "reset_at": 1784492395, "limit_window_seconds": 604800},
				"secondary_window": null
			}
		}]
	}`)
	var parsed usageResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("unmarshal usage response: %v", err)
	}

	t.Run("enabled by default", func(t *testing.T) {
		t.Setenv(temporaryNoFiveHourFeature, "")
		lines := linesFromUsageResponse(parsed, resetCreditsResponse{}, false)
		assertProgress(t, lines, "Weekly limit", 0, "2026-07-19T20:15:45Z")
		assertProgress(t, lines, "Spark weekly", 0, "2026-07-19T20:19:55Z")
		assertNoProgress(t, lines, "5h limit")
		assertNoProgress(t, lines, "Spark 5h")
	})

	t.Run("kill switch restores positional behavior", func(t *testing.T) {
		t.Setenv(temporaryNoFiveHourFeature, "false")
		lines := linesFromUsageResponse(parsed, resetCreditsResponse{}, false)
		assertProgress(t, lines, "5h limit", 0, "2026-07-19T20:15:45Z")
		assertProgress(t, lines, "Spark 5h", 0, "2026-07-19T20:19:55Z")
	})
}

func TestLinesFromUsageResponseShowsResetCreditExpiry(t *testing.T) {
	parsed := usageResponse{}
	parsed.RateLimitResetCredits = &struct {
		AvailableCount int `json:"available_count"`
	}{AvailableCount: 1}
	resetCredits := resetCreditsResponse{
		AvailableCount: 1,
		Credits: []resetCredit{
			{Status: "redeemed", ExpiresAt: "2026-07-01T00:00:00Z"},
			{ID: "credit_1", Status: "available", ResetType: "rate_limit_reset", Title: "Rate limit reset", GrantedAt: "2026-06-12T01:20:48.728491Z", ExpiresAt: "2026-07-12T01:20:48.728491Z"},
			{ID: "credit_2", Status: "available", ResetType: "rate_limit_reset", Title: "Rate limit reset", GrantedAt: "2026-06-13T01:20:48.728491Z", ExpiresAt: "2026-07-13T01:20:48.728491Z"},
		},
	}

	lines := linesFromUsageResponse(parsed, resetCredits, true)
	remoteCredits := remoteResetCredits(resetCredits, true)

	assertAmount(t, lines, "Reset grants", 1, "available")
	assertText(t, lines, "Grant expiry", "2026-07-12T01:20:48.728491Z")
	if len(remoteCredits) != 3 || remoteCredits[1].ID != "credit_1" || remoteCredits[1].ResetType != "rate_limit_reset" {
		t.Fatalf("unexpected remote credits: %#v", remoteCredits)
	}
}

func TestProfileResultMapsProfileStats(t *testing.T) {
	var parsed profileResponse
	if err := json.Unmarshal([]byte(`{
		"profile": {"username":"ardasevinc","display_name":"Arda Sevinc","profile_picture_url":"https://example.com/a.png"},
		"metadata": {"stats_as_of":"2026-06-28","generated_at":"2026-06-29T00:01:45Z","stats_error":null},
		"stats": {
			"lifetime_tokens": 8318370263,
			"peak_daily_tokens": 947935822,
			"current_streak_days": 22,
			"longest_streak_days": 22,
			"longest_running_turn_sec": 18784,
			"fast_mode_usage_percentage": 2.46,
			"most_used_reasoning_effort": "medium",
			"most_used_reasoning_effort_percentage": 80.59,
			"total_threads": 585,
			"total_skills_used": 1001,
			"unique_skills_used": 38,
			"top_invocations": [{"type":"skill","skill_name":"agent-browser","usage_count":277}],
			"daily_usage_buckets": [{"start_date":"2026-06-28","tokens":78511833}],
			"weekly_usage_buckets": [{"start_date":"2026-06-22","tokens":1573087214}],
			"cumulative_daily_usage_buckets": [{"start_date":"2026-06-28","tokens":8318370263}]
		}
	}`), &parsed); err != nil {
		t.Fatalf("unmarshal profile response: %v", err)
	}

	result := profileResult(parsed, testAuth(), "2026-06-29T00:02:00Z")

	if result.Profile.Username != "ardasevinc" || result.Profile.DisplayName != "Arda Sevinc" {
		t.Fatalf("unexpected profile: %#v", result.Profile)
	}
	if result.Metadata.StatsAsOf != "2026-06-28" || result.Metadata.GeneratedAt != "2026-06-29T00:01:45Z" {
		t.Fatalf("unexpected metadata: %#v", result.Metadata)
	}
	if result.Stats.LifetimeTokens != 8318370263 || result.Stats.CurrentStreakDays != 22 || result.Stats.MostUsedReasoningEffort != "medium" {
		t.Fatalf("unexpected stats: %#v", result.Stats)
	}
	if len(result.Stats.TopInvocations) != 1 || result.Stats.TopInvocations[0].SkillName != "agent-browser" {
		t.Fatalf("unexpected invocations: %#v", result.Stats.TopInvocations)
	}
	if len(result.Stats.DailyUsageBuckets) != 1 || result.Stats.DailyUsageBuckets[0].StartDate != "2026-06-28" {
		t.Fatalf("unexpected daily buckets: %#v", result.Stats.DailyUsageBuckets)
	}
	if !result.AuthState.OK || result.AuthState.AccessToken != "token" {
		t.Fatalf("unexpected auth state: %#v", result.AuthState)
	}
}

func testAuth() remote.AuthState {
	return remote.AuthState{OK: true, Email: "arda@example.com", AccessToken: "token", AccountID: "acct"}
}

func assertProgress(t *testing.T, lines []model.MetricLine, label string, used float64, resetsAt string) {
	t.Helper()
	for _, line := range lines {
		if line.Label != label {
			continue
		}
		if line.Type != "progress" {
			t.Fatalf("%s type = %q, want progress", label, line.Type)
		}
		if line.Used == nil || *line.Used != used {
			t.Fatalf("%s used = %#v, want %v", label, line.Used, used)
		}
		if line.ResetsAt != resetsAt {
			t.Fatalf("%s resetsAt = %q, want %q", label, line.ResetsAt, resetsAt)
		}
		return
	}
	t.Fatalf("missing progress line %q in %#v", label, lines)
}

func assertNoProgress(t *testing.T, lines []model.MetricLine, label string) {
	t.Helper()
	for _, line := range lines {
		if line.Type == "progress" && line.Label == label {
			t.Fatalf("unexpected progress line %q: %#v", label, line)
		}
	}
}

func assertAmount(t *testing.T, lines []model.MetricLine, label string, value float64, suffix string) {
	t.Helper()
	for _, line := range lines {
		if line.Label != label {
			continue
		}
		if line.Type != "amount" {
			t.Fatalf("%s type = %q, want amount", label, line.Type)
		}
		got, ok := line.Value.(float64)
		if !ok || got != value {
			t.Fatalf("%s value = %#v, want %v", label, line.Value, value)
		}
		if line.Format == nil || line.Format.Suffix != suffix {
			t.Fatalf("%s suffix = %#v, want %q", label, line.Format, suffix)
		}
		return
	}
	t.Fatalf("missing amount line %q in %#v", label, lines)
}

func assertText(t *testing.T, lines []model.MetricLine, label string, value any) {
	t.Helper()
	for _, line := range lines {
		if line.Label != label {
			continue
		}
		if line.Type != "text" {
			t.Fatalf("%s type = %q, want text", label, line.Type)
		}
		if line.Value != value {
			t.Fatalf("%s value = %#v, want %#v", label, line.Value, value)
		}
		return
	}
	t.Fatalf("missing text line %q in %#v", label, lines)
}
