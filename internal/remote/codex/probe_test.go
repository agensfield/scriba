package codex

import (
	"encoding/json"
	"testing"

	"github.com/agensfield/scriba/internal/model"
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

func TestLinesFromUsageResponseShowsResetCreditExpiry(t *testing.T) {
	parsed := usageResponse{}
	parsed.RateLimitResetCredits = &struct {
		AvailableCount int `json:"available_count"`
	}{AvailableCount: 1}
	resetCredits := resetCreditsResponse{
		AvailableCount: 1,
		Credits: []resetCredit{
			{Status: "redeemed", ExpiresAt: "2026-07-01T00:00:00Z"},
			{Status: "available", GrantedAt: "2026-06-12T01:20:48.728491Z", ExpiresAt: "2026-07-12T01:20:48.728491Z"},
			{Status: "available", GrantedAt: "2026-06-13T01:20:48.728491Z", ExpiresAt: "2026-07-13T01:20:48.728491Z"},
		},
	}

	lines := linesFromUsageResponse(parsed, resetCredits, true)

	assertAmount(t, lines, "Reset grants", 1, "available")
	assertText(t, lines, "Grant expiry", "2026-07-12T01:20:48.728491Z")
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
