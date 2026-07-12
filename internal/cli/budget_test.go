package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/budget"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
)

func TestBudgetHistoryIsUnavailableWithoutStateDatabase(t *testing.T) {
	history, state, err := budgetHistory(context.Background(), "codex", remote.AuthState{AccountID: "acct"}, filepath.Join(t.TempDir(), "missing.sqlite"), "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if history != nil || state != budget.HistoryUnavailable {
		t.Fatalf("history=%#v state=%q", history, state)
	}
}

func TestClaudeBudgetHistoryIsExplicitlyUnavailable(t *testing.T) {
	history, state, err := budgetHistory(context.Background(), "claude", remote.AuthState{}, "", "", time.Now())
	if err != nil || history != nil || state != budget.HistoryUnavailable {
		t.Fatalf("history=%#v state=%q err=%v", history, state, err)
	}
}

func TestProbeObservedAtUsesProviderProvenance(t *testing.T) {
	want := time.Date(2026, 7, 12, 2, 3, 4, 0, time.UTC)
	got := probeObservedAt(remote.ProbeResult{Provenance: []model.SourceProvenance{{FetchedAt: want.Format(time.RFC3339Nano)}}}, want.Add(time.Hour))
	if !got.Equal(want) {
		t.Fatalf("observed at = %s, want %s", got, want)
	}
}

func TestRenderBudgetShowsPacingAndReasons(t *testing.T) {
	used, remaining, pace, safe := 72.5, 27.5, 3.25, 1.5
	reset := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	report := budget.Report{
		ProviderID: "codex",
		History:    budget.History{State: budget.HistoryAvailable, SampleCount: 3},
		Windows: []budget.Window{{
			Label: "5h limit", Risk: "high", Freshness: "fresh", Confidence: "high",
			UsedPercent: &used, RemainingPercentPoints: &remaining,
			PaceBurnPercentPointsPerHour: &pace, SafeHourlyAllowancePercentPoints: &safe,
			ResetAt: &reset, Reasons: []string{"recent_estimate_available"},
		}},
	}
	text := stripANSI(renderBudget(report))
	for _, want := range []string{"Codex budget", "history available", "samples 3", "5h limit", "high", "72.5% used", "27.5% remaining", "3.25pp/h", "safe 1.50pp/h", "fresh · high confidence", "recent_estimate_available"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
}

func TestBudgetAccountRefPrefersProviderAccountID(t *testing.T) {
	if got := remote.AccountRef(remote.AuthState{AccountID: "acct-live", Email: "other@example.com"}); got != "acct-live" {
		t.Fatalf("account ref = %q", got)
	}
	if a, b := remote.AccountRef(remote.AuthState{Email: "same@example.com"}), remote.AccountRef(remote.AuthState{Email: "same@example.com"}); a != b || !strings.HasPrefix(a, "acct_") {
		t.Fatalf("derived refs = %q %q", a, b)
	}
}

func TestBudgetCommandsArePublishedInHelpAndSchema(t *testing.T) {
	if !strings.Contains(groupHelp("codex"), "scriba codex budget") || !strings.Contains(groupHelp("claude"), "scriba claude budget") {
		t.Fatal("budget commands missing from provider help")
	}
	if !containsCommand(commands()["codex"], "budget") || !containsCommand(commands()["claude"], "budget") {
		t.Fatal("budget commands missing from command schema")
	}
}

func containsCommand(commands []string, want string) bool {
	for _, command := range commands {
		if command == want {
			return true
		}
	}
	return false
}
