package render

import (
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
