package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/budget"
	"github.com/agensfield/scriba/internal/budgetadapter"
	"github.com/agensfield/scriba/internal/privacy"
	"github.com/agensfield/scriba/internal/remote"
	remoteclaude "github.com/agensfield/scriba/internal/remote/claude"
	remotecodex "github.com/agensfield/scriba/internal/remote/codex"
	"github.com/agensfield/scriba/internal/server/store"
)

func runBudget(providerID string, opts options) (err error) {
	defer func() {
		if err != nil && opts.redact {
			err = redactBudgetError(err)
		}
	}()
	cfg, err := load(opts)
	if err != nil {
		return err
	}
	var result remote.ProbeResult
	switch providerID {
	case "codex":
		result, err = remotecodex.ProbeContext(context.Background(), true)
	case "claude":
		result, err = remoteclaude.Probe(true)
	default:
		return fmt.Errorf("unsupported budget provider: %s", providerID)
	}
	if err != nil {
		return err
	}
	if !result.AuthState.OK {
		if result.AuthState.Error != "" {
			return fmt.Errorf("%s auth unavailable: %s", providerID, result.AuthState.Error)
		}
		return fmt.Errorf("%s auth unavailable", providerID)
	}
	now := time.Now().UTC()
	observedAt := now
	observation := budgetadapter.FromMetricLines(providerID, observedAt, result.Lines)
	history, historyState, err := budgetHistory(context.Background(), providerID, result.AuthState, cfg.Server.StatePath, opts.statePath, observedAt)
	if err != nil {
		return err
	}
	report := budget.Evaluate(budget.Input{ProviderID: providerID, Observation: observation, History: history, HistoryState: historyState}, now)
	return output(opts, report, renderBudget(report))
}

func redactBudgetError(err error) error {
	return errors.New(fmt.Sprint(privacy.Redact(err.Error())))
}

func budgetHistory(ctx context.Context, providerID string, auth remote.AuthState, configured, override string, observedAt time.Time) ([]budget.Observation, budget.HistoryState, error) {
	if providerID != "codex" {
		return nil, budget.HistoryUnavailable, nil
	}
	if override != "" {
		configured = override
	}
	path := resolveServerStatePath(configured)
	st, err := store.OpenReadOnly(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, budget.HistoryUnavailable, nil
	}
	if err != nil {
		return nil, budget.HistoryUnavailable, fmt.Errorf("open budget history: %w", err)
	}
	defer func() { _ = st.Close() }()
	history, err := st.LoadBudgetHistory(ctx, "codex", remote.AccountRef(auth), observedAt.Add(-24*time.Hour))
	if err != nil {
		return nil, budget.HistoryUnavailable, fmt.Errorf("load budget history: %w", err)
	}
	state := budget.HistoryAvailable
	if len(history) == 0 {
		state = budget.HistoryEmpty
	}
	return history, state, nil
}

func renderBudget(report budget.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", cliHeader(title(report.ProviderID)+" budget"))
	b.WriteString(cliMuted(budgetHistorySummary(report.History)))
	if len(report.Windows) == 0 {
		b.WriteString("\n" + cliYellow("No quota windows were returned."))
		return b.String()
	}
	for _, window := range report.Windows {
		fmt.Fprintf(&b, "\n\n%s · %s\n", cliBold(window.Label), budgetRisk(window.Risk))
		fmt.Fprintf(&b, "%s used, %s left.\n", budgetPercent(window.UsedPercent), budgetPercent(window.RemainingPercentPoints))
		b.WriteString(budgetPaceSummary(window))
		if window.ResetAt != nil {
			fmt.Fprintf(&b, "\nResets %s.", budgetTime(window.ResetAt))
		}
		if window.Confidence == "low" || window.Confidence == "none" || window.Freshness != "fresh" {
			fmt.Fprintf(&b, "\n%s", cliMuted(budgetSignalSummary(window)))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func budgetHistorySummary(history budget.History) string {
	switch history.State {
	case budget.HistoryAvailable:
		if history.SampleCount == 1 {
			return "Using 1 recent sample for pace estimates."
		}
		return fmt.Sprintf("Using %d recent samples for pace estimates.", history.SampleCount)
	case budget.HistoryEmpty:
		return "No recent history yet; estimates use this cycle and may change."
	default:
		return "No stored history; estimates use this cycle and may change."
	}
}

func budgetPaceSummary(window budget.Window) string {
	if window.PaceBurnPercentPointsPerHour == nil || window.SafeHourlyAllowancePercentPoints == nil {
		return "Not enough data to estimate a sustainable pace."
	}
	pace, safe := *window.PaceBurnPercentPointsPerHour, *window.SafeHourlyAllowancePercentPoints
	if pace == 0 {
		return fmt.Sprintf("No usage yet. You can spend up to %.2f%% per hour and stay on track.", safe)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Current pace %.2f%% per hour; sustainable pace %.2f%% per hour.", pace, safe)
	if window.ProjectedExhaustionAt != nil {
		fmt.Fprintf(&b, "\nAt this pace, the limit runs out %s", budgetTime(window.ProjectedExhaustionAt))
		if window.ResetAt != nil {
			delta := window.ProjectedExhaustionAt.Sub(*window.ResetAt)
			switch {
			case delta < -time.Minute:
				fmt.Fprintf(&b, ", %s before reset", budgetDuration(-delta))
			case delta > time.Minute:
				fmt.Fprintf(&b, ", %s after reset", budgetDuration(delta))
			}
		}
		b.WriteString(".")
	}
	return b.String()
}

func budgetSignalSummary(window budget.Window) string {
	confidence := window.Confidence
	if confidence == "none" {
		confidence = "very low"
	}
	if window.Freshness == "fresh" {
		return fmt.Sprintf("Estimate confidence: %s.", confidence)
	}
	return fmt.Sprintf("Estimate confidence: %s; data is %s.", confidence, window.Freshness)
}

func budgetDuration(value time.Duration) string {
	value = value.Round(time.Hour)
	if value < time.Hour {
		return "less than an hour"
	}
	hours := int(value / time.Hour)
	days, hours := hours/24, hours%24
	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case days > 0:
		return fmt.Sprintf("%dd", days)
	default:
		return fmt.Sprintf("%dh", hours)
	}
}

func budgetPercent(value *float64) string {
	if value == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.1f%%", *value)
}

func budgetTime(value *time.Time) string {
	if value == nil {
		return "unknown"
	}
	return value.Local().Format("Mon Jan 2 at 15:04 MST")
}

func budgetRisk(risk string) string {
	switch risk {
	case "low":
		return cliGreen("on track")
	case "elevated":
		return cliYellow("pace is getting tight")
	case "high":
		return cliYellow("spending too fast")
	case "critical":
		return cliYellow("limit exhausted")
	default:
		return cliMuted("not enough data")
	}
}
