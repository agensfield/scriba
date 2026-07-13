package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/radar"
	"github.com/agensfield/scriba/internal/remote"
	remotecodex "github.com/agensfield/scriba/internal/remote/codex"
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
		ResetGrants: resetwatch.ResetGrants{
			AvailableCount: ptrInt(1),
			ExpiresAt:      parseTimeNano("2026-07-12T01:20:48.728491Z"),
		},
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
		"<pre>",
		"Weekly",
		"5h",
		"▰▱▱▱▱▱▱▱▱▱",
		"<b>Reset grants</b>",
		"available 1",
		"expires   2026-07-12 01:20 UTC",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Spark") {
		t.Fatalf("render should hide Spark windows:\n%s", text)
	}
	if five, weekly := strings.Index(text, "5h"), strings.Index(text, "Weekly"); five < 0 || weekly < 0 || five > weekly {
		t.Fatalf("expected 5h before weekly in:\n%s", text)
	}
}

func TestRenderProfileShowsCodexProfileStats(t *testing.T) {
	rank := int64(2)
	total := int64(7)
	text := RenderProfile(remotecodex.ProfileResult{
		Profile:  remotecodex.Profile{Username: "ardasevinc", DisplayName: "Arda & Co"},
		Metadata: remotecodex.ProfileMetadata{StatsAsOf: "2026-06-28", GeneratedAt: "2026-06-29T00:01:45Z"},
		AuthState: remote.AuthState{
			OK:    true,
			Email: "arda@example.com",
		},
		Stats: remotecodex.ProfileStats{
			LifetimeTokens:             8318370263,
			PeakDailyTokens:            947935822,
			CurrentStreakDays:          22,
			LongestStreakDays:          22,
			LongestRunningTurnSec:      18784,
			FastModeUsagePercentage:    2.46,
			MostUsedReasoningEffort:    "medium",
			MostUsedReasoningEffortPct: 80.59,
			TotalThreads:               585,
			TotalSkillsUsed:            1001,
			UniqueSkillsUsed:           38,
			WorkspaceRank:              &rank,
			WorkspaceTotalUserCount:    &total,
			DailyUsageBuckets:          []remotecodex.UsageBucket{{StartDate: "2026-06-27", Tokens: 947935822}, {StartDate: "2026-06-28", Tokens: 78511833}},
			WeeklyUsageBuckets:         []remotecodex.UsageBucket{{StartDate: "2026-06-22", Tokens: 1573087214}},
			TopInvocations:             []remotecodex.Invocation{{Type: "skill", SkillName: "agent-browser", UsageCount: 277}},
		},
	})

	for _, want := range []string{
		"<b>Codex profile</b>",
		"<b>Arda &amp; Co</b> <code>@ardasevinc</code>",
		"stats as of 2026-06-28",
		"<b>Overview</b>",
		"tokens        8.3B lifetime",
		"peak day      947.9M",
		"streak        22d now",
		"reasoning     medium · 80.6%",
		"fast mode     2.5%",
		"threads       585",
		"skills        1,001 uses · 38 unique",
		"workspace     #2 of 7",
		"<b>Daily tokens</b>",
		"2026-06-27",
		"<b>Weekly tokens</b>",
		"<b>Top invocations</b>",
		"agent-browser",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q in:\n%s", want, text)
		}
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

func TestRenderGrantExpiryWarningShowsExpiryCheckpoint(t *testing.T) {
	text := RenderGrantExpiryWarning(resetwatch.GrantExpiryWarning{
		Account:       resetwatch.Account{Label: "personal", Email: "arda@example.com", Plan: "prolite"},
		CreditID:      "credit_1234567890",
		CreditTitle:   "Rate limit reset",
		ThresholdDays: 5,
		ExpiresAt:     parseTime("2026-07-12T01:20:48Z"),
		DetectedAt:    parseTime("2026-07-07T02:20:48Z"),
	})
	for _, want := range []string{
		"<b>Codex reset grant expiry</b>",
		"checkpoint 5d",
		"expires    2026-07-12 01:20 UTC",
		"left       5d",
		"grant      Rate limit reset",
		"id         credit_12345",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q in:\n%s", want, text)
		}
	}
}

