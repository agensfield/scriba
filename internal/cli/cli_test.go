package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/cache"
	"github.com/agensfield/scriba/internal/config"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
	remotecodex "github.com/agensfield/scriba/internal/remote/codex"
	"github.com/agensfield/scriba/internal/resetwatch"
	servercore "github.com/agensfield/scriba/internal/server"
	"github.com/agensfield/scriba/internal/server/store"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestCodexLimitsFromSnapshotFiltersRemoteLimitLines(t *testing.T) {
	used := 12.0
	limit := 100.0
	grants := 1.0
	snapshot := model.StatusSnapshot{
		SchemaVersion: model.SchemaVersion,
		GeneratedAt:   "2026-05-19T19:30:56Z",
		Providers: []model.ProviderSnapshot{
			{
				ProviderID:  "codex",
				DisplayName: "Codex",
				State:       "ok",
				Lines: []model.MetricLine{
					{Type: "badge", Label: "Plan", Text: "prolite"},
					{Type: "progress", Label: "5h limit", Used: &used, Limit: &limit},
					{Type: "amount", Label: "Reset grants", Value: grants, Format: &model.MetricFormat{Kind: "count", Suffix: "available"}},
					{Type: "text", Label: "Today", Value: "123"},
				},
			},
		},
	}

	payload, err := codexLimitsFromSnapshot(snapshot)
	if err != nil {
		t.Fatalf("codexLimitsFromSnapshot returned error: %v", err)
	}
	if payload.Mode != "fast" {
		t.Fatalf("mode = %q, want fast", payload.Mode)
	}
	if len(payload.Lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(payload.Lines))
	}
	if payload.Lines[0].Label != "Plan" || payload.Lines[1].Label != "5h limit" || payload.Lines[2].Label != "Reset grants" {
		t.Fatalf("unexpected labels: %#v", payload.Lines)
	}
}

func TestServerStatsRendersQueueObservability(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	stats := servercore.Stats{Store: store.Stats{
		Counts:        map[string]int64{},
		Outbox:        store.QueueStats{Pending: 2, DuePending: 1, DeadLetter: 3, Attempts: 5, OldestPendingAt: &old, OldestPendingAge: time.Hour},
		TelegramInbox: store.InboxStats{Pending: 4, Due: 2, Dead: 1, Attempts: 6, OldestPendingAt: &old, OldestPendingAge: time.Hour},
	}}
	text := renderServerStats(stats, "test", true)
	for _, want := range []string{"Outbox", "dead letter   3", "Telegram inbox", "attempts      6", "oldest"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	payload := serverStatsPayload(stats, "test", true)
	if payload["store"].(store.Stats).Outbox.DeadLetter != 3 {
		t.Fatalf("payload: %#v", payload)
	}
}

func TestServerHealthProfilesAreOrderedSafeAndRendered(t *testing.T) {
	now := time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC)
	health := servercore.Health{Status: servercore.HealthDegraded, Profiles: []servercore.ProfileHealth{
		{Profile: servercore.ProfileIdentity{Ref: "personal", Label: "Personal"}, IsDefault: true, Status: servercore.HealthOK, LastAttemptAt: &now, LastSuccessAt: &now},
		{Profile: servercore.ProfileIdentity{Ref: "work", Label: "Work"}, Status: servercore.HealthDegraded, LastAttemptAt: &now, LastFailureAt: &now, FailureKind: "network", LastErrorCode: "timeout", ConsecutiveFailures: 2},
	}}
	payload := healthPayload(health)
	profiles, ok := payload["profiles"].(profilesHealthOutput)
	if !ok || profiles.SchemaVersion != "scriba.profiles.v1" || profiles.DefaultProfileID != "personal" || len(profiles.Profiles) != 2 || profiles.Profiles[0].ProfileID != "personal" || profiles.Profiles[1].LastErrorCode != "timeout" {
		t.Fatalf("profiles=%#v", payload["profiles"])
	}
	raw, err := json.Marshal(payload["profiles"])
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"accountRef", "auth", "token", "source", "/secret/"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("forbidden %q in %s", forbidden, raw)
		}
	}
	var document, instance any
	schemaRaw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "profiles.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(schemaRaw, &document); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	const schemaURL = "https://agensfield.dev/scriba/schemas/profiles.schema.json"
	if err := compiler.AddResource(schemaURL, document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("generated profile payload does not match public schema: %v\n%s", err, raw)
	}
	text := renderServerHealth(health)
	for _, want := range []string{"Profiles", "personal *", "Personal · ok", "work", "network/timeout"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	longDefault := renderServerProfiles(profilesHealthOutput{Profiles: []profileHealthOutput{{ProfileID: "long-default-profile", Label: "Long", IsDefault: true}}})
	if !strings.Contains(longDefault, "*") {
		t.Fatalf("long default marker was truncated:\n%s", longDefault)
	}
}

