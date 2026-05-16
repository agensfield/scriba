package reports

import (
	"testing"

	"github.com/agensfield/scriba/internal/model"
)

func TestDailyAggregatesModelsAndDateFilters(t *testing.T) {
	cost := 0.25
	events := []model.LocalUsageEvent{
		{ProviderID: "codex", Timestamp: "2026-05-15T23:00:00.000Z", Model: "gpt-5", InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
		{ProviderID: "codex", Timestamp: "2026-05-16T10:00:00.000Z", Model: "gpt-5", InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CostUSD: &cost},
		{ProviderID: "codex", Timestamp: "2026-05-16T11:00:00.000Z", Model: "gpt-5.4", InputTokens: 7, OutputTokens: 8, TotalTokens: 15},
	}

	filtered := ApplyFilters(events, Filters{Since: "2026-05-16", Until: "2026-05-16"})
	rows := Daily(filtered, true)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Date != "2026-05-16" || row.TotalTokens != 30 || row.InputTokens != 17 || row.OutputTokens != 13 {
		t.Fatalf("row = %+v", row)
	}
	if row.CostUSD == nil || *row.CostUSD != cost {
		t.Fatalf("cost = %v, want %v", row.CostUSD, cost)
	}
	if len(row.Models) != 2 || row.Models[0].TotalTokens != 15 || row.Models[1].TotalTokens != 15 {
		t.Fatalf("models = %+v", row.Models)
	}
}