func TestRenderResetGrantShowsLoadedGrant(t *testing.T) {
	text := RenderResetGrant(resetwatch.ResetGrantEvent{
		Account:        resetwatch.Account{Label: "personal", Email: "arda@example.com", Plan: "prolite"},
		CreditID:       "RateLimitResetCredit_1234567890",
		CreditTitle:    "Full reset (Weekly + 5 hr)",
		ResetType:      "codex_rate_limits",
		GrantedAt:      parseTime("2026-06-18T00:29:25Z"),
		ExpiresAt:      parseTime("2026-07-18T00:29:25Z"),
		AvailableCount: 2,
		DetectedAt:     parseTime("2026-06-18T00:40:25Z"),
	})
	for _, want := range []string{
		"<b>Codex reset grant loaded</b>",
		"Tibo loaded a reset grant.",
		"available  2",
		"grant      Full reset (Weekly + 5 hr)",
		"type       codex_rate_limits",
		"granted    2026-06-18 00:29 UTC",
		"expires    2026-07-18 00:29 UTC",
		"id         RateLimitRes",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q in:\n%s", want, text)
		}
	}
}

func TestRenderRadarProbabilityUsesEnglishSummary(t *testing.T) {
	text := RenderRadarProbability(radar.ProbabilityAlert{
		Milestone:        50,
		Probability24H:   0.64,
		Probability48H:   0.78,
		Level:            "high",
		ExpectedWindow:   "未来 24-48 小时",
		ReasoningSummary: "24小时约64%、48小时约78%，属于高位预警但不是官方确认。",
		CheckedAt:        "2026-06-03T19:00:36+08:00",
		DetectedAt:       parseTime("2026-06-03T12:29:47Z"),
	})
	for _, want := range []string{
		"<b>Codex reset radar alert</b>",
		"checkpoint   50%",
		"24h          64%",
		"48h          78%",
		"window       next 24-48h",
		"prediction signal, not an official reset confirmation",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q in:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"未来", "小时", "属于高位预警"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("render leaked upstream Chinese %q in:\n%s", unwanted, text)
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
		"<b>Outbox</b>",
		"<b>Telegram inbox</b>",
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
		"<b>Grant warning deliveries</b>",
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

func TestEmptyAllowlistIsPrivateChatOnlyAndGroupsRequireUserAllowlist(t *testing.T) {
	private := &models.Update{Message: &models.Message{Chat: models.Chat{ID: 123, Type: models.ChatTypePrivate}, From: &models.User{ID: 7}}}
	group := &models.Update{Message: &models.Message{Chat: models.Chat{ID: 123, Type: models.ChatTypeSupergroup}, From: &models.User{ID: 7}}}
	svc := &Service{cfg: BotConfig{ChatID: 123}}
	if !svc.authorized(private) {
		t.Fatal("empty allowlist should retain private-chat compatibility")
	}
	if svc.authorized(group) {
		t.Fatal("empty allowlist authorized a group user")
	}
	svc.cfg.AllowedUserIDs = []int64{7}
	if !svc.authorized(group) {
		t.Fatal("explicitly allowlisted group user was denied")
	}
	svc.cfg.AllowedUserIDs = []int64{8}
	if svc.authorized(group) {
		t.Fatal("non-allowlisted group user was authorized")
	}
}

func TestVersionedProfileCallbacksSelectExactProfile(t *testing.T) {
	controller := &fakeController{latest: resetwatch.Observation{Account: resetwatch.Account{Label: "Work"}, ObservedAt: time.Now()}, latestOK: true, health: healthFixture()}
	controller.health.Profiles = []server.ProfileHealth{{Profile: server.ProfileIdentity{Ref: "work", Label: "Work"}, Status: server.HealthOK}}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	if err := svc.handleCallback(t.Context(), &models.CallbackQuery{Data: "profiles:v1:limits:work"}); err != nil {
		t.Fatal(err)
	}
	if controller.latestProfile != "work" {
		t.Fatalf("selected profile=%q", controller.latestProfile)
	}
	if _, _, ok := parseProfileCallback("profiles:v2:limits:work"); ok {
		t.Fatal("future callback version accepted")
	}
	for _, malformed := range []string{"profiles:v1:limits:", "profiles:v1:limits:INVALID", "profiles:v1:list:-1", "profiles:v1:list:10000", "profiles:v1:unknown:work", "profiles:v1:limits:work:extra"} {
		if _, _, ok := parseProfileCallback(malformed); ok {
			t.Fatalf("malformed callback accepted: %q", malformed)
		}
	}
}

func TestInaccessibleMessageCallbackRetainsChatAuthorization(t *testing.T) {
	update := &models.Update{CallbackQuery: &models.CallbackQuery{From: models.User{ID: 7}, Message: models.MaybeInaccessibleMessage{Type: models.MaybeInaccessibleMessageTypeInaccessibleMessage, InaccessibleMessage: &models.InaccessibleMessage{Chat: models.Chat{ID: -100, Type: models.ChatTypeSupergroup}, MessageID: 5}}}}
	svc := &Service{cfg: BotConfig{ChatID: -100, AllowedUserIDs: []int64{7}}}
	if !svc.authorized(update) {
		t.Fatal("chat-backed inaccessible callback was denied")
	}
	svc.cfg.AllowedUserIDs = []int64{8}
	if svc.authorized(update) {
		t.Fatal("inaccessible callback bypassed user allowlist")
	}
}

func TestStaleProfileCallbackRemovesProfileActionsWithoutFallback(t *testing.T) {
	controller := &fakeController{health: healthFixture(), latest: resetwatch.Observation{Account: resetwatch.Account{Label: "Default"}}, latestOK: true}
	controller.health.Profiles = []server.ProfileHealth{{Profile: server.ProfileIdentity{Ref: "default", Label: "Default"}, IsDefault: true, Status: server.HealthOK}}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	if err := svc.handleCallback(t.Context(), &models.CallbackQuery{Data: "profiles:v1:limits:removed"}); err != nil {
		t.Fatal(err)
	}
	if controller.latestProfile != "" {
		t.Fatalf("stale callback reached account lookup: %q", controller.latestProfile)
	}
}

func TestCallbackKindUsesClosedLogVocabulary(t *testing.T) {
	if got := callbackKind("PRIVATE:SECRET:VALUE"); got != "unknown" {
		t.Fatalf("unknown callback log kind=%q", got)
	}
	if got := callbackKind("profiles:v1:limits:work"); got != "profiles:v1" {
		t.Fatalf("profile callback log kind=%q", got)
	}
}

func TestProfileKeyboardPaginationIsBoundedAndCallbackSafe(t *testing.T) {
	profiles := make([]server.ProfileHealth, 14)
	for i := range profiles {
		profiles[i] = server.ProfileHealth{Profile: server.ProfileIdentity{Ref: fmt.Sprintf("profile-%d", i), Label: strings.Repeat("🔥", 80)}, IsDefault: i == 0, Status: server.HealthOK}
	}
	text, pages := RenderProfilesPage(profiles, 1)
	keyboard := profilesKeyboard(profiles, 1)
	if pages != 3 || len(text) > 4096 || len(keyboard.InlineKeyboard) > profilesPageSize+2 {
		t.Fatalf("pages=%d text=%d rows=%d", pages, len(text), len(keyboard.InlineKeyboard))
	}
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			if len([]byte(button.CallbackData)) > 64 || len([]rune(button.Text)) > 64 {
				t.Fatalf("unsafe button=%+v", button)
			}
		}
	}
	if !strings.Contains(keyboard.InlineKeyboard[0][0].Text, "profile-6") {
		t.Fatalf("profile id missing from button: %q", keyboard.InlineKeyboard[0][0].Text)
	}
	if text, _ := RenderProfilesPage(profiles, 3); text != "" {
		t.Fatalf("out-of-range page rendered: %q", text)
	}
}

