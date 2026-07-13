package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/budget"
	"github.com/agensfield/scriba/internal/remote"
)

func TestBudgetErrorRedaction(t *testing.T) {
	got := redactBudgetError(errors.New("open /Users/arda/private.sqlite: nope")).Error()
	if strings.Contains(got, "/Users/arda") || !strings.Contains(got, "/Users/[redacted]") {
		t.Fatalf("error was not redacted: %q", got)
	}
}

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

func TestRenderBudgetExplainsPacingWithoutMachineReasonCodes(t *testing.T) {
	used, remaining, pace, safe := 72.5, 27.5, 3.25, 1.5
	reset := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	exhaustion := reset.Add(-6 * time.Hour)
	report := budget.Report{
		ProviderID: "codex",
		History:    budget.History{State: budget.HistoryAvailable, SampleCount: 3},
		Windows: []budget.Window{{
			Label: "5h limit", Risk: "high", Freshness: "fresh", Confidence: "high",
			UsedPercent: &used, RemainingPercentPoints: &remaining,
			PaceBurnPercentPointsPerHour: &pace, SafeHourlyAllowancePercentPoints: &safe,
			ResetAt: &reset, ProjectedExhaustionAt: &exhaustion, Reasons: []string{"recent_estimate_available"},
		}},
	}
	text := stripANSI(renderBudget(report))
	for _, want := range []string{"Codex budget", "Using 3 recent samples", "5h limit · spending too fast", "72.5% used, 27.5% left", "Current pace 3.25% per hour", "sustainable pace 1.50% per hour", "6h before reset", "Resets"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "recent_estimate_available") || strings.Contains(text, "reasons") {
		t.Fatalf("machine reasons leaked into human output:\n%s", text)
	}
}

func TestRenderBudgetMakesZeroBurnPlain(t *testing.T) {
	used, remaining, pace, safe := 0.0, 100.0, 0.0, 0.6
	reset := time.Date(2026, 7, 20, 20, 33, 0, 0, time.UTC)
	report := budget.Report{ProviderID: "codex", History: budget.History{State: budget.HistoryEmpty}, Windows: []budget.Window{{Label: "Spark weekly", Risk: "low", Freshness: "fresh", Confidence: "low", UsedPercent: &used, RemainingPercentPoints: &remaining, PaceBurnPercentPointsPerHour: &pace, SafeHourlyAllowancePercentPoints: &safe, ResetAt: &reset, Reasons: []string{"burn_zero", "projection_unavailable"}}}}
	text := stripANSI(renderBudget(report))
	for _, want := range []string{"No recent history yet", "Spark weekly · on track", "0.0% used, 100.0% left", "No usage yet", "up to 0.60% per hour", "Estimate confidence: low"} {
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
