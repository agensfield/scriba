package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server"
	"github.com/agensfield/scriba/internal/server/store"
)

func TestRenderResetIncludesJokeAccountAndBeforeAfterBars(t *testing.T) {
	event := resetwatch.Event{
		ID:                   "reset_1",
		Account:              resetwatch.Account{Ref: "acct", Label: "personal", Email: "arda@example.com", Plan: "plus"},
		PrimaryTriggerLabel:  resetwatch.LabelWeeklyLimit,
		ResetKind:            resetwatch.ResetKindEarly,
		PreviousResetAt:      parseTime("2026-06-06T21:00:00Z"),
		CurrentResetAt:       parseTime("2026-06-09T12:00:00Z"),
		PreviousSnapshotJSON: snapshot("2026-06-06T21:00:00Z", 51),
		CurrentSnapshotJSON:  snapshot("2026-06-09T12:00:00Z", 0),
		JokeID:               "tibo-ceiling",
	}
	text := RenderReset(event)
	for _, want := range []string{
		"<b>Codex reset notification</b>",
		"Tibo moved the ceiling again.",
		"<b>Account</b> personal",
		"<b>Trigger</b>",
		"window   Weekly",
		"before",
		"after",
		"▰▰▰▰▰▰▱▱▱▱",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q in:\n%s", want, text)
		}
	}
}

func TestRenderLimitsUsesHTMLSectionsAndFreshness(t *testing.T) {
	text := RenderLimits(resetwatch.Observation{
		Account:    resetwatch.Account{Label: "personal", Email: "arda@example.com", Plan: "prolite"},
		ObservedAt: parseTime("2026-06-01T00:00:00Z"),
		Windows: []resetwatch.Window{
			{Label: resetwatch.LabelWeeklyLimit, UsedPercent: ptrFloat(3), ResetAt: parseTime("2026-06-07T16:39:00Z")},
			{Label: resetwatch.LabelFiveHour, UsedPercent: ptrFloat(6), ResetAt: parseTime("2026-06-01T02:39:00Z")},
			{Label: resetwatch.LabelSparkWeekly, UsedPercent: ptrFloat(3), ResetAt: parseTime("2026-06-07T16:39:00Z")},
		},
	})
	for _, want := range []string{
		"<b>Codex limits</b>",
		"<i>observed ",
		"<b>Primary</b>",
		"<b>Secondary</b>",
		"<pre>",
		"Weekly",
		"5h",
		"Spark weekly",
		"▰▱▱▱▱▱▱▱▱▱",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q in:\n%s", want, text)
		}
	}
	if five, weekly := strings.Index(text, "5h"), strings.Index(text, "Weekly"); five < 0 || weekly < 0 || five > weekly {
		t.Fatalf("expected 5h before weekly in:\n%s", text)
	}
}

func TestRenderLimitWarningShowsCheckpoint(t *testing.T) {
	text := RenderLimitWarning(resetwatch.WarningEvent{
		Account:            resetwatch.Account{Label: "personal", Email: "arda@example.com", Plan: "prolite"},
		Label:              resetwatch.LabelFiveHour,
		ThresholdRemaining: 5,
		UsedPercent:        96,
		RemainingPercent:   4,
		ResetAt:            parseTime("2026-06-01T02:39:00Z"),
		DetectedAt:         parseTime("2026-06-01T00:39:00Z"),
	})
	for _, want := range []string{
		"<b>Codex limit warning</b>",
		"<b>5h</b>",
		"left",
		"4%",
		"checkpoint 5%",
		"used",
		"96%",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q in:\n%s", want, text)
		}
	}
}

func TestRenderStatsShowsStorageFreshnessAndDeliveries(t *testing.T) {
	text := RenderStats(server.Stats{
		PollInterval:             5 * time.Minute,
		ObservationRetentionDays: 120,
		Store:                    storeStatsFixture(),
	}, "prod", true)
	for _, want := range []string{
		"<b>Scriba stats</b>",
		"<b>Health</b>",
		"poll",
		"5m",
		"<b>Observation</b>",
		"latest",
		"latest win",
		"<b>Storage</b>",
		"stored polls",
		"stored win",
		"tracked win",
		"<b>Reset deliveries</b>",
		"delivered",
		"<b>Warning deliveries</b>",
		"<b>Recent</b>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q in:\n%s", want, text)
		}
	}
}

func TestAuthorizationRequiresChatAndAllowedUser(t *testing.T) {
	svc := &Service{cfg: BotConfig{ChatID: 123, AllowedUserIDs: []int64{7}}}
	if !svc.authorized(&models.Update{Message: &models.Message{Chat: models.Chat{ID: 123}, From: &models.User{ID: 7}}}) {
		t.Fatal("expected authorized update")
	}
	if svc.authorized(&models.Update{Message: &models.Message{Chat: models.Chat{ID: 999}, From: &models.User{ID: 7}}}) {
		t.Fatal("wrong chat was authorized")
	}
	if svc.authorized(&models.Update{Message: &models.Message{Chat: models.Chat{ID: 123}, From: &models.User{ID: 8}}}) {
		t.Fatal("wrong user was authorized")
	}
}

func TestHandleSettingsCallbackUpdatesPollInterval(t *testing.T) {
	controller := &fakeController{interval: 5 * time.Minute}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	svc.handleCallback(context.Background(), &models.CallbackQuery{Data: "settings:poll:10m"})
	if controller.interval != 10*time.Minute {
		t.Fatalf("interval was not updated: %s", controller.interval)
	}
}