func TestProfileCallbackAnswersAndEditsExistingMessage(t *testing.T) {
	var mu sync.Mutex
	methods := map[string]int{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/editMessageText") {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":5,"date":1,"chat":{"id":123,"type":"private"}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	}))
	t.Cleanup(api.Close)
	bot, err := tgbot.New("test", tgbot.WithServerURL(api.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	controller := &fakeController{health: healthFixture()}
	controller.health.Profiles = []server.ProfileHealth{{Profile: server.ProfileIdentity{Ref: "work", Label: "Work"}, Status: server.HealthOK}}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller, bot: bot, apiTimeout: time.Second}
	query := &models.CallbackQuery{ID: "callback-1", Data: "profiles:v1:list:0", From: models.User{ID: 7}, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 5, Date: 1, Chat: models.Chat{ID: 123, Type: models.ChatTypePrivate}}}}
	if err := svc.handleCallback(t.Context(), query); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if methods["/bottest/answerCallbackQuery"] != 1 || methods["/bottest/editMessageText"] != 1 || methods["/bottest/sendMessage"] != 0 {
		t.Fatalf("methods=%v", methods)
	}
}

func TestHandleSettingsCallbackUpdatesPollInterval(t *testing.T) {
	controller := &fakeController{interval: 5 * time.Minute}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	if err := svc.handleCallback(context.Background(), &models.CallbackQuery{Data: "settings:poll:10m"}); err != nil {
		t.Fatal(err)
	}
	if controller.interval != 10*time.Minute {
		t.Fatalf("interval was not updated: %s", controller.interval)
	}
}

