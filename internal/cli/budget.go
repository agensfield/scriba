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
	fmt.Fprintf(&b, "%s %s · %s %d\n", cliLabel("history"), report.History.State, cliMuted("samples"), report.History.SampleCount)
	if len(report.Windows) == 0 {
		b.WriteString("\n" + cliYellow("No quota windows were returned."))
		return b.String()
	}
	for _, window := range report.Windows {
		fmt.Fprintf(&b, "\n%s  %s\n", cliBold(window.Label), budgetRisk(window.Risk))
		fmt.Fprintf(&b, "  %-12s %s used · %s remaining\n", cliLabel("quota"), budgetPercent(window.UsedPercent), budgetPercent(window.RemainingPercentPoints))
		fmt.Fprintf(&b, "  %-12s %s/h · safe %s/h\n", cliLabel("pace"), budgetNumber(window.PaceBurnPercentPointsPerHour), budgetNumber(window.SafeHourlyAllowancePercentPoints))
		fmt.Fprintf(&b, "  %-12s %s\n", cliLabel("reset"), budgetTime(window.ResetAt))
		fmt.Fprintf(&b, "  %-12s %s · %s confidence\n", cliLabel("signal"), window.Freshness, window.Confidence)
		if window.ProjectedExhaustionAt != nil {
			fmt.Fprintf(&b, "  %-12s %s\n", cliLabel("exhaustion"), budgetTime(window.ProjectedExhaustionAt))
		}
		if len(window.Reasons) > 0 {
			fmt.Fprintf(&b, "  %-12s %s\n", cliLabel("reasons"), strings.Join(window.Reasons, ", "))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func budgetPercent(value *float64) string {
	if value == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.1f%%", *value)
}

func budgetNumber(value *float64) string {
	if value == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.2fpp", *value)
}

func budgetTime(value *time.Time) string {
	if value == nil {
		return "unknown"
	}
	return value.Local().Format("2006-01-02 15:04 MST")
}

func budgetRisk(risk string) string {
	switch risk {
	case "low":
		return cliGreen(risk)
	case "elevated", "high", "critical":
		return cliYellow(risk)
	default:
		return cliMuted(risk)
	}
}