func TestLimitsCommandUsesCachedObservation(t *testing.T) {
	controller := &fakeController{
		latest: resetwatch.Observation{
			Account:    resetwatch.Account{Label: "personal"},
			ObservedAt: parseTime("2026-06-01T00:00:00Z"),
			Windows: []resetwatch.Window{
				{Label: resetwatch.LabelWeeklyLimit, UsedPercent: ptrFloat(3), ResetAt: parseTime("2026-06-07T16:39:00Z")},
			},
		},
		latestOK: true,
	}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	reply, _ := svc.handleCommand(context.Background(), "/limits")
	if !strings.Contains(reply, "<b>Codex limits</b>") {
		t.Fatalf("unexpected limits reply: %s", reply)
	}
	if controller.refreshes != 0 {
		t.Fatalf("/limits should not force refresh, got %d refreshes", controller.refreshes)
	}
}

func TestStripTelegramHTMLFallback(t *testing.T) {
	text := "<b>Codex limits</b>\n<pre>Weekly &lt;ok&gt;</pre>"
	got := stripTelegramHTML(text)
	if got != "Codex limits\nWeekly <ok>" {
		t.Fatalf("unexpected stripped text: %q", got)
	}
}

func TestCommandNameNormalizesBotSuffix(t *testing.T) {
	if got := commandName("/Status@codexusagebot now"); got != "/status" {
		t.Fatalf("unexpected command: %q", got)
	}
}

type fakeController struct {
	interval  time.Duration
	latest    resetwatch.Observation
	latestOK  bool
	refreshes int
}

func (f *fakeController) RefreshNow(context.Context) (server.PollResult, error) {
	f.refreshes++
	return server.PollResult{}, nil
}

func (f *fakeController) PollInterval(context.Context) (time.Duration, error) {
	return f.interval, nil
}

func (f *fakeController) SetPollInterval(_ context.Context, interval time.Duration) error {
	f.interval = interval
	return nil
}

func (f *fakeController) LastResetEvent(context.Context) (resetwatch.Event, bool, error) {
	return resetwatch.Event{}, false, nil
}

func (f *fakeController) LatestObservation(context.Context) (resetwatch.Observation, bool, error) {
	return f.latest, f.latestOK, nil
}

func (f *fakeController) Stats(context.Context) (server.Stats, error) {
	return server.Stats{PollInterval: f.interval, ObservationRetentionDays: 120, Store: storeStatsFixture(), Health: healthFixture(), Version: "0.1.0-alpha.1", Commit: "test"}, nil
}

func (f *fakeController) Health(context.Context) (server.Health, error) {
	return healthFixture(), nil
}

func healthFixture() server.Health {
	lastOK := parseTime("2026-06-01T00:00:00Z")
	next := parseTime("2026-06-01T00:05:00Z")
	return server.Health{
		Status:              server.HealthOK,
		Version:             "0.1.0-alpha.1",
		Commit:              "test",
		PollInterval:        5 * time.Minute,
		LastSuccessAt:       &lastOK,
		NextPollEstimateAt:  &next,
		StaleAfter:          10 * time.Minute,
		ConsecutiveFailures: 0,
	}
}

func storeStatsFixture() store.Stats {
	return store.Stats{
		Path:          "/tmp/scriba.sqlite",
		SchemaVersion: 3,
		DBFiles:       store.DBFileStats{MainBytes: 1024, WALBytes: 512, TotalBytes: 1536},
		Counts: map[string]int64{
			"accounts":                 1,
			"limit_observations":       9,
			"observed_windows":         36,
			"limit_windows":            4,
			"reset_events":             2,
			"limit_warning_events":     3,
			"notification_deliveries":  2,
			"limit_warning_deliveries": 3,
		},
		ResetDeliveries: map[string]store.DeliveryCounts{
			"delivered": {Count: 2, Attempts: 2},
		},
		WarningDeliveries: map[string]store.DeliveryCounts{
			"pending":   {Count: 1, Attempts: 0},
			"delivered": {Count: 2, Attempts: 2},
		},
		LatestObservation: &store.ObservationSummary{
			ObservedAt:   parseTime("2026-06-01T00:00:00Z"),
			AccountLabel: "personal",
			AccountEmail: "arda@example.com",
			AccountPlan:  "prolite",
			Windows:      4,
		},
		LastReset: &store.ResetSummary{
			Trigger:    resetwatch.LabelWeeklyLimit,
			Kind:       resetwatch.ResetKindEarly,
			DetectedAt: parseTime("2026-06-01T00:00:00Z"),
		},
		LastWarning: &store.WarningSummary{
			Label:              resetwatch.LabelFiveHour,
			ThresholdRemaining: 5,
			DetectedAt:         parseTime("2026-06-01T00:00:00Z"),
		},
	}
}

func snapshot(resetAt string, used float64) []byte {
	limit := 100.0
	return resetwatch.SnapshotJSON(remote.ProbeResult{Lines: []model.MetricLine{
		{Type: "progress", Label: resetwatch.LabelWeeklyLimit, Used: &used, Limit: &limit, ResetsAt: resetAt},
	}})
}

func parseTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func ptrFloat(value float64) *float64 {
	return &value
}