func TestLimitsCommandUsesCachedObservation(t *testing.T) {
	controller := &fakeController{
		latest: resetwatch.Observation{
			Account:    resetwatch.Account{Label: "personal"},
			ObservedAt: parseTime("2026-06-01T00:00:00Z"),
			ResetGrants: resetwatch.ResetGrants{
				AvailableCount: ptrInt(1),
				ExpiresAt:      parseTimeNano("2026-07-12T01:20:48.728491Z"),
			},
			Windows: []resetwatch.Window{
				{Label: resetwatch.LabelWeeklyLimit, UsedPercent: ptrFloat(3), ResetAt: parseTime("2026-06-07T16:39:00Z")},
			},
		},
		latestOK: true,
	}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	reply, markup := svc.handleCommand(context.Background(), "/limits work")
	if !strings.Contains(reply, "<b>Codex limits</b>") {
		t.Fatalf("unexpected limits reply: %s", reply)
	}
	if !strings.Contains(reply, "<b>Reset grants</b>") || !strings.Contains(reply, "available 1") {
		t.Fatalf("limits reply missing reset grants: %s", reply)
	}
	if controller.refreshes != 0 {
		t.Fatalf("/limits should not force refresh, got %d refreshes", controller.refreshes)
	}
	if controller.latestProfile != "work" || !strings.Contains(reply, "Configured profile</b> <code>work</code>") {
		t.Fatalf("profile=%q reply=%s", controller.latestProfile, reply)
	}
	keyboard, ok := markup.(models.InlineKeyboardMarkup)
	if !ok || keyboard.InlineKeyboard[0][0].CallbackData != "profiles:v1:limits:work" {
		t.Fatalf("profile keyboard=%+v", markup)
	}
}

func TestGrantsCommandShowsDetailedCachedCredits(t *testing.T) {
	controller := &fakeController{
		latest: resetwatch.Observation{
			Account:    resetwatch.Account{Label: "personal", Plan: "pro"},
			ObservedAt: parseTime("2026-07-10T20:00:00Z"),
			ResetGrants: resetwatch.ResetGrants{
				AvailableCount: ptrInt(1),
				Credits: []resetwatch.ResetCredit{{
					ID:        "RateLimitResetCredit_1234567890",
					Status:    "available",
					ResetType: "codex_rate_limits",
					Title:     "Full reset (Weekly + 5 hr)",
					GrantedAt: parseTime("2026-07-01T10:00:00Z"),
					ExpiresAt: parseTime("2026-08-01T10:00:00Z"),
				}},
			},
		},
		latestOK: true,
	}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	reply, markup := svc.handleCommand(context.Background(), "/grants personal")
	for _, want := range []string{
		"<b>Codex reset grants</b>",
		"<b>1 available</b>",
		"Full reset (Weekly + 5 hr)",
		"codex_rate_limits",
		"available",
		"2026-07-01 10:00 UTC",
		"2026-08-01 10:00 UTC",
		"RateLimitResetCredit_1234567890",
	} {
		if !strings.Contains(reply, want) {
			t.Fatalf("grants reply missing %q:\n%s", want, reply)
		}
	}
	if markup == nil {
		t.Fatal("expected main keyboard")
	}
	if controller.refreshes != 0 {
		t.Fatalf("/grants should use the latest observation, got %d refreshes", controller.refreshes)
	}
	if controller.latestProfile != "personal" {
		t.Fatalf("profile=%q", controller.latestProfile)
	}
}

