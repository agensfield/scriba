package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/cache"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestContextMissingSourcesUsesFixedClockAndClosedReasons(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	got, err := New(Config{CacheDir: t.TempDir() + "/missing", StorePath: t.TempDir() + "/missing.sqlite", Clock: func() time.Time { return now }}).Context(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.GeneratedAt.Equal(now) || got.SchemaVersion != SchemaVersion || got.Providers == nil || got.Events == nil {
		t.Fatalf("invalid partial envelope: %#v", got)
	}
	if len(got.Sources) != 8 {
		t.Fatalf("sources = %d, want 8", len(got.Sources))
	}
	allowed := map[string]bool{"": true, "missing": true, "read_error": true, "history_unavailable": true, "fresh": true, "stale": true}
	for _, source := range got.Sources {
		if !allowed[source.ReasonCode] {
			t.Fatalf("open-ended reason code %q", source.ReasonCode)
		}
	}
}

func TestContextSurfacesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(Config{}).Context(ctx); err != context.Canceled {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestLegacyServiceRejectsArbitraryExplicitProfile(t *testing.T) {
	svc := New(Config{ProfileID: "legacy"})
	if _, err := svc.ContextForProfile(t.Context(), "other"); err == nil {
		t.Fatal("legacy service accepted arbitrary profile")
	}
	if _, err := svc.ContextForProfile(t.Context(), "legacy"); err != nil {
		t.Fatalf("legacy profile rejected: %v", err)
	}
}

func TestContextProfileSelectionUsesMappedAccountAndRejectsUnknown(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SyncProfiles(ctx, []store.ProfileSpec{
		{ProfileRef: "personal", ProviderID: "codex", Label: "Personal", Enabled: true, IsDefault: true},
		{ProfileRef: "work", ProviderID: "codex", Label: "Work", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 13, 2, 0, 0, 0, time.UTC)
	for i, profile := range []string{"personal", "work"} {
		used := float64(20 + 60*i)
		obs := resetwatch.Observation{
			ProviderID: "codex", Account: resetwatch.Account{Ref: "acct-" + profile, Label: profile}, ObservedAt: base.Add(time.Duration(i) * time.Hour),
			SnapshotJSON: []byte(`{}`),
			Windows:      []resetwatch.Window{{Label: resetwatch.LabelWeeklyLimit, UsedPercent: &used, ResetAt: base.Add(7 * 24 * time.Hour)}},
		}
		if _, err := st.ApplyCodexPoll(ctx, store.CodexPollInput{ProfileRef: profile, Observation: obs, ResetOptions: resetwatch.DefaultOptions(), CommittedAt: obs.ObservedAt.Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
		used = float64(81 + 10*i)
		obs.ObservedAt = base.Add(2*time.Hour + time.Duration(i)*time.Hour)
		obs.Windows[0].UsedPercent = &used
		if _, err := st.ApplyCodexPoll(ctx, store.CodexPollInput{ProfileRef: profile, Observation: obs, ResetOptions: resetwatch.DefaultOptions(), CommittedAt: obs.ObservedAt.Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(t.TempDir(), "cache")
	c, err := cache.Open(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	cachedUsed := 99.0
	cachedAt := base.Add(10 * time.Hour)
	if err := c.SaveSnapshot("status", model.StatusSnapshot{SchemaVersion: model.SchemaVersion, GeneratedAt: cachedAt.Format(time.RFC3339), Providers: []model.ProviderSnapshot{{ProviderID: "codex", Lines: []model.MetricLine{{Type: "progress", Label: resetwatch.LabelWeeklyLimit, Used: &cachedUsed, ResetsAt: base.Add(7 * 24 * time.Hour).Format(time.RFC3339)}}}}}, cachedAt.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	svc := New(Config{CacheDir: cacheDir, StorePath: path, DefaultProfileID: "personal", ProfileIDs: []string{"personal", "work"}, Clock: func() time.Time { return base.Add(2 * time.Hour) }})
	personal, err := svc.Context(ctx)
	if err != nil {
		t.Fatal(err)
	}
	work, err := svc.ContextForProfile(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}
	if got := *providerByID(t, personal, "codex").Profiles[0].Windows[0].UsedPercent; got != 81 {
		t.Fatalf("personal used=%v", got)
	}
	workProfile := providerByID(t, work, "codex").Profiles[0]
	if workProfile.ProfileID != "work" || *workProfile.Windows[0].UsedPercent != 91 {
		t.Fatalf("work profile=%+v", workProfile)
	}
	for _, profile := range []string{"personal", "work"} {
		page, err := svc.Events(ctx, EventPageRequest{Mode: "latest", Limit: 20, ProfileID: profile})
		if err != nil || len(page.Events) == 0 {
			t.Fatalf("%s events=%+v err=%v", profile, page, err)
		}
		for _, event := range page.Events {
			if event.ProfileID != profile {
				t.Fatalf("%s received cross-profile event %+v", profile, event)
			}
		}
	}
	if _, err := svc.ContextForProfile(ctx, "unknown"); err == nil {
		t.Fatal("unknown profile accepted")
	} else {
		var profileErr *ProfileError
		if !errors.As(err, &profileErr) || profileErr.ReasonCode != "profile_unavailable" {
			t.Fatalf("unknown err=%v", err)
		}
	}
}

func TestEnvelopeTypesCannotSerializePrivateIdentifiers(t *testing.T) {
	payload, err := json.Marshal(Context{SchemaVersion: SchemaVersion, GeneratedAt: time.Now(), Sources: []Source{}, Providers: []Provider{}, Events: []Event{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"accountRef", "snapshot", "grantId", "ruleId", "configHash", "path"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("private field %q in %s", forbidden, payload)
		}
	}
}

func TestEventLimitDefaultsAndCaps(t *testing.T) {
	for _, tc := range []struct{ in, want int }{{0, 20}, {-1, 20}, {1, 1}, {101, 100}} {
		limit := tc.in
		if limit <= 0 {
			limit = defaultEventLimit
		}
		if limit > 100 {
			limit = 100
		}
		if limit != tc.want {
			t.Fatalf("limit %d = %d, want %d", tc.in, limit, tc.want)
		}
	}
}

func TestContextBuildsClaudeFromRealCacheDeterministically(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 7, 12, 11, 55, 0, 0, time.UTC)
	now := at.Add(5 * time.Minute)
	used := float64(20)
	c, err := cache.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := model.StatusSnapshot{SchemaVersion: model.SchemaVersion, GeneratedAt: at.Format(time.RFC3339), Providers: []model.ProviderSnapshot{{ProviderID: "claude", Lines: []model.MetricLine{{Type: "progress", Label: resetwatch.LabelWeeklyLimit, Used: &used, ResetsAt: at.Add(7 * 24 * time.Hour).Format(time.RFC3339)}}}}}
	if err := c.SaveSnapshot("status", snapshot, at.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	svc := New(Config{CacheDir: dir, StorePath: filepath.Join(t.TempDir(), "missing.db"), Clock: func() time.Time { return now }})
	a, err := svc.Context(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Context(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	if !reflect.DeepEqual(aj, bj) {
		t.Fatalf("nondeterministic output\n%s\n%s", aj, bj)
	}
	if len(a.Providers) != 1 || a.Providers[0].ProviderID != "claude" || len(a.Providers[0].Profiles[0].Windows) != 1 {
		t.Fatalf("unexpected providers: %#v", a.Providers)
	}
	if a.Providers[0].Profiles[0].Budgets[0].Reasons[0] != "history_unavailable" {
		t.Fatalf("claude history was not unavailable: %#v", a.Providers[0].Profiles[0].Budgets)
	}
}

func TestTokenOnlyCacheIsNotQuota(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 7, 12, 11, 55, 0, 0, time.UTC)
	c, err := cache.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := model.StatusSnapshot{SchemaVersion: model.SchemaVersion, GeneratedAt: at.Format(time.RFC3339), Providers: []model.ProviderSnapshot{{ProviderID: "codex", Lines: []model.MetricLine{{Type: "amount", Label: "tokens"}}}}}
	if err := c.SaveSnapshot("status", s, at.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	got, err := New(Config{CacheDir: dir, StorePath: filepath.Join(t.TempDir(), "missing.db"), Clock: func() time.Time { return at }}).Context(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Providers) != 0 {
		t.Fatalf("token-only status became quota: %#v", got.Providers)
	}
}

func TestCacheObservationStateUsesProviderProvenance(t *testing.T) {
	fetched := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	fallback := fetched.Add(2 * time.Hour)
	at, stale := cacheObservationState(model.ProviderSnapshot{
		ProviderID: "codex",
		Provenance: []model.SourceProvenance{
			{Kind: "provider-api", ProviderID: "codex", FetchedAt: fetched.Format(time.RFC3339Nano)},
			{Kind: "provider-api", ProviderID: "codex", FetchedAt: fallback.Format(time.RFC3339Nano), Error: "failed newer attempt"},
			{Kind: "cache", ProviderID: "codex", Stale: true},
		},
		Lines: []model.MetricLine{{Type: "progress", Label: resetwatch.LabelFiveHour}},
	}, fallback, false)
	if !at.Equal(fetched) || !stale {
		t.Fatalf("at=%s stale=%t", at, stale)
	}
}

func TestCacheObservationStateIgnoresAggregateProviderDegradation(t *testing.T) {
	fetched := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	at, stale := cacheObservationState(model.ProviderSnapshot{
		ProviderID: "codex",
		State:      "degraded",
		Provenance: []model.SourceProvenance{{Kind: "provider-api", ProviderID: "codex", FetchedAt: fetched.Format(time.RFC3339Nano)}},
		Lines:      []model.MetricLine{{Type: "progress", Label: resetwatch.LabelFiveHour}},
	}, fetched.Add(time.Hour), false)
	if !at.Equal(fetched) || stale {
		t.Fatalf("at=%s stale=%t", at, stale)
	}
}

func TestRealCacheStorePrecedenceIsolationPrivacyAndSchema(t *testing.T) {
	base := time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)
	t.Run("cache newer", func(t *testing.T) {
		cacheDir, storePath := seedCacheStore(t, base, base.Add(3*time.Hour), base)
		got, err := New(Config{CacheDir: cacheDir, StorePath: storePath, Clock: func() time.Time { return base.Add(4 * time.Hour) }}).Context(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Events) != 0 {
			t.Fatalf("cache winner inherited store events: %#v", got.Events)
		}
		p := providerByID(t, got, "codex")
		if used := *p.Profiles[0].Windows[0].UsedPercent; used != 33 {
			t.Fatalf("cache did not win: used=%v", used)
		}
		assertSchemaAndPrivacy(t, got)
	})
	t.Run("store newer and account isolated", func(t *testing.T) {
		cacheDir, storePath := seedCacheStore(t, base, base, base.Add(3*time.Hour))
		svc := New(Config{CacheDir: cacheDir, StorePath: storePath, Clock: func() time.Time { return base.Add(4 * time.Hour) }})
		got, err := svc.Context(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		p := providerByID(t, got, "codex")
		if used := *p.Profiles[0].Windows[0].UsedPercent; used != 81 {
			t.Fatalf("store did not win: used=%v", used)
		}
		if len(got.Events) != 1 || strings.Contains(got.Events[0].ID, "PRIVATE_OTHER") {
			t.Fatalf("cross-account event leak: %#v", got.Events)
		}
		a, _ := json.Marshal(got)
		again, err := svc.Context(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(again)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("repeated output differs\n%s\n%s", a, b)
		}
		assertSchemaAndPrivacy(t, got)
	})
	t.Run("readable empty policy events are available", func(t *testing.T) {
		cacheDir, storePath := seedCacheStoreWithoutTransition(t, base, base.Add(3*time.Hour))
		got, err := New(Config{CacheDir: cacheDir, StorePath: storePath, Clock: func() time.Time { return base.Add(4 * time.Hour) }}).Context(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		events := sourceByID(t, got, "codex-policy-events")
		if events.Availability != "available" || events.ReasonCode != "" || len(got.Events) != 0 {
			t.Fatalf("event source=%+v events=%+v", events, got.Events)
		}
	})
}

func seedCacheStoreWithoutTransition(t *testing.T, base, storeAt time.Time) (string, string) {
	t.Helper()
	cacheDir := filepath.Join(t.TempDir(), "missing-cache")
	storePath := filepath.Join(t.TempDir(), "scriba.db")
	st, err := store.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	o := pollObservation("account", storeAt, 70)
	if _, err := st.ApplyCodexPoll(context.Background(), store.CodexPollInput{Observation: o, NotificationTarget: "telegram:1", ResetOptions: resetwatch.DefaultOptions(), CommittedAt: o.ObservedAt.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return cacheDir, storePath
}

func seedCacheStore(t *testing.T, base, cacheAt, selectedAt time.Time) (string, string) {
	t.Helper()
	cacheDir := t.TempDir()
	usedCache := float64(33)
	c, err := cache.Open(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	snap := model.StatusSnapshot{SchemaVersion: model.SchemaVersion, GeneratedAt: cacheAt.Format(time.RFC3339), Providers: []model.ProviderSnapshot{{ProviderID: "codex", Lines: []model.MetricLine{{Type: "progress", Label: resetwatch.LabelFiveHour, Used: &usedCache, ResetsAt: cacheAt.Add(5 * time.Hour).Format(time.RFC3339)}}}}}
	if err := c.SaveSnapshot("status", snap, cacheAt.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "scriba.db")
	st, err := store.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	seedAccount := func(ref string, at time.Time) {
		for i, used := range []float64{70, 81} {
			o := pollObservation(ref, at.Add(time.Duration(i)*time.Minute), used)
			if _, err := st.ApplyCodexPoll(context.Background(), store.CodexPollInput{Observation: o, NotificationTarget: "PRIVATE_TARGET", ResetOptions: resetwatch.DefaultOptions(), CommittedAt: o.ObservedAt.Add(time.Second)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	seedAccount("PRIVATE_OTHER_ACCOUNT", selectedAt.Add(-2*time.Hour))
	seedAccount("PRIVATE_SELECTED_ACCOUNT", selectedAt)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return cacheDir, storePath
}

func pollObservation(ref string, at time.Time, used float64) resetwatch.Observation {
	period := int64((5 * time.Hour) / time.Millisecond)
	return resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: ref, Label: "PRIVATE_LABEL", Email: "PRIVATE_EMAIL", Plan: "PRIVATE_PLAN"}, ObservedAt: at, SnapshotJSON: []byte(`{"snapshot":"PRIVATE_SNAPSHOT","grantId":"PRIVATE_GRANT","ruleId":"PRIVATE_RULE","configHash":"PRIVATE_CONFIG"}`), Windows: []resetwatch.Window{{Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: at.Add(5 * time.Hour), PeriodDurationMs: &period}}}
}
func providerByID(t *testing.T, c Context, id string) Provider {
	t.Helper()
	for _, p := range c.Providers {
		if p.ProviderID == id {
			return p
		}
	}
	t.Fatalf("provider %s missing", id)
	return Provider{}
}
func sourceByID(t *testing.T, c Context, id string) Source {
	t.Helper()
	for _, source := range c.Sources {
		if source.SourceID == id {
			return source
		}
	}
	t.Fatalf("source %s missing", id)
	return Source{}
}
func assertSchemaAndPrivacy(t *testing.T, c Context) {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"PRIVATE_OTHER_ACCOUNT", "PRIVATE_SELECTED_ACCOUNT", "PRIVATE_LABEL", "PRIVATE_EMAIL", "PRIVATE_PLAN", "PRIVATE_SNAPSHOT", "PRIVATE_GRANT", "PRIVATE_RULE", "PRIVATE_CONFIG", "PRIVATE_TARGET"} {
		if strings.Contains(string(data), s) {
			t.Fatalf("forbidden sentinel %q in %s", s, data)
		}
	}
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{"event", "context"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "schemas", name+".schema.json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource("https://agensfield.dev/scriba/schemas/"+name+".schema.json", doc); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := compiler.Compile("https://agensfield.dev/scriba/schemas/context.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(payload); err != nil {
		t.Fatalf("context schema: %v\n%s", err, data)
	}
}