func TestTelegramDeliveryTarget(t *testing.T) {
	disabledID, disabledTarget, err := telegramDeliveryTarget(config.Config{})
	if err != nil || disabledID != 0 || disabledTarget != "" {
		t.Fatalf("disabled: id=%d target=%q err=%v", disabledID, disabledTarget, err)
	}
	cfg := config.Config{}
	cfg.Telegram.Enabled = true
	cfg.Telegram.ChatID = "123"
	chatID, target, err := telegramDeliveryTarget(cfg)
	if err != nil || chatID != 123 || target != "telegram:123" {
		t.Fatalf("enabled: id=%d target=%q err=%v", chatID, target, err)
	}
	cfg.Telegram.ChatID = "bad"
	if _, _, err = telegramDeliveryTarget(cfg); err == nil {
		t.Fatal("expected invalid chat id error")
	}
}

func TestDeliveryRuntimeUsesStableTargetsAndEnvironmentSecrets(t *testing.T) {
	t.Setenv("SCRIBA_WEBHOOK_TEST_SECRET", "webhook-secret")
	t.Setenv("SCRIBA_NTFY_TEST_TOKEN", "ntfy-token")
	cfg := config.Default()
	cfg.Telegram.Enabled = true
	cfg.Telegram.ChatID = "123"
	cfg.Deliveries = config.DeliveryConfig{
		Webhooks: []config.WebhookConfig{{ID: "deploy", Enabled: true, URL: "https://example.com/hook", SecretEnv: "SCRIBA_WEBHOOK_TEST_SECRET"}},
		Ntfy:     []config.NtfyConfig{{ID: "phone", Enabled: true, URL: "https://ntfy.sh", Topic: "scriba", TokenEnv: "SCRIBA_NTFY_TEST_TOKEN"}},
	}
	chatID, targets, adapters, err := deliveryRuntime(cfg)
	if err != nil || chatID != 123 || !reflect.DeepEqual(targets, []string{"telegram:123", "webhook:deploy", "ntfy:phone"}) || len(adapters) != 2 || adapters[0].Target() != "webhook:deploy" || adapters[1].Target() != "ntfy:phone" {
		t.Fatalf("chat=%d targets=%v adapters=%v err=%v", chatID, targets, adapters, err)
	}

	t.Setenv("SCRIBA_WEBHOOK_TEST_SECRET", "")
	if _, _, _, err := deliveryRuntime(cfg); err == nil || strings.Contains(err.Error(), "webhook-secret") {
		t.Fatalf("missing or unsafe webhook secret error: %v", err)
	}
}