func TestProfileCommandUsesControllerProfile(t *testing.T) {
	controller := &fakeController{
		profile: remotecodex.ProfileResult{
			Profile:   remotecodex.Profile{Username: "ardasevinc", DisplayName: "Arda Sevinc"},
			AuthState: remote.AuthState{OK: true},
			Stats: remotecodex.ProfileStats{
				LifetimeTokens:    8318370263,
				CurrentStreakDays: 22,
				LongestStreakDays: 22,
			},
		},
	}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	reply, markup := svc.handleCommand(context.Background(), "/profile work")
	if !strings.Contains(reply, "<b>Codex profile</b>") || !strings.Contains(reply, "8.3B lifetime") {
		t.Fatalf("unexpected profile reply: %s", reply)
	}
	if controller.profileCalls != 1 {
		t.Fatalf("expected one profile call, got %d", controller.profileCalls)
	}
	if controller.codexProfileID != "work" {
		t.Fatalf("profile=%q", controller.codexProfileID)
	}
	if markup == nil {
		t.Fatal("expected main keyboard")
	}
}

func TestProfilesCommandListsSafeBoundedHealthAndProfileUsage(t *testing.T) {
	controller := &fakeController{}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	health := healthFixture()
	health.Profiles = []server.ProfileHealth{
		{Profile: server.ProfileIdentity{Ref: "personal", Label: "Personal"}, IsDefault: true, Status: server.HealthOK},
		{Profile: server.ProfileIdentity{Ref: "work", Label: "Work"}, Status: server.HealthDegraded},
	}
	controller.health = health
	reply, markup := svc.handleCommand(context.Background(), "/profiles")
	for _, want := range []string{"Configured profiles", "personal", "default", "work", "degraded", "Choose a profile"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("missing %q:\n%s", want, reply)
		}
	}
	if markup == nil {
		t.Fatal("expected main keyboard")
	}
	for _, command := range []string{"/limits INVALID", "/grants one two", "/profile " + strings.Repeat("a", 33), "/profiles extra"} {
		reply, _ := svc.handleCommand(context.Background(), command)
		if !strings.HasPrefix(reply, "usage:") {
			t.Fatalf("%q reply=%q", command, reply)
		}
	}
}

func TestRenderProfilesEscapesAndBoundsOutput(t *testing.T) {
	profiles := make([]server.ProfileHealth, 0, maxRenderedProfiles+1)
	for i := 0; i < maxRenderedProfiles+1; i++ {
		profiles = append(profiles, server.ProfileHealth{Profile: server.ProfileIdentity{Ref: fmt.Sprintf("p%d", i), Label: "<private>"}, IsDefault: i == 0, Status: server.HealthOK})
	}
	text := RenderProfiles(profiles)
	if strings.Contains(text, "<private>") || !strings.Contains(text, "&lt;private&gt;") || !strings.Contains(text, fmt.Sprintf("%d more profiles omitted", len(profiles)-maxRenderedProfiles)) || strings.Contains(text, fmt.Sprintf("p%d", maxRenderedProfiles)) {
		t.Fatalf("unexpected bounded profile output:\n%s", text)
	}
	worst := make([]server.ProfileHealth, maxRenderedProfiles+1)
	for i := range worst {
		worst[i] = server.ProfileHealth{Profile: server.ProfileIdentity{Ref: strings.Repeat("a", 31) + fmt.Sprint(i%10), Label: strings.Repeat("&", 128)}, Status: server.HealthDegraded}
	}
	if text := RenderProfiles(worst); len(text) > 4096 {
		t.Fatalf("profile output exceeds Telegram limit: %d", len(text))
	}
}

