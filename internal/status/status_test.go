package status

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/config"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
	remotecodex "github.com/agensfield/scriba/internal/remote/codex"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
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

func TestBuildCodexAccountRoutesConfiguredSource(t *testing.T) {
	dir := t.TempDir()
	authA := filepath.Join(dir, "a.json")
	authB := filepath.Join(dir, "b.json")
	for _, fixture := range []struct{ path, account string }{{authA, "private-a"}, {authB, "private-b"}} {
		data, _ := json.Marshal(map[string]any{"tokens": map[string]string{"access_token": "token-" + fixture.account, "account_id": fixture.account}})
		if err := os.WriteFile(fixture.path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.Server.StatePath = filepath.Join(dir, "server.sqlite")
	cfg.CodexAuthPaths = []string{authA, authB}
	cfg.Providers.Claude.Enabled = false
	st, err := store.Open(cfg.Server.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	base := time.Date(2026, 9, 10, 19, 0, 0, 0, time.UTC)
	for _, account := range []string{"private-a", "private-b"} {
		used := 25.0
		_, err := st.ApplyCodexPoll(context.Background(), store.CodexPollInput{
			Observation: resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: account}, ObservedAt: base, Windows: []resetwatch.Window{{Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: base.Add(5 * time.Hour)}}},
			CommittedAt: base.Add(time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		base = base.Add(time.Minute)
	}
	wantID := store.AccountID("codex", "private-b")
	oldFetch := fetchCodexLimits
	fetchCodexLimits = func(_ context.Context, _ *http.Client, opts remotecodex.FetchOptions) (remote.ProbeResult, error) {
		if len(opts.AuthPaths) != 1 || opts.AuthPaths[0] != authB || opts.ExpectedAccountID != "private-b" {
			t.Fatalf("fetch options=%+v", opts)
		}
		return remote.ProbeResult{ProviderID: "codex", AuthState: remote.AuthState{OK: true}, Lines: []model.MetricLine{{Type: "progress", Label: "5h limit"}}}, nil
	}
	t.Cleanup(func() { fetchCodexLimits = oldFetch })
	built, err := Build(cfg, nil, true, wantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Snapshot.Providers) != 1 || built.Snapshot.Providers[0].AccountID != wantID {
		t.Fatalf("providers=%+v", built.Snapshot.Providers)
	}
}

func TestBuildCodexAccountUsesStoredObservationWithoutRemote(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.StatePath = filepath.Join(dir, "server.sqlite")
	cfg.CodexAuthPaths = []string{filepath.Join(dir, "missing-auth.json")}
	cfg.Providers.Claude.Enabled = false
	st, err := store.Open(cfg.Server.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	used := 72.0
	at := time.Now().UTC().Add(-time.Hour)
	_, err = st.ApplyCodexPoll(context.Background(), store.CodexPollInput{Observation: resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: "private-history"}, ObservedAt: at, Windows: []resetwatch.Window{{Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: at.Add(5 * time.Hour)}}}, CommittedAt: at.Add(time.Second)})
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	_ = st.Close()
	built, err := Build(cfg, nil, false, store.AccountID("codex", "private-history"))
	if err != nil {
		t.Fatal(err)
	}
	provider := built.Snapshot.Providers[0]
	if provider.AccountID == "" || provider.ObservedAt == "" || provider.ObservedAgeMs == nil || !provider.ObservationStale || len(provider.Lines) == 0 {
		t.Fatalf("stored provider=%+v", provider)
	}
}
