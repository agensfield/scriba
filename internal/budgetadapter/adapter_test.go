package budgetadapter

import (
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/model"
)

func TestWindowKeyExactMappings(t *testing.T) {
	tests := map[string]struct{ provider, label string }{
		"primary.five_hour": {"codex", "5h limit"}, "primary.weekly": {"codex", "Weekly limit"},
		"spark.five_hour": {"codex", "Spark 5h"}, "spark.weekly": {"codex", "Spark weekly"},
		"review.five_hour": {"codex", "Review 5h"}, "review.weekly": {"codex", "Review weekly"},
		"oauth_apps.weekly": {"claude", "OAuth Apps"}, "sonnet.weekly": {"claude", "Sonnet"},
		"design.weekly": {"claude", "Claude Design"}, "routines.weekly": {"claude", "Claude Routines"},
	}
	for want, tc := range tests {
		if got, ok := WindowKey(tc.provider, tc.label); !ok || string(got) != want {
			t.Errorf("%s/%s = %q, %v", tc.provider, tc.label, got, ok)
		}
	}
	if _, ok := WindowKey("codex", "Credits left"); ok {
		t.Fatal("non-window Codex label mapped")
	}
}

func TestClaudeCanonicalDurationsAndNonProgressIsolation(t *testing.T) {
	used := 25.0
	obs := FromMetricLines("claude", time.Unix(1, 0), []model.MetricLine{
		{Type: "progress", Label: "5h limit", Used: &used}, {Type: "progress", Label: "Weekly limit", Used: &used},
		{Type: "amount", Label: "Lifetime tokens", Value: 9e12},
	})
	if len(obs.Windows) != 2 || obs.Windows[0].PeriodDuration != 5*time.Hour || obs.Windows[1].PeriodDuration != 7*24*time.Hour {
		t.Fatalf("unexpected windows: %#v", obs.Windows)
	}
}
