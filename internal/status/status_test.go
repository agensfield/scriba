package status

import (
	"strings"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/model"
)

func TestProviderFromDailyShowsEffectiveAndTrafficTotals(t *testing.T) {
	location := time.FixedZone("test", 3*60*60)
	today := time.Now().In(location).Format("2006-01-02")
	provider := providerFromDaily("codex", "Codex", []model.DailyReportRow{{
		Date: today,
		ReportTotals: model.ReportTotals{TokenUsage: model.TokenUsage{
			EffectiveTokens: 30,
			TotalTokens:     110,
		}},
	}}, model.ScannerStats{Files: 1}, time.Now().UTC().Format(time.RFC3339Nano), location)

	got := provider.Lines[0].Value.(string)
	if !strings.Contains(got, "30 effective") || !strings.Contains(got, "110 traffic") {
		t.Fatalf("today = %q", got)
	}
}
