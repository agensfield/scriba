package render

import (
	"regexp"
	"strings"
	"testing"

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
