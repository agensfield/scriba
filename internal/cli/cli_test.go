package cli

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/cache"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
	"github.com/agensfield/scriba/internal/resetwatch"
	servercore "github.com/agensfield/scriba/internal/server"
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
	text := stripANSI(renderResetGrantsAt(payload, now))
	expiry := time.Date(2026, 7, 12, 1, 20, 48, 728491000, time.UTC).Local().Format("2006-01-02 15:04 MST")
	granted := time.Date(2026, 6, 12, 1, 20, 48, 728491000, time.UTC).Local().Format("2006-01-02 15:04 MST")
	for _, want := range []string{
		"Codex reset grants",
		"2 available · earliest expires " + expiry + " (in 13d)",
		"1. One free rate limit reset",
		"expires  " + expiry + " (in 13d)",
		"granted  " + granted,
		"status   available",
		"credit_1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("renderResetGrants() missing %q in:\n%s", want, text)
		}
	}
}

func stripANSI(text string) string {
	return regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(text, "")
}

func TestCodexGroupHelpListsResetGrants(t *testing.T) {
	text := groupHelp("codex")
	if !strings.Contains(text, "scriba codex reset-grants") {
		t.Fatalf("codex help missing reset-grants command:\n%s", text)
	}
}

func TestRenderCacheStatusIsHumanReadable(t *testing.T) {
	text := stripANSI(renderCacheStatus(cache.Status{
		CacheDir:      "/tmp/scriba",
		DatabasePath:  "/tmp/scriba/scriba.sqlite",
		SchemaVersion: 1,
		SizeBytes:     1536,
		WAL:           cache.WALInfo{Enabled: true, Mode: "wal", BusyTimeoutMs: 5000},
		ScanStats: []cache.ScanStatsInfo{{
			ProviderID: "codex",
			UpdatedAt:  "2026-06-29T01:20:00Z",
			Stats:      model.ScannerStats{Files: 2, Events: 42},
		}},
	}))

	for _, want := range []string{"Scriba cache", "size", "1.5 KB", "Scans", "codex", "42 events"} {
		if !strings.Contains(text, want) {
			t.Fatalf("cache status missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "{") {
		t.Fatalf("cache status looks like JSON:\n%s", text)
	}
}

func TestRenderServerRefreshAvoidsTelegramMarkup(t *testing.T) {
	used := 15.0
	expires := time.Date(2026, 7, 12, 1, 20, 0, 0, time.UTC)
	count := 1
	text := stripANSI(renderServerRefresh(servercore.PollResult{
		Observation: resetwatch.Observation{
			Account:    resetwatch.Account{Email: "arda@example.com", Plan: "pro"},
			ObservedAt: time.Date(2026, 6, 29, 1, 20, 0, 0, time.UTC),
			Windows: []resetwatch.Window{{
				Label:       resetwatch.LabelWeeklyLimit,
				UsedPercent: &used,
				ResetAt:     time.Date(2026, 7, 5, 17, 45, 0, 0, time.UTC),
			}},
			ResetGrants: resetwatch.ResetGrants{
				AvailableCount: &count,
				ExpiresAt:      expires,
			},
		},
	}))
	localExpiry := expires.Local().Format("2006-01-02 15:04 MST")

	for _, want := range []string{"Codex limits", "Weekly", "15% used", "Reset grants", localExpiry} {
		if !strings.Contains(text, want) {
			t.Fatalf("server refresh missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "<b>") || strings.Contains(text, "<pre>") {
		t.Fatalf("server refresh leaked Telegram markup:\n%s", text)
	}
}