func TestProfileCommandsBoundPrivateControllerErrorsAndDefaultSelection(t *testing.T) {
	privateErr := errors.New("open /secret/auth.json: bearer PRIVATE_ACCOUNT")
	controller := &fakeController{latestErr: privateErr, profileErr: privateErr, healthErr: privateErr}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	for _, command := range []string{"/limits", "/grants", "/profile", "/profiles"} {
		reply, _ := svc.handleCommand(context.Background(), command)
		if strings.Contains(reply, "/secret/") || strings.Contains(reply, "PRIVATE_ACCOUNT") || !strings.Contains(reply, "failed") {
			t.Fatalf("%q leaked private error: %q", command, reply)
		}
	}
	if controller.latestProfile != "" || controller.codexProfileID != "" {
		t.Fatalf("default selectors latest=%q profile=%q", controller.latestProfile, controller.codexProfileID)
	}

	controller = &fakeController{latestErr: server.ErrProfileUnavailable, profileErr: server.ErrProfileUnavailable}
	svc.controller = controller
	for _, command := range []string{"/limits missing", "/grants missing", "/profile missing"} {
		reply, _ := svc.handleCommand(context.Background(), command)
		if reply != "unknown or disabled profile." {
			t.Fatalf("%q reply=%q", command, reply)
		}
	}
}

func TestStripTelegramHTMLFallback(t *testing.T) {
	text := "<b>Codex limits</b>\n<pre>Weekly &lt;ok&gt;</pre>"
	got := stripTelegramHTML(text)
	if got != "Codex limits\nWeekly <ok>" {
		t.Fatalf("unexpected stripped text: %q", got)
	}
}

func TestUncertainSendErrorSkipsPlainTextFallback(t *testing.T) {
	for _, err := range []error{
		context.DeadlineExceeded,
		errors.New(`error do request for method sendMessage, Post "https://api.telegram.org/bot***/sendMessage": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`),
		errors.New("read tcp 127.0.0.1:123->1.2.3.4:443: i/o timeout"),
	} {
		if !isUncertainSendError(err) {
			t.Fatalf("expected uncertain send error for %v", err)
		}
	}
	if isUncertainSendError(errors.New("Bad Request: can't parse entities")) {
		t.Fatal("formatting errors should still allow plain text fallback")
	}
}

func TestHandleUpdateOnlyAdvancesAfterSuccessfulOrIgnoredUpdate(t *testing.T) {
	offsets := &fakeOffsetStore{}
	svc := &Service{cfg: BotConfig{ChatID: 123, AllowedUserIDs: []int64{7}}, offsets: offsets, botRef: "default", logger: slog.Default()}

	svc.handleUpdate(context.Background(), nil, &models.Update{ID: 10, Message: &models.Message{Chat: models.Chat{ID: 999}, From: &models.User{ID: 7}, Text: "/help"}})
	svc.handleUpdate(context.Background(), nil, &models.Update{ID: 11, Message: &models.Message{Chat: models.Chat{ID: 123}, From: &models.User{ID: 7}}})
	if got := offsets.values; len(got) != 2 || got[0] != 10 || got[1] != 11 {
		t.Fatalf("ignored updates should advance cursor in order, got %v", got)
	}
}

func TestFailedCommandReplyLeavesCursorAndIsBoundedWithoutFallback(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":123,"type":"private"}}}`))
	}))
	defer server.Close()

	b, err := tgbot.New("test", tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	offsets := &fakeOffsetStore{}
	svc := &Service{cfg: BotConfig{ChatID: 123}, offsets: offsets, botRef: "default", bot: b, logger: slog.Default(), apiTimeout: 30 * time.Millisecond}
	started := time.Now()
	svc.handleUpdate(context.Background(), b, &models.Update{ID: 12, Message: &models.Message{Chat: models.Chat{ID: 123, Type: models.ChatTypePrivate}, From: &models.User{ID: 7}, Text: "/help"}})
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("hanging send was not bounded: %s", elapsed)
	}
	if len(offsets.values) != 0 {
		t.Fatalf("failed reply advanced cursor: %v", offsets.values)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("ambiguous timeout retried send, got %d requests", requests)
	}
}

