package render

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/model"
)

func TestCodexLimitsHidesSparkLines(t *testing.T) {
	used := 12.0
	limit := 100.0
	text := CodexLimits([]model.MetricLine{
		{Type: "badge", Label: "Plan", Text: "prolite"},
		{Type: "progress", Label: "5h limit", Used: &used, Limit: &limit},
		{Type: "progress", Label: "Spark 5h", Used: &used, Limit: &limit},
		{Type: "progress", Label: "Spark weekly", Used: &used, Limit: &limit},
	}, false)
	if strings.Contains(text, "Spark") {
		t.Fatalf("expected Spark lines to be hidden:\n%s", text)
	}
	if !strings.Contains(text, "5h limit") || !strings.Contains(text, "Plan") {
		t.Fatalf("expected non-Spark lines to remain:\n%s", text)
	}
}

func TestCodexLimitsShowsResetGrants(t *testing.T) {
	grants := 1.0
	text := stripANSI(CodexLimits([]model.MetricLine{
		{Type: "amount", Label: "Reset grants", Value: grants, Format: &model.MetricFormat{Kind: "count", Suffix: "available"}},
	}, false))

	if !strings.Contains(text, "Reset grants") || !strings.Contains(text, "1 available") {
		t.Fatalf("expected reset grants to render:\n%s", text)
	}
}

func TestCodexLimitsFormatsGrantExpiry(t *testing.T) {
	text := stripANSI(CodexLimits([]model.MetricLine{
		{Type: "text", Label: "Grant expiry", Value: "2026-07-12T01:20:48.728491Z"},
	}, false))

	want := time.Date(2026, 7, 12, 1, 20, 48, 728491000, time.UTC).Local().Format("2006-01-02 15:04 MST")
	if strings.Contains(text, "728491") || !strings.Contains(text, want) {
		t.Fatalf("expected human grant expiry:\n%s", text)
	}
}

func TestStatusHidesSparkLines(t *testing.T) {
	used := 12.0
	limit := 100.0
	text := Status(model.StatusSnapshot{
		SchemaVersion: model.SchemaVersion,
		GeneratedAt:   "2026-06-01T00:00:00Z",
		Providers: []model.ProviderSnapshot{{
			ProviderID:  "codex",
			DisplayName: "Codex",
			Lines: []model.MetricLine{
				{Type: "progress", Label: "5h limit", Used: &used, Limit: &limit},
				{Type: "progress", Label: "Spark weekly", Used: &used, Limit: &limit},
			},
		}},
	})
	if strings.Contains(text, "Spark") {
		t.Fatalf("expected Spark lines to be hidden:\n%s", text)
	}
	if !strings.Contains(text, "5h limit") {
		t.Fatalf("expected 5h line to remain:\n%s", text)
	}
}

func TestStatusSanitizesOAuthJSONErrors(t *testing.T) {
	text := stripANSI(Status(model.StatusSnapshot{
		SchemaVersion: model.SchemaVersion,
		GeneratedAt:   "2026-06-01T00:00:00Z",
		Providers: []model.ProviderSnapshot{{
			ProviderID:  "claude",
			DisplayName: "Claude",
			Provenance: []model.SourceProvenance{{
				Error: `claude OAuth credentials found but refresh failed: refresh failed: 400 {"error":"invalid_grant","error_description":"Refresh token not found or invalid"}`,
			}},
		}},
	}))

	if strings.Contains(text, `"error"`) || !strings.Contains(text, "OAuth refresh failed: Refresh token not found or invalid") {
		t.Fatalf("expected sanitized OAuth error:\n%s", text)
	}
}

func TestReportShowsHumanRows(t *testing.T) {
	cost := 1.25
	text := stripANSI(Report("Codex Weekly", []model.WeeklyReportRow{
		{
			Week: "2026-06-29",
			ReportTotals: model.ReportTotals{
				TokenUsage: model.TokenUsage{InputTokens: 1200, OutputTokens: 300, TotalTokens: 1500},
				CostUSD:    &cost,
			},
			Models: []model.ModelBreakdown{{Model: "gpt-5-codex", TokenUsage: model.TokenUsage{TotalTokens: 1500}}},
		},
	}))

	for _, want := range []string{"Codex Weekly", "1 weeks", "week of 2026-06-29", "1.5K tokens", "$1.25", "gpt-5-codex"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "rows") {
		t.Fatalf("report leaked row-count wording:\n%s", text)
	}
}

func TestCodexLimitsAlignsProgressBars(t *testing.T) {
	fiveUsed := 8.0
	weeklyUsed := 23.0
	limit := 100.0
	text := stripANSI(CodexLimits([]model.MetricLine{
		{Type: "badge", Label: "Plan", Text: "prolite"},
		{Type: "progress", Label: "5h limit", Used: &fiveUsed, Limit: &limit},
		{Type: "progress", Label: "Weekly limit", Used: &weeklyUsed, Limit: &limit},
	}, false))

	lines := strings.Split(text, "\n")
	barColumns := map[string]int{}
	for _, line := range lines {
		if strings.Contains(line, "limit") {
			barColumns[strings.TrimSpace(line[:strings.Index(line, "▰")])] = strings.Index(line, "▰")
		}
	}
	if barColumns["5h limit"] != barColumns["Weekly limit"] {
		t.Fatalf("progress bars are not aligned:\n%s", text)
	}
}

func stripANSI(text string) string {
	return regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(text, "")
}