func TestCodexLimitsFromSnapshotRequiresCodexProvider(t *testing.T) {
	_, err := codexLimitsFromSnapshot(model.StatusSnapshot{
		SchemaVersion: model.SchemaVersion,
		Providers: []model.ProviderSnapshot{
			{ProviderID: "claude", DisplayName: "Claude"},
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRenderResetGrantsShowsEachCreditExpiry(t *testing.T) {
	payload := codexLimitsPayload{
		SchemaVersion: model.SchemaVersion,
		ProviderID:    "codex",
		Source:        "chatgpt-codex-backend",
		Mode:          "live",
		Lines: []model.MetricLine{
			{Type: "amount", Label: "Reset grants", Value: 2.0, Format: &model.MetricFormat{Kind: "count", Suffix: "available"}},
			{Type: "text", Label: "Grant expiry", Value: "2026-07-12T01:20:48.728491Z"},
		},
		ResetCredits: []remote.ResetCredit{
			{
				ID:        "credit_1",
				Status:    "available",
				ResetType: "codex_rate_limits",
				Title:     "One free rate limit reset",
				GrantedAt: "2026-06-12T01:20:48.728491Z",
				ExpiresAt: "2026-07-12T01:20:48.728491Z",
			},
		},
	}

	now := time.Date(2026, 6, 29, 1, 20, 0, 0, time.UTC)
	text := stripANSI(renderResetGrantsAt(payload, now))
	expiry := time.Date(2026, 7, 12, 1, 20, 48, 728491000, time.UTC).Local().Format("2006-01-02 15:04 MST")
	granted := time.Date(2026, 6, 12, 1, 20, 48, 728491000, time.UTC).Local().Format("2006-01-02 15:04 MST")
	for _, want := range []string{
		"Codex reset grants",
		"2 available · earliest expires " + expiry + " (in 13d)",
		"1. One free rate limit reset",
		"expires  " + expiry + " (in 13d)",
		"granted  " + granted,
		"status   available",
		"credit_1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("renderResetGrants() missing %q in:\n%s", want, text)
		}
	}
}

func stripANSI(text string) string {
	return regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(text, "")
}

func TestCodexGroupHelpListsResetGrants(t *testing.T) {
	text := groupHelp("codex")
	if !strings.Contains(text, "scriba codex reset-grants") {
		t.Fatalf("codex help missing reset-grants command:\n%s", text)
	}
	if !strings.Contains(text, "scriba codex profile") {
		t.Fatalf("codex help missing profile command:\n%s", text)
	}
	if !strings.Contains(text, "scriba codex reset --dry-run") {
		t.Fatalf("codex help missing reset command:\n%s", text)
	}
}

func TestCodexResetFlagsParseSafely(t *testing.T) {
	opts, rest, err := parse([]string{"--credit", "credit-1", "--dry-run", "--json"}, flagSpec{
		Use: "scriba codex reset [flags]", Flags: []string{"json", "credit", "dry-run", "yes"},
	})
	if err != nil || len(rest) != 0 || opts.credit != "credit-1" || !opts.dryRun || !opts.jsonOut || opts.yes {
		t.Fatalf("opts=%+v rest=%v err=%v", opts, rest, err)
	}
}

func TestConfirmCodexResetDefaultsNoAndRequiresExplicitYes(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  bool
	}{{"yes\n", true}, {"Y\n", true}, {"\n", false}, {"no\n", false}, {"", false}} {
		var prompt strings.Builder
		got, err := confirmCodexReset(strings.NewReader(tc.input), &prompt)
		if err != nil || got != tc.want || prompt.String() != "Redeem this reset credit now? [y/N] " {
			t.Fatalf("input=%q got=%t want=%t prompt=%q err=%v", tc.input, got, tc.want, prompt.String(), err)
		}
	}
}

func TestRenderCodexResetMakesDryRunAndSelectedCreditExplicit(t *testing.T) {
	used := 99.0
	text := stripANSI(renderCodexReset(codexResetPayload{
		SchemaVersion: model.SchemaVersion, ProviderID: "codex", Source: "chatgpt-codex-backend",
		DryRun: true, Outcome: "planned", AvailableBefore: 2, WeeklyUsedBefore: &used,
		Credit: remote.ResetCredit{ID: "credit-oldest", Title: "Full reset", ExpiresAt: "2026-07-18T00:29:25Z"},
	}))
	for _, want := range []string{"Codex reset", "dry run · no credit redeemed", "99% used", "2 before reset", "Full reset", "credit-oldest"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestNewResetRequestIDIsUUID(t *testing.T) {
	id, err := remotecodex.NewRateLimitResetRequestID()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("id=%q", id)
	}
}

func TestServerGroupHelpListsBackup(t *testing.T) {
	text := groupHelp("server")
	if !strings.Contains(text, "scriba server backup") || !strings.Contains(text, "--retention 14") {
		t.Fatalf("server help missing backup contract:\n%s", text)
	}
}

func TestSuperviseReturnsUnexpectedExitAndJoinsSibling(t *testing.T) {
	joined := make(chan struct{})
	err := supervise(context.Background(), func(context.Context) error { return nil }, func(ctx context.Context) error {
		<-ctx.Done()
		close(joined)
		return ctx.Err()
	})
	if err == nil || !strings.Contains(err.Error(), "exited unexpectedly") {
		t.Fatalf("unexpected error: %v", err)
	}
	select {
	case <-joined:
	default:
		t.Fatal("supervisor returned before sibling joined")
	}
}

func TestSuperviseBoundsStuckSiblingShutdown(t *testing.T) {
	err := superviseWithTimeout(context.Background(), 20*time.Millisecond,
		func(context.Context) error { return errors.New("failed") },
		func(context.Context) error { select {} },
	)
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSuperviseBoundsStuckChildrenAfterParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := superviseWithTimeout(ctx, 20*time.Millisecond, func(context.Context) error { select {} })
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("supervisor took %s", elapsed)
	}
}

func TestContextAPISocketPathDefaultsBesideState(t *testing.T) {
	state := filepath.Join(t.TempDir(), "nested", "server.sqlite")
	want := filepath.Join(filepath.Dir(state), "context.sock")
	if got := resolveContextAPISocketPath(state, ""); got != want {
		t.Fatalf("socket path = %q, want %q", got, want)
	}
	if got := resolveContextAPISocketPath(state, "/custom/context.sock"); got != "/custom/context.sock" {
		t.Fatalf("socket override = %q", got)
	}
}

func TestContextAPISocketPathMakesRelativeStateAbsolute(t *testing.T) {
	got := resolveContextAPISocketPath(filepath.Join("relative", "server.sqlite"), "")
	if !filepath.IsAbs(got) {
		t.Fatalf("default socket path is relative: %q", got)
	}
}

func TestMCPCommandDiscoveryAndFlags(t *testing.T) {
	if !contains(commands()["root"], "mcp") {
		t.Fatal("root command discovery missing mcp")
	}
	if err := dispatch([]string{"mcp", "--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("mcp --help error = %v", err)
	}
	if err := dispatch([]string{"mcp", "extra"}); err == nil || !strings.Contains(err.Error(), "positional") {
		t.Fatalf("mcp positional error = %v", err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestRenderCodexProfileShowsHumanStats(t *testing.T) {
	rank := int64(2)
	total := int64(7)
	text := stripANSI(renderCodexProfile(remotecodex.ProfileResult{
		Profile:  remotecodex.Profile{Username: "ardasevinc", DisplayName: "Arda Sevinc"},
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
	}))

	for _, want := range []string{
		"Codex profile",
		"Arda Sevinc @ardasevinc",
		"stats as of 2026-06-28",
		"tokens        8.3B lifetime",
		"peak day      947.9M",
		"streak        22d current · 22d best",
		"reasoning     medium · 80.6%",
		"fast mode     2.5%",
		"threads       585",
		"skills        1,001 uses · 38 unique",
		"workspace     #2 of 7",
		"Daily activity",
		"2026-06-27",
		"Weekly activity",
		"Top invocations",
		"agent-browser",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("profile render missing %q:\n%s", want, text)
		}
	}
}

func TestRenderCacheStatusIsHumanReadable(t *testing.T) {
	text := stripANSI(renderCacheStatus(cache.Status{
		CacheDir:      "/tmp/scriba",
		DatabasePath:  "/tmp/scriba/scriba.sqlite",
		SchemaVersion: 1,
		SizeBytes:     1536,
		WAL:           cache.WALInfo{Enabled: true, Mode: "wal", BusyTimeoutMs: 5000},
		ScanStats: []cache.ScanStatsInfo{{
			ProviderID: "codex",
			UpdatedAt:  "2026-06-29T01:20:00Z",
			Stats:      model.ScannerStats{Files: 2, Events: 42},
		}},
	}))

	for _, want := range []string{"Scriba cache", "size", "1.5 KB", "Scans", "codex", "42 events"} {
		if !strings.Contains(text, want) {
			t.Fatalf("cache status missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "{") {
		t.Fatalf("cache status looks like JSON:\n%s", text)
	}
}

func TestRenderServerRefreshAvoidsTelegramMarkup(t *testing.T) {
	used := 15.0
	expires := time.Date(2026, 7, 12, 1, 20, 0, 0, time.UTC)
	count := 1
	text := stripANSI(renderServerRefresh(servercore.PollResult{
		Profile: servercore.ProfileIdentity{Ref: "work", Label: "Work"},
		Observation: resetwatch.Observation{
			Account:    resetwatch.Account{Email: "arda@example.com", Plan: "pro"},
			ObservedAt: time.Date(2026, 6, 29, 1, 20, 0, 0, time.UTC),
			Windows: []resetwatch.Window{{
				Label:       resetwatch.LabelWeeklyLimit,
				UsedPercent: &used,
				ResetAt:     time.Date(2026, 7, 5, 17, 45, 0, 0, time.UTC),
			}},
			ResetGrants: resetwatch.ResetGrants{
				AvailableCount: &count,
				ExpiresAt:      expires,
			},
		},
	}))
	localExpiry := expires.Local().Format("2006-01-02 15:04 MST")

	for _, want := range []string{"Codex limits", "Profile", "work · Work", "Weekly", "15% used", "Reset grants", localExpiry} {
		if !strings.Contains(text, want) {
			t.Fatalf("server refresh missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "<b>") || strings.Contains(text, "<pre>") {
		t.Fatalf("server refresh leaked Telegram markup:\n%s", text)
	}
}