func TestRestartPollsAfterPersistedUpdate(t *testing.T) {
	requestSeen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case requestSeen <- string(body):
		default:
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer server.Close()
	offsets := &fakeOffsetStore{stored: 44, storedOK: true}
	svc, err := newBotService(BotConfig{Token: "test", ChatID: 123}, nil, offsets, nil, radar.Client{}, tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.bot.Start(ctx)
	select {
	case got := <-requestSeen:
		if !strings.Contains(got, "name=\"offset\"\r\n\r\n45\r\n") {
			t.Fatalf("restart request %q does not poll after persisted update 44", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for restart poll")
	}
}

type fakeOffsetStore struct {
	stored   int64
	storedOK bool
	values   []int64
}

func (f *fakeOffsetStore) GetTelegramOffset(context.Context, string) (int64, bool, error) {
	return f.stored, f.storedOK, nil
}

func (f *fakeOffsetStore) SetTelegramOffset(_ context.Context, _ string, offset int64) error {
	f.values = append(f.values, offset)
	return nil
}

func TestCommandNameNormalizesBotSuffix(t *testing.T) {
	if got := commandName("/Status@codexusagebot now"); got != "/status" {
		t.Fatalf("unexpected command: %q", got)
	}
}

type fakeController struct {
	interval       time.Duration
	latest         resetwatch.Observation
	latestOK       bool
	refreshes      int
	profile        remotecodex.ProfileResult
	profileCalls   int
	latestProfile  string
	codexProfileID string
	health         server.Health
	latestErr      error
	profileErr     error
	healthErr      error
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

func (f *fakeController) LatestObservationForProfile(_ context.Context, profile string) (resetwatch.Observation, bool, error) {
	f.latestProfile = profile
	return f.latest, f.latestOK, f.latestErr
}

func (f *fakeController) CodexProfile(context.Context) (remotecodex.ProfileResult, error) {
	f.profileCalls++
	return f.profile, nil
}

func (f *fakeController) CodexProfileForProfile(_ context.Context, profile string) (remotecodex.ProfileResult, error) {
	f.profileCalls++
	f.codexProfileID = profile
	return f.profile, f.profileErr
}

func (f *fakeController) Stats(context.Context) (server.Stats, error) {
	return server.Stats{PollInterval: f.interval, ObservationRetentionDays: 120, Store: storeStatsFixture(), Health: healthFixture(), Version: "0.1.0-alpha.1", Commit: "test"}, nil
}

func (f *fakeController) Health(context.Context) (server.Health, error) {
	if f.healthErr != nil {
		return server.Health{}, f.healthErr
	}
	if f.health.Version != "" || len(f.health.Profiles) > 0 {
		return f.health, nil
	}
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
			"accounts":                       1,
			"limit_observations":             9,
			"observed_windows":               36,
			"limit_windows":                  4,
			"reset_events":                   2,
			"limit_warning_events":           3,
			"reset_grant_warning_events":     1,
			"notification_deliveries":        2,
			"limit_warning_deliveries":       3,
			"reset_grant_warning_deliveries": 1,
		},
		ResetDeliveries: map[string]store.DeliveryCounts{
			"delivered": {Count: 2, Attempts: 2},
		},
		WarningDeliveries: map[string]store.DeliveryCounts{
			"pending":   {Count: 1, Attempts: 0},
			"delivered": {Count: 2, Attempts: 2},
		},
		GrantWarningDeliveries: map[string]store.DeliveryCounts{
			"pending": {Count: 1, Attempts: 0},
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
		LastGrantWarning: &store.GrantWarningSummary{
			CreditID:      "credit_1",
			ThresholdDays: 5,
			ExpiresAt:     parseTime("2026-07-12T01:20:48Z"),
			DetectedAt:    parseTime("2026-07-07T01:20:48Z"),
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

func parseTimeNano(value string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func ptrFloat(value float64) *float64 {
	return &value
}

func ptrInt(value int) *int {
	return &value
}
