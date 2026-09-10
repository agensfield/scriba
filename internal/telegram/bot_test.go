package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/accounts"
	"github.com/agensfield/scriba/internal/budget"
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

func TestRenderActivityShowsCodexStats(t *testing.T) {
	rank := int64(2)
	total := int64(7)
	text := RenderActivity(remotecodex.ProfileResult{
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
		"<b>Codex activity</b>",
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

func TestRenderPacingWarningExplainsRiskAndDedupe(t *testing.T) {
	text := RenderPacingWarning(budget.PacingAlert{AccountLabel: "personal", Label: resetwatch.LabelWeeklyLimit, Risk: "high", Confidence: "low", UsedPercent: 40, RemainingPercentPoints: 60, PacePercentPointsPerHour: 1.65, SafePercentPointsPerHour: 0.42, ProjectedExhaustionAt: parseTime("2026-07-15T08:59:00Z"), ResetAt: parseTime("2026-07-19T20:15:00Z")})
	for _, want := range []string{"<b>Codex pacing warning</b>", "Weekly · spending too fast", "40% used, 60% left", "Current pace 1.65%/h", "sustainable pace 0.42%/h", "before reset", "low confidence estimate", "won’t repeat this warning"} {
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

func TestRenderHealthSeparatesSourceAndHistoricalAccountState(t *testing.T) {
	active := testAccount("active-ref", "active", true)
	historical := testAccount("historical-ref", "historical", false)
	historical.LastSeenAt = time.Time{}
	health := healthFixture()
	health.Sources = []server.SourceHealth{{Source: server.SourceIdentity{Ref: "src-private-hash"}, Status: server.HealthOK}, {Source: server.SourceIdentity{Ref: "src-other-private-hash"}, Status: server.HealthDegraded}}
	health.Accounts = []server.AccountHealth{{Account: active, Status: server.HealthOK}, {Account: historical, Status: server.HealthUnknown}}
	text := RenderHealth(health)
	for _, want := range []string{"Auth sources", "configured", "degraded", "Accounts", "active · ready", "historical · offline", "never observed"} {
		if !strings.Contains(text, want) {
			t.Fatalf("health missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "src-private-hash") || strings.Contains(text, "src-other-private-hash") {
		t.Fatalf("health exposed internal source ref:\n%s", text)
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

func TestVersionedAccountCallbacksSelectExactAccount(t *testing.T) {
	account := testAccount("work", "work", true)
	controller := &fakeController{accounts: []store.Account{account}, latest: resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: "work", Label: "Work"}, ObservedAt: time.Now()}, latestOK: true, health: healthFixture(), activityResult: server.CodexActivityResult{Account: account, Activity: remotecodex.ProfileResult{AuthState: remote.AuthState{OK: true}}}}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	if err := svc.handleCallback(t.Context(), &models.CallbackQuery{Data: "accounts:v1:limits:" + account.ID}); err != nil {
		t.Fatal(err)
	}
	if controller.latestSelector != account.ID {
		t.Fatalf("selected account=%q", controller.latestSelector)
	}
	if err := svc.handleCallback(t.Context(), &models.CallbackQuery{Data: "accounts:v1:activity:" + account.ID}); err != nil {
		t.Fatal(err)
	}
	if controller.activitySelector != account.ID {
		t.Fatalf("selected activity account=%q", controller.activitySelector)
	}
	if _, _, ok := parseAccountCallback("accounts:v2:limits:" + account.ID); ok {
		t.Fatal("future callback version accepted")
	}
	for _, malformed := range []string{"accounts:v1:limits:", "accounts:v1:limits:personal", "accounts:v1:list:-1", "accounts:v1:list:10000", "accounts:v1:unknown:" + account.ID, "accounts:v1:limits:" + account.ID + ":extra"} {
		if _, _, ok := parseAccountCallback(malformed); ok {
			t.Fatalf("malformed callback accepted: %q", malformed)
		}
	}
}

func TestAccountActivityCallbackHonorsChatAndUserAuthorization(t *testing.T) {
	account := testAccount("work", "work", true)
	controller := &fakeController{accounts: []store.Account{account}, activityResult: server.CodexActivityResult{Account: account, Activity: remotecodex.ProfileResult{AuthState: remote.AuthState{OK: true}}}}
	svc := &Service{cfg: BotConfig{ChatID: 123, AllowedUserIDs: []int64{7}}, controller: controller, logger: slog.Default()}
	callback := func(user int64) *models.Update {
		return &models.Update{CallbackQuery: &models.CallbackQuery{Data: "accounts:v1:activity:" + account.ID, From: models.User{ID: user}, Message: models.MaybeInaccessibleMessage{Message: &models.Message{Chat: models.Chat{ID: 123, Type: models.ChatTypePrivate}}}}}
	}
	if err := svc.dispatchUpdate(t.Context(), callback(8)); err != nil {
		t.Fatal(err)
	}
	if controller.profileCalls != 0 {
		t.Fatal("unauthorized callback reached activity controller")
	}
	if err := svc.dispatchUpdate(t.Context(), callback(7)); err != nil {
		t.Fatal(err)
	}
	if controller.profileCalls != 1 || controller.activitySelector != account.ID {
		t.Fatalf("authorized callback calls=%d selector=%q", controller.profileCalls, controller.activitySelector)
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

func TestStaleAccountCallbackRemovesActionsWithoutFallback(t *testing.T) {
	controller := &fakeController{accounts: []store.Account{testAccount("default", "default", true)}, health: healthFixture(), latest: resetwatch.Observation{Account: resetwatch.Account{Label: "Default"}}, latestOK: true}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	if err := svc.handleCallback(t.Context(), &models.CallbackQuery{Data: "accounts:v1:limits:" + store.AccountID("codex", "removed")}); err != nil {
		t.Fatal(err)
	}
	if controller.latestSelector != "" {
		t.Fatalf("stale callback reached account lookup: %q", controller.latestSelector)
	}
}

func TestLegacyInlineControlUsesGenericUnknownHandling(t *testing.T) {
	controller := &fakeController{accounts: []store.Account{testAccount("work", "work", true)}}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	if err := svc.handleCallback(t.Context(), &models.CallbackQuery{Data: "profiles:v1:limits:work"}); err != nil {
		t.Fatal(err)
	}
	if controller.latestSelector != "" {
		t.Fatalf("legacy control reached account lookup: %q", controller.latestSelector)
	}
}

func TestCallbackKindUsesClosedLogVocabulary(t *testing.T) {
	if got := callbackKind("PRIVATE:SECRET:VALUE"); got != "unknown" {
		t.Fatalf("unknown callback log kind=%q", got)
	}
	if got := callbackKind("accounts:v1:limits:" + store.AccountID("codex", "work")); got != "accounts:v1" {
		t.Fatalf("account callback log kind=%q", got)
	}
}

func TestAccountKeyboardPaginationIsBoundedAndCallbackSafe(t *testing.T) {
	accounts := make([]store.Account, 14)
	for i := range accounts {
		accounts[i] = testAccount(fmt.Sprintf("account-%d", i), strings.Repeat("🔥", 80), i%2 == 0)
	}
	text, pages := RenderAccountsPage(accounts, 1)
	keyboard := accountsKeyboard(accounts, 1)
	if pages != 3 || len(text) > 4096 || len(keyboard.InlineKeyboard) > accountsPageSize+2 {
		t.Fatalf("pages=%d text=%d rows=%d", pages, len(text), len(keyboard.InlineKeyboard))
	}
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			if len([]byte(button.CallbackData)) > 64 || len([]rune(button.Text)) > 64 {
				t.Fatalf("unsafe button=%+v", button)
			}
			if strings.Contains(button.CallbackData, "account-") || strings.Contains(button.CallbackData, "example.com") {
				t.Fatalf("callback exposed private account data: %+v", button)
			}
		}
	}
	if !strings.Contains(keyboard.InlineKeyboard[0][0].Text, "🔥") {
		t.Fatalf("account display missing from button: %q", keyboard.InlineKeyboard[0][0].Text)
	}
	if text, _ := RenderAccountsPage(accounts, 3); text != "" {
		t.Fatalf("out-of-range page rendered: %q", text)
	}
}

func TestAccountCallbackAnswersAndEditsExistingMessage(t *testing.T) {
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
	controller := &fakeController{accounts: []store.Account{testAccount("work", "work", true)}, health: healthFixture()}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller, bot: bot, apiTimeout: time.Second}
	query := &models.CallbackQuery{ID: "callback-1", Data: "accounts:v1:list:0", From: models.User{ID: 7}, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 5, Date: 1, Chat: models.Chat{ID: 123, Type: models.ChatTypePrivate}}}}
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
	account := testAccount("work-ref", "work", true)
	controller := &fakeController{
		accounts: []store.Account{account},
		latest: resetwatch.Observation{
			ProviderID: "codex",
			Account:    resetwatch.Account{Ref: "work-ref", Label: "personal"},
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
	if controller.latestSelector != "work" || !strings.Contains(reply, account.ID) {
		t.Fatalf("selector=%q reply=%s", controller.latestSelector, reply)
	}
	keyboard, ok := markup.(models.InlineKeyboardMarkup)
	if !ok || keyboard.InlineKeyboard[0][0].CallbackData != "accounts:v1:limits:"+account.ID {
		t.Fatalf("account keyboard=%+v", markup)
	}
}

func TestGrantsCommandShowsDetailedCachedCredits(t *testing.T) {
	account := testAccount("personal-ref", "personal", false)
	controller := &fakeController{
		accounts: []store.Account{account},
		latest: resetwatch.Observation{
			ProviderID: "codex",
			Account:    resetwatch.Account{Ref: "personal-ref", Label: "personal", Plan: "pro"},
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
		"credentials unavailable; showing stored data",
		"stored observation is stale",
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
	if controller.latestSelector != "personal" {
		t.Fatalf("selector=%q", controller.latestSelector)
	}
}

func TestSelectedAccountViewsNeverBorrowAnotherAccountsObservation(t *testing.T) {
	personal := testAccount("personal-ref", "personal", true)
	work := testAccount("work-ref", "work", false)
	personalObservation := resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: personal.Ref, Label: "Personal"}, ObservedAt: personal.LastSeenAt, Windows: []resetwatch.Window{{Label: resetwatch.LabelWeeklyLimit, UsedPercent: ptrFloat(11), ResetAt: personal.LastSeenAt.Add(24 * time.Hour)}}}
	workObservation := resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: work.Ref, Label: "Work"}, ObservedAt: work.LastSeenAt, Windows: []resetwatch.Window{{Label: resetwatch.LabelWeeklyLimit, UsedPercent: ptrFloat(88), ResetAt: work.LastSeenAt.Add(24 * time.Hour)}}}
	controller := &fakeController{
		accounts: []store.Account{personal, work},
		latestBySelector: map[string]resetwatch.Observation{
			personal.ID: personalObservation,
			work.ID:     workObservation,
		},
	}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	personalReply, _ := svc.handleCommand(t.Context(), "/limits "+personal.ID)
	workReply, _ := svc.handleCommand(t.Context(), "/limits "+work.ID)
	if !strings.Contains(personalReply, "11% used") || strings.Contains(personalReply, "88% used") {
		t.Fatalf("personal limits crossed accounts:\n%s", personalReply)
	}
	if !strings.Contains(workReply, "88% used") || strings.Contains(workReply, "11% used") || !strings.Contains(workReply, "credentials unavailable") {
		t.Fatalf("work limits crossed accounts:\n%s", workReply)
	}
}

func TestResetCommandRequiresBoundConfirmationAndReusesCompletedResult(t *testing.T) {
	used := 99.0
	credit := remote.ResetCredit{ID: "RateLimitResetCredit_oldest", Status: "available", Title: "Full reset", ExpiresAt: "2026-07-18T00:29:25Z"}
	controller := &fakeController{
		resetPlan:   server.CodexResetPlan{Account: testAccount("work-ref", "work", true), Plan: remotecodex.RateLimitResetPlan{ProviderID: "codex", AvailableCount: 2, Credit: credit, WeeklyUsed: &used}},
		resetResult: remotecodex.RateLimitResetResult{ProviderID: "codex", Outcome: remotecodex.ResetOutcomeReset, WindowsReset: 2, Credit: credit},
	}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	reply, markup := svc.handleCommandFor(t.Context(), "/reset work", 123, 7)
	if controller.resetConsumes != 0 || controller.resetSelector != "work" {
		t.Fatalf("preview mutated or selected wrong account: consumes=%d selector=%q", controller.resetConsumes, controller.resetSelector)
	}
	for _, want := range []string{"Confirm Codex limit reset?", "99% used", "RateLimitResetCredit_oldest", "spends one reset grant"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("preview missing %q:\n%s", want, reply)
		}
	}
	keyboard, ok := markup.(models.InlineKeyboardMarkup)
	if !ok || len(keyboard.InlineKeyboard) != 2 {
		t.Fatalf("keyboard=%+v", markup)
	}
	confirm := keyboard.InlineKeyboard[0][0].CallbackData
	cancel := keyboard.InlineKeyboard[1][0].CallbackData
	if len([]byte(confirm)) > 64 || len([]byte(cancel)) > 64 {
		t.Fatalf("callbacks exceed Telegram limit: %q %q", confirm, cancel)
	}
	// The alias/current selector may resolve elsewhere after preview. Confirmation
	// remains pinned to the stable account returned with the original plan.
	controller.resetPlan.Account = testAccount("other-ref", "work", true)
	query := resetTestQuery(confirm, 123, 7)
	if err := svc.handleResetCallback(t.Context(), query); err != nil {
		t.Fatal(err)
	}
	wantAccountID := store.AccountID("codex", "work-ref")
	if controller.resetConsumes != 1 || controller.resetCredit.ID != credit.ID || controller.resetSelector != wantAccountID || !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(controller.resetRequestID) {
		t.Fatalf("consume=%d selector=%q credit=%+v request=%q", controller.resetConsumes, controller.resetSelector, controller.resetCredit, controller.resetRequestID)
	}
	if err := svc.handleResetCallback(t.Context(), query); err != nil {
		t.Fatal(err)
	}
	if controller.resetConsumes != 1 {
		t.Fatalf("completed callback consumed again: %d", controller.resetConsumes)
	}
}

func TestResetConfirmationIsOwnerBoundCancellableAndExpires(t *testing.T) {
	credit := remote.ResetCredit{ID: "credit", Status: "available"}
	controller := &fakeController{resetPlan: server.CodexResetPlan{Account: testAccount("one", "one", true), Plan: remotecodex.RateLimitResetPlan{Credit: credit}}, resetResult: remotecodex.RateLimitResetResult{Outcome: remotecodex.ResetOutcomeReset, Credit: credit}}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	_, markup := svc.handleCommandFor(t.Context(), "/reset", 123, 7)
	keyboard := markup.(models.InlineKeyboardMarkup)
	confirm := keyboard.InlineKeyboard[0][0].CallbackData
	cancel := keyboard.InlineKeyboard[1][0].CallbackData
	if err := svc.handleResetCallback(t.Context(), resetTestQuery(confirm, 123, 8)); err != nil {
		t.Fatal(err)
	}
	if err := svc.handleResetCallback(t.Context(), resetTestQuery(confirm, 999, 7)); err != nil {
		t.Fatal(err)
	}
	if controller.resetConsumes != 0 {
		t.Fatal("foreign callback consumed reset")
	}
	if err := svc.handleResetCallback(t.Context(), resetTestQuery(cancel, 123, 7)); err != nil {
		t.Fatal(err)
	}
	if err := svc.handleResetCallback(t.Context(), resetTestQuery(confirm, 123, 7)); err != nil {
		t.Fatal(err)
	}
	if controller.resetConsumes != 0 {
		t.Fatal("cancelled callback consumed reset")
	}

	_, markup = svc.handleCommandFor(t.Context(), "/reset", 123, 7)
	confirm = markup.(models.InlineKeyboardMarkup).InlineKeyboard[0][0].CallbackData
	_, token, _ := parseResetCallback(confirm)
	svc.pendingResets[token].ExpiresAt = time.Now().Add(-time.Second)
	if err := svc.handleResetCallback(t.Context(), resetTestQuery(confirm, 123, 7)); err != nil {
		t.Fatal(err)
	}
	if controller.resetConsumes != 0 {
		t.Fatal("expired callback consumed reset")
	}
}

func TestResetConfirmationRetryKeepsOriginalIdempotencyKey(t *testing.T) {
	credit := remote.ResetCredit{ID: "credit", Status: "available"}
	controller := &fakeController{resetPlan: server.CodexResetPlan{Account: testAccount("one", "one", true), Plan: remotecodex.RateLimitResetPlan{Credit: credit}}, resetConsumeErr: errors.New("temporary")}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	_, markup := svc.handleCommandFor(t.Context(), "/reset", 123, 7)
	confirm := markup.(models.InlineKeyboardMarkup).InlineKeyboard[0][0].CallbackData
	query := resetTestQuery(confirm, 123, 7)
	if err := svc.handleResetCallback(t.Context(), query); err != nil {
		t.Fatal(err)
	}
	firstRequestID := controller.resetRequestID
	controller.resetConsumeErr = nil
	controller.resetResult = remotecodex.RateLimitResetResult{Outcome: remotecodex.ResetOutcomeAlreadyRedeemed, Credit: credit}
	if err := svc.handleResetCallback(t.Context(), query); err != nil {
		t.Fatal(err)
	}
	if controller.resetConsumes != 2 || controller.resetRequestID != firstRequestID {
		t.Fatalf("consumes=%d first=%q second=%q", controller.resetConsumes, firstRequestID, controller.resetRequestID)
	}
}

func TestResetConfirmationRetiresAccountChangedPreview(t *testing.T) {
	credit := remote.ResetCredit{ID: "credit", Status: "available"}
	controller := &fakeController{
		resetPlan:       server.CodexResetPlan{Account: testAccount("one", "one", true), Plan: remotecodex.RateLimitResetPlan{Credit: credit}},
		resetConsumeErr: &remotecodex.ResetAccountBindingError{Changed: true},
	}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	_, markup := svc.handleCommandFor(t.Context(), "/reset", 123, 7)
	confirm := markup.(models.InlineKeyboardMarkup).InlineKeyboard[0][0].CallbackData
	_, token, _ := parseResetCallback(confirm)
	query := resetTestQuery(confirm, 123, 7)
	if err := svc.handleResetCallback(t.Context(), query); err != nil {
		t.Fatal(err)
	}
	if controller.resetConsumes != 1 || svc.pendingResets[token] != nil {
		t.Fatalf("consumes=%d pending=%v", controller.resetConsumes, svc.pendingResets[token])
	}
	if err := svc.handleResetCallback(t.Context(), query); err != nil {
		t.Fatal(err)
	}
	if controller.resetConsumes != 1 {
		t.Fatalf("retired confirmation consumed again: %d", controller.resetConsumes)
	}
	if text := RenderCodexResetAccountChanged(server.AccountIdentity{ID: store.AccountID("codex", "one"), DisplayName: "one"}); !strings.Contains(text, "account changed") || !strings.Contains(text, "Preview the reset again") {
		t.Fatalf("account-changed message=%q", text)
	}
}

func TestResetCallbacksUseClosedVersionedShape(t *testing.T) {
	valid := "reset:v1:confirm:0123456789abcdef0123"
	if action, token, ok := parseResetCallback(valid); !ok || action != "confirm" || token != "0123456789abcdef0123" || callbackKind(valid) != "reset:v1" {
		t.Fatalf("valid callback rejected: action=%q token=%q ok=%t kind=%q", action, token, ok, callbackKind(valid))
	}
	for _, value := range []string{"reset:v2:confirm:0123456789abcdef0123", "reset:v1:spend:0123456789abcdef0123", "reset:v1:confirm:SHORT", "reset:v1:confirm:0123456789abcdef0123:extra"} {
		if _, _, ok := parseResetCallback(value); ok || callbackKind(value) != "unknown" {
			t.Fatalf("malformed callback accepted: %q", value)
		}
	}
}

func TestResetCommandIsDiscoverableFromHelpAndKeyboards(t *testing.T) {
	if !strings.Contains(helpText(), "/reset [account]") {
		t.Fatalf("help missing reset command:\n%s", helpText())
	}
	main := mainKeyboard()
	accountID := store.AccountID("codex", "work")
	account := accountKeyboard(accountID)
	if !keyboardHasCallback(main, "quick:reset") || !keyboardHasCallback(account, "accounts:v1:reset:"+accountID) {
		t.Fatalf("reset callbacks missing: main=%+v account=%+v", main, account)
	}
}

func TestRegisterCommandsIncludesReset(t *testing.T) {
	var requestBody string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	}))
	defer api.Close()
	bot, err := tgbot.New("test", tgbot.WithServerURL(api.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: BotConfig{ChatID: 123}, bot: bot, apiTimeout: time.Second}
	if err := svc.RegisterCommands(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"command":"reset"`, `"command":"accounts"`, `"command":"activity"`, "preview and confirm"} {
		if !strings.Contains(requestBody, want) {
			t.Fatalf("setMyCommands missing %q: %s", want, requestBody)
		}
	}
}

func keyboardHasCallback(keyboard models.InlineKeyboardMarkup, callback string) bool {
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			if button.CallbackData == callback {
				return true
			}
		}
	}
	return false
}

func resetTestQuery(data string, chatID, userID int64) *models.CallbackQuery {
	return &models.CallbackQuery{ID: "callback", Data: data, From: models.User{ID: userID}, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 5, Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate}}}}
}

func TestActivityCommandUsesControllerActivity(t *testing.T) {
	account := testAccount("switched-ref", "switched", true)
	controller := &fakeController{
		accounts: []store.Account{testAccount("old-ref", "old", true)},
		activityResult: server.CodexActivityResult{
			Account: account,
			Activity: remotecodex.ProfileResult{
				Profile:   remotecodex.Profile{Username: "ardasevinc", DisplayName: "Arda Sevinc"},
				AuthState: remote.AuthState{OK: true},
				Stats: remotecodex.ProfileStats{
					LifetimeTokens:    8318370263,
					CurrentStreakDays: 22,
					LongestStreakDays: 22,
				},
			},
		},
	}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	reply, markup := svc.handleCommand(context.Background(), "/activity")
	if !strings.Contains(reply, "<b>Codex activity</b>") || !strings.Contains(reply, "8.3B lifetime") {
		t.Fatalf("unexpected profile reply: %s", reply)
	}
	if !strings.Contains(reply, "switched") || !strings.Contains(reply, account.ID) || strings.Contains(reply, "old-ref") {
		t.Fatalf("implicit current lost resolved account: %s", reply)
	}
	if controller.profileCalls != 1 {
		t.Fatalf("expected one profile call, got %d", controller.profileCalls)
	}
	if controller.activitySelector != "" {
		t.Fatalf("selector=%q", controller.activitySelector)
	}
	keyboard, ok := markup.(models.InlineKeyboardMarkup)
	if !ok || !keyboardHasCallback(keyboard, "accounts:v1:activity:"+account.ID) {
		t.Fatalf("resolved account keyboard=%+v", markup)
	}
}

func TestAccountsCommandListsActiveAndHistoricalAccounts(t *testing.T) {
	historical := testAccount("work-ref", "work", false)
	historical.LastSeenAt = time.Time{}
	controller := &fakeController{accounts: []store.Account{
		testAccount("personal-ref", "personal", true),
		historical,
	}}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	reply, markup := svc.handleCommand(context.Background(), "/accounts")
	for _, want := range []string{"Codex accounts", "personal", "credentials available", "work", "credentials unavailable", "never observed", "Choose an account"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("missing %q:\n%s", want, reply)
		}
	}
	if markup == nil {
		t.Fatal("expected main keyboard")
	}
	for _, command := range []string{"/limits INVALID", "/grants one two", "/reset one two", "/activity " + strings.Repeat("a", 33), "/accounts extra"} {
		reply, _ := svc.handleCommand(context.Background(), command)
		if !strings.HasPrefix(reply, "usage:") {
			t.Fatalf("%q reply=%q", command, reply)
		}
	}
}

func TestRenderAccountsEscapesAndBoundsPage(t *testing.T) {
	known := make([]store.Account, accountsPageSize+1)
	for i := range known {
		known[i] = testAccount(fmt.Sprintf("account-%d", i), strings.Repeat("<&", 80), i%2 == 0)
	}
	text, pages := RenderAccountsPage(known, 0)
	if pages != 2 || strings.Contains(text, "<&") || !strings.Contains(text, "&lt;&amp;") || len(text) > 4096 {
		t.Fatalf("unexpected bounded account output:\n%s", text)
	}
}

func TestAccountCommandsBoundPrivateControllerErrorsAndDefaultSelection(t *testing.T) {
	privateErr := errors.New("open /secret/auth.json: bearer PRIVATE_ACCOUNT")
	controller := &fakeController{latestErr: privateErr, profileErr: privateErr, healthErr: privateErr, resetPlanErr: privateErr}
	svc := &Service{cfg: BotConfig{ChatID: 123}, controller: controller}
	for _, command := range []string{"/limits", "/grants", "/reset", "/activity", "/accounts"} {
		reply, _ := svc.handleCommand(context.Background(), command)
		if strings.Contains(reply, "/secret/") || strings.Contains(reply, "PRIVATE_ACCOUNT") || !strings.Contains(reply, "failed") {
			t.Fatalf("%q leaked private error: %q", command, reply)
		}
	}
	if controller.latestSelector != "" || controller.activitySelector != "" {
		t.Fatalf("default selectors latest=%q activity=%q", controller.latestSelector, controller.activitySelector)
	}

	controller = &fakeController{latestErr: accounts.ErrAccountNotFound, profileErr: accounts.ErrCredentialsUnavailable, resetPlanErr: accounts.ErrAccountNotFound}
	svc.controller = controller
	for _, command := range []string{"/limits missing", "/grants missing", "/reset missing"} {
		reply, _ := svc.handleCommand(context.Background(), command)
		if !strings.HasPrefix(reply, "unknown account.") {
			t.Fatalf("%q reply=%q", command, reply)
		}
	}
	controller.accounts = []store.Account{testAccount("missing-ref", "missing", false)}
	reply, _ := svc.handleCommand(context.Background(), "/activity missing")
	if !strings.Contains(reply, "no usable credentials") {
		t.Fatalf("activity reply=%q", reply)
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
	interval         time.Duration
	accounts         []store.Account
	latest           resetwatch.Observation
	latestBySelector map[string]resetwatch.Observation
	latestOK         bool
	refreshes        int
	activityResult   server.CodexActivityResult
	profileCalls     int
	latestSelector   string
	activitySelector string
	health           server.Health
	latestErr        error
	profileErr       error
	healthErr        error
	resetPlan        server.CodexResetPlan
	resetResult      remotecodex.RateLimitResetResult
	resetPlanErr     error
	resetConsumeErr  error
	resetSelector    string
	resetCredit      remote.ResetCredit
	resetRequestID   string
	resetConsumes    int
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

func (f *fakeController) Accounts(context.Context) ([]store.Account, error) {
	return append([]store.Account(nil), f.accounts...), f.healthErr
}

func (f *fakeController) LatestObservationForAccount(_ context.Context, selector string) (resetwatch.Observation, bool, error) {
	f.latestSelector = selector
	if observation, ok := f.latestBySelector[selector]; ok {
		return observation, true, f.latestErr
	}
	return f.latest, f.latestOK, f.latestErr
}

func (f *fakeController) CodexActivityForAccount(_ context.Context, selector string) (server.CodexActivityResult, error) {
	f.profileCalls++
	f.activitySelector = selector
	return f.activityResult, f.profileErr
}

func (f *fakeController) PlanCodexReset(_ context.Context, selector string) (server.CodexResetPlan, error) {
	f.resetSelector = selector
	return f.resetPlan, f.resetPlanErr
}

func (f *fakeController) ConsumeCodexReset(_ context.Context, selector string, _ remotecodex.ResetAccountPin, credit remote.ResetCredit, requestID string) (remotecodex.RateLimitResetResult, error) {
	f.resetConsumes++
	f.resetSelector = selector
	f.resetCredit = credit
	f.resetRequestID = requestID
	return f.resetResult, f.resetConsumeErr
}

func (f *fakeController) Stats(context.Context) (server.Stats, error) {
	return server.Stats{PollInterval: f.interval, ObservationRetentionDays: 120, Store: storeStatsFixture(), Health: healthFixture(), Version: "0.1.0-alpha.1", Commit: "test"}, nil
}

func (f *fakeController) Health(context.Context) (server.Health, error) {
	if f.healthErr != nil {
		return server.Health{}, f.healthErr
	}
	if f.health.Version != "" || len(f.health.Accounts) > 0 {
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

func testAccount(ref, alias string, credentials bool) store.Account {
	seen := parseTime("2026-09-10T12:00:00Z")
	return store.Account{
		ID:                   store.AccountID("codex", ref),
		Ref:                  ref,
		ProviderID:           "codex",
		Alias:                alias,
		Email:                alias + "@example.com",
		FirstSeenAt:          seen.Add(-time.Hour),
		LastSeenAt:           seen,
		CredentialsAvailable: credentials,
	}
}
