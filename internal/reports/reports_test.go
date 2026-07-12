package reports

import (
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/model"
)

func TestDailyAggregatesModelsAndDateFilters(t *testing.T) {
	cost := 0.25
	events := []model.LocalUsageEvent{
		{ProviderID: "codex", Timestamp: "2026-05-15T23:00:00.000Z", Model: "gpt-5", InputTokens: 1, UncachedInputTokens: 1, OutputTokens: 2, EffectiveTokens: 3, TotalTokens: 3},
		{ProviderID: "codex", Timestamp: "2026-05-16T10:00:00.000Z", Model: "gpt-5", InputTokens: 10, UncachedInputTokens: 8, CachedInputTokens: 2, OutputTokens: 5, EffectiveTokens: 13, TotalTokens: 15, CostUSD: &cost},
		{ProviderID: "codex", Timestamp: "2026-05-16T11:00:00.000Z", Model: "gpt-5.4", InputTokens: 7, UncachedInputTokens: 4, CachedInputTokens: 3, OutputTokens: 8, EffectiveTokens: 12, TotalTokens: 15},
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
	if row.UncachedInputTokens != 12 || row.CachedInputTokens != 5 || row.EffectiveTokens != 25 {
		t.Fatalf("effective row = %+v", row)
	}
	if row.CostUSD == nil || *row.CostUSD != cost {
		t.Fatalf("cost = %v, want %v", row.CostUSD, cost)
	}
	if len(row.Models) != 2 || row.Models[0].TotalTokens != 15 || row.Models[1].TotalTokens != 15 {
		t.Fatalf("models = %+v", row.Models)
	}
}

func TestDailyAndDateFiltersUseRequestedTimezone(t *testing.T) {
	istanbul, err := time.LoadLocation("Europe/Istanbul")
	if err != nil {
		t.Fatal(err)
	}
	events := []model.LocalUsageEvent{
		{ProviderID: "codex", Timestamp: "2026-07-09T20:59:59Z", TotalTokens: 10},
		{ProviderID: "codex", Timestamp: "2026-07-09T21:00:00Z", TotalTokens: 20},
	}

	rows := DailyIn(events, false, istanbul)
	if len(rows) != 2 || rows[0].Date != "2026-07-09" || rows[0].TotalTokens != 10 || rows[1].Date != "2026-07-10" || rows[1].TotalTokens != 20 {
		t.Fatalf("rows = %+v", rows)
	}
	filtered := ApplyFiltersIn(events, Filters{Since: "2026-07-10", Until: "2026-07-10"}, istanbul)
	if len(filtered) != 1 || filtered[0].TotalTokens != 20 {
		t.Fatalf("filtered = %+v", filtered)
	}
}

func TestLocationRejectsInvalidName(t *testing.T) {
	if _, err := Location("Mars/Olympus_Mons"); err == nil {
		t.Fatal("expected invalid timezone error")
	}
}

func TestApplyFiltersComparesRFC3339TimestampsAsInstants(t *testing.T) {
	events := []model.LocalUsageEvent{
		{Timestamp: "2026-07-10T00:30:00+03:00", TotalTokens: 1},
		{Timestamp: "2026-07-09T22:00:00Z", TotalTokens: 2},
	}
	got := ApplyFilters(events, Filters{Since: "2026-07-09T21:45:00Z"})
	if len(got) != 1 || got[0].TotalTokens != 2 {
		t.Fatalf("filtered = %+v", got)
	}
}

func TestApplyFiltersPreservesLexicalFallbackForInvalidTimestamps(t *testing.T) {
	events := []model.LocalUsageEvent{{Timestamp: "bad-a"}, {Timestamp: "bad-z"}}
	got := ApplyFilters(events, Filters{Since: "bad-m"})
	if len(got) != 1 || got[0].Timestamp != "bad-z" {
		t.Fatalf("filtered = %+v", got)
	}
}
