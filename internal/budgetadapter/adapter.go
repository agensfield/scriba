// Package budgetadapter normalizes provider quota windows for budget evaluation.
package budgetadapter

import (
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/budget"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/resetwatch"
)

const (
	KeyFiveHour         budget.WindowKey = "primary.five_hour"
	KeySevenDay         budget.WindowKey = "primary.weekly"
	KeySparkFiveHour    budget.WindowKey = "spark.five_hour"
	KeySparkSevenDay    budget.WindowKey = "spark.weekly"
	KeyReviewFiveHour   budget.WindowKey = "review.five_hour"
	KeyReviewSevenDay   budget.WindowKey = "review.weekly"
	KeyOAuthSevenDay    budget.WindowKey = "oauth_apps.weekly"
	KeySonnetSevenDay   budget.WindowKey = "sonnet.weekly"
	KeyDesignSevenDay   budget.WindowKey = "design.weekly"
	KeyRoutinesSevenDay budget.WindowKey = "routines.weekly"
	KeyExtraWindow      budget.WindowKey = "extra.current"
)

// FromResetwatch converts a durable provider observation without consulting
// token traffic or snapshot logs.
func FromResetwatch(obs resetwatch.Observation) budget.Observation {
	windows := make([]budget.WindowObservation, 0, len(obs.Windows))
	for _, window := range obs.Windows {
		key, ok := WindowKey(obs.ProviderID, window.Label)
		if !ok {
			continue
		}
		var period time.Duration
		if window.PeriodDurationMs != nil {
			period = time.Duration(*window.PeriodDurationMs) * time.Millisecond
		} else {
			period = canonicalDuration(obs.ProviderID, key)
		}
		windows = append(windows, budget.WindowObservation{Key: key, Label: window.Label, UsedPercent: window.UsedPercent, ResetAt: window.ResetAt, PeriodDuration: period})
	}
	return budget.Observation{ObservedAt: obs.ObservedAt, Windows: windows}
}

// FromMetricLines converts a live provider response. Only progress windows are
// accepted; badges, token counts, credits, and logs cannot affect budgets.
func FromMetricLines(providerID string, observedAt time.Time, lines []model.MetricLine) budget.Observation {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	windows := make([]budget.WindowObservation, 0, len(lines))
	for _, line := range lines {
		if line.Type != "progress" {
			continue
		}
		key, ok := WindowKey(providerID, line.Label)
		if !ok {
			continue
		}
		var reset time.Time
		if line.ResetsAt != "" {
			reset, _ = time.Parse(time.RFC3339Nano, line.ResetsAt)
		}
		period := canonicalDuration(providerID, key)
		if line.PeriodDurationMs != nil {
			period = time.Duration(*line.PeriodDurationMs) * time.Millisecond
		}
		windows = append(windows, budget.WindowObservation{Key: key, Label: line.Label, UsedPercent: line.Used, ResetAt: reset, PeriodDuration: period})
	}
	return budget.Observation{ObservedAt: observedAt, Windows: windows}
}

func WindowKey(providerID, label string) (budget.WindowKey, bool) {
	providerID, label = strings.ToLower(strings.TrimSpace(providerID)), strings.ToLower(strings.TrimSpace(label))
	var key budget.WindowKey
	switch providerID + "\x00" + label {
	case "codex\x005h limit", "claude\x005h limit":
		key = KeyFiveHour
	case "codex\x00weekly limit", "claude\x00weekly limit":
		key = KeySevenDay
	case "codex\x00spark 5h":
		key = KeySparkFiveHour
	case "codex\x00spark weekly":
		key = KeySparkSevenDay
	case "codex\x00review 5h":
		key = KeyReviewFiveHour
	case "codex\x00review weekly":
		key = KeyReviewSevenDay
	case "claude\x00oauth apps":
		key = KeyOAuthSevenDay
	case "claude\x00sonnet":
		key = KeySonnetSevenDay
	case "claude\x00claude design":
		key = KeyDesignSevenDay
	case "claude\x00claude routines":
		key = KeyRoutinesSevenDay
	case "claude\x00extra claude window":
		key = KeyExtraWindow
	default:
		return "", false
	}
	return key, true
}

func canonicalDuration(providerID string, key budget.WindowKey) time.Duration {
	if providerID != "claude" {
		return 0
	}
	if key == KeyFiveHour {
		return 5 * time.Hour
	}
	return 7 * 24 * time.Hour
}
