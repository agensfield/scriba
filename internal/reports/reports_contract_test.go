package reports

import (
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/model"
)

func TestContractTimezoneAndDSTBuckets(t *testing.T) {
	events := []model.LocalUsageEvent{
		{ProviderID: "codex", Timestamp: "2026-03-08T06:59:59Z", Model: "gpt", TotalTokens: 1},
		{ProviderID: "codex", Timestamp: "2026-03-08T07:00:00Z", Model: "gpt", TotalTokens: 2},
		{ProviderID: "codex", Timestamp: "2026-07-09T21:00:00Z", Model: "gpt", TotalTokens: 4},
	}
	tests := []struct {
		name  string
		dates []string
	}{
		{"UTC", []string{"2026-03-08", "2026-07-09"}},
		{"Europe/Istanbul", []string{"2026-03-08", "2026-07-10"}},
		{"America/New_York", []string{"2026-03-08", "2026-07-09"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc, err := time.LoadLocation(tt.name)
			if err != nil {
				t.Fatal(err)
			}
			rows := DailyIn(append([]model.LocalUsageEvent(nil), events...), false, loc)
			if len(rows) != len(tt.dates) {
				t.Fatalf("rows=%+v", rows)
			}
			for i := range rows {
				if rows[i].Date != tt.dates[i] {
					t.Fatalf("rows=%+v", rows)
				}
			}
		})
	}
}

func TestContractNewYorkDSTDateFilter(t *testing.T) {
	// TODO(contract): ApplyFiltersIn compares differently formatted RFC3339
	// strings lexically, excluding part of this 23-hour local day. Do not freeze
	// the undercount as a golden; enable this when filtering compares instants.
	t.Skip("known defect: timezone date filter uses lexical timestamp comparison")
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	events := []model.LocalUsageEvent{{Timestamp: "2026-03-08T04:59:59Z"}, {Timestamp: "2026-03-08T05:00:00Z"}, {Timestamp: "2026-03-09T03:59:59Z"}, {Timestamp: "2026-03-09T04:00:00Z"}}
	got := ApplyFiltersIn(events, Filters{Since: "2026-03-08", Until: "2026-03-08"}, loc)
	if len(got) != 2 || got[0].Timestamp != "2026-03-08T05:00:00Z" || got[1].Timestamp != "2026-03-09T03:59:59Z" {
		t.Fatalf("filtered=%+v", got)
	}
}
