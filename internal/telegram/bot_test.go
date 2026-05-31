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
