package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
)

func TestCodexLimitsFromSnapshotFiltersRemoteLimitLines(t *testing.T) {
	used := 12.0
	limit := 100.0
	grants := 1.0
	snapshot := model.StatusSnapshot{
		SchemaVersion: model.SchemaVersion,
		GeneratedAt:   "2026-05-19T19:30:56Z",
		Providers: []model.ProviderSnapshot{
			{
				ProviderID:  "codex",
				DisplayName: "Codex",
				State:       "ok",
				Lines: []model.MetricLine{
					{Type: "badge", Label: "Plan", Text: "prolite"},
					{Type: "progress", Label: "5h limit", Used: &used, Limit: &limit},
					{Type: "amount", Label: "Reset grants", Value: grants, Format: &model.MetricFormat{Kind: "count", Suffix: "available"}},
					{Type: "text", Label: "Today", Value: "123"},
				},
			},
		},
	}

	payload, err := codexLimitsFromSnapshot(snapshot)
	if err != nil {
		t.Fatalf("codexLimitsFromSnapshot returned error: %v", err)
	}
	if payload.Mode != "fast" {
		t.Fatalf("mode = %q, want fast", payload.Mode)
	}
	if len(payload.Lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(payload.Lines))
	}
	if payload.Lines[0].Label != "Plan" || payload.Lines[1].Label != "5h limit" || payload.Lines[2].Label != "Reset grants" {
		t.Fatalf("unexpected labels: %#v", payload.Lines)
	}
}

func TestCodexLimitsFromSnapshotRequiresCodexProvider(t *testing.T) {
	_, err := codexLimitsFromSnapshot(model.StatusSnapshot{
		SchemaVersion: model.SchemaVersion,
		Providers: []model.ProviderSnapshot{
			{ProviderID: "claude", DisplayName: "Claude"},
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRenderResetGrantsShowsEachCreditExpiry(t *testing.T) {
	payload := codexLimitsPayload{
		SchemaVersion: model.SchemaVersion,
		ProviderID:    "codex",
		Source:        "chatgpt-codex-backend",
		Mode:          "live",
		Lines: []model.MetricLine{
			{Type: "amount", Label: "Reset grants", Value: 2.0, Format: &model.MetricFormat{Kind: "count", Suffix: "available"}},
			{Type: "text", Label: "Grant expiry", Value: "2026-07-12T01:20:48.728491Z"},
		},
		ResetCredits: []remote.ResetCredit{
			{
				ID:        "credit_1",
				Status:    "available",
				ResetType: "codex_rate_limits",
				Title:     "One free rate limit reset",
				GrantedAt: "2026-06-12T01:20:48.728491Z",
				ExpiresAt: "2026-07-12T01:20:48.728491Z",
			},
		},
	}

	now := time.Date(2026, 6, 29, 1, 20, 0, 0, time.UTC)
	text := renderResetGrantsAt(payload, now)
	for _, want := range []string{
		"Codex reset grants",
		"2 available · earliest expires 2026-07-12 01:20 UTC (in 13d)",
		"1. One free rate limit reset",
		"expires  2026-07-12 01:20 UTC (in 13d)",
		"granted  2026-06-12 01:20 UTC",
		"credit_1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("renderResetGrants() missing %q in:\n%s", want, text)
		}
	}
}

func TestCodexGroupHelpListsResetGrants(t *testing.T) {
	text := groupHelp("codex")
	if !strings.Contains(text, "scriba codex reset-grants") {
		t.Fatalf("codex help missing reset-grants command:\n%s", text)
	}
}
