package status

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	accountresolver "github.com/agensfield/scriba/internal/accounts"
	"github.com/agensfield/scriba/internal/cache"
	"github.com/agensfield/scriba/internal/cached"
	"github.com/agensfield/scriba/internal/config"
	"github.com/agensfield/scriba/internal/local/claude"
	"github.com/agensfield/scriba/internal/local/codex"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
	remoteclaude "github.com/agensfield/scriba/internal/remote/claude"
	remotecodex "github.com/agensfield/scriba/internal/remote/codex"
	"github.com/agensfield/scriba/internal/reports"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
)

type Built struct {
	Snapshot  model.StatusSnapshot
	ScanStats map[string]model.ScannerStats
}

var (
	fetchCodexLimits = remotecodex.FetchLimitsWithOptions
	resolveCodexLive = func(resolver *accountresolver.Resolver, ctx context.Context, selector string) (accountresolver.LiveAccount, error) {
		return resolver.ResolveLive(ctx, selector)
	}
)

func Build(cfg config.Config, c *cache.Cache, includeRemote bool, accountSelector ...string) (Built, error) {
	generatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	location, err := reports.Location(cfg.Timezone)
	if err != nil {
		return Built{}, fmt.Errorf("invalid timezone %q: %w", cfg.Timezone, err)
	}
	var providers []model.ProviderSnapshot
	scanStats := map[string]model.ScannerStats{}
	if cfg.Providers.Claude.Enabled {
		var events []model.LocalUsageEvent
		var stats model.ScannerStats
		var err error
		if c != nil {
			events, stats, err = cached.ScanClaude(c, cfg.Providers.Claude.Paths)
		} else {
			events, stats, err = claude.Scan(cfg.Providers.Claude.Paths)
		}
		if err != nil {
			return Built{}, err
		}
		scanStats["claude"] = stats
		provider := providerFromDaily("claude", "Claude", reports.DailyIn(events, true, location), stats, generatedAt, location)
		if includeRemote {
			appendRemote(&provider, remoteclaude.Probe)
		}
		providers = append(providers, provider)
	}
	if cfg.Providers.Codex.Enabled {
		var events []model.LocalUsageEvent
		var stats model.ScannerStats
		var err error
		if c != nil {
			events, stats, err = cached.ScanCodex(c, cfg.Providers.Codex.Paths)
		} else {
			events, stats, err = codex.Scan(cfg.Providers.Codex.Paths)
		}
		if err != nil {
			return Built{}, err
		}
		scanStats["codex"] = stats
		provider := providerFromDaily("codex", "Codex", reports.DailyIn(events, true, location), stats, generatedAt, location)
		selector := ""
		if len(accountSelector) > 0 {
			selector = accountSelector[0]
		}
		if includeRemote || selector != "" {
			if err := appendCodexAccount(&provider, cfg, selector, includeRemote); err != nil {
				if selector != "" {
					return Built{}, err
				}
				appendProviderError(&provider, err)
			}
		}
		providers = append(providers, provider)
	}
	return Built{
		Snapshot:  model.StatusSnapshot{SchemaVersion: model.SchemaVersion, GeneratedAt: generatedAt, Timezone: location.String(), Providers: providers},
		ScanStats: scanStats,
	}, nil
}

func appendCodexAccount(provider *model.ProviderSnapshot, cfg config.Config, selector string, includeRemote bool) error {
	ctx := context.Background()
	path := accountStatePath(cfg.Server.StatePath)
	st, err := store.OpenReadOnly(path)
	if errors.Is(err, os.ErrNotExist) {
		st = nil
	} else if err != nil {
		return err
	}
	if st != nil {
		defer func() { _ = st.Close() }()
	}
	resolver := accountresolver.New(st, accountresolver.Sources(cfg))
	account, err := resolver.Resolve(ctx, selector)
	if err != nil {
		return err
	}
	if !includeRemote {
		return appendStoredCodexAccount(provider, st, account)
	}
	live, err := resolveCodexLive(resolver, ctx, selector)
	if errors.Is(err, accountresolver.ErrCredentialsUnavailable) {
		if storedErr := appendStoredCodexAccount(provider, st, account); storedErr == nil {
			return nil
		}
	}
	if err != nil {
		return err
	}
	account = live.Account
	result, err := fetchCodexLimits(ctx, nil, live.FetchOptions())
	if err != nil {
		return err
	}
	provider.AccountID = account.ID
	provider.AccountAlias = account.Alias
	credentialsAvailable := account.CredentialsAvailable
	provider.CredentialsAvailable = &credentialsAvailable
	provider.Lines = append(result.Lines, provider.Lines...)
	provider.Provenance = append(provider.Provenance, result.Provenance...)
	if !result.AuthState.OK {
		provider.State = "degraded"
	}
	return nil
}

func accountStatePath(configured string) string {
	if configured != "" {
		return configured
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "scriba", "server.sqlite")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "scriba", "server.sqlite")
}

func appendStoredCodexAccount(provider *model.ProviderSnapshot, st *store.Store, account store.Account) error {
	if st == nil {
		return fmt.Errorf("no stored Codex limits for account %s", account.DisplayName())
	}
	observation, ok, err := st.LoadLatestObservationForAccount(context.Background(), account.ID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no stored Codex limits for account %s", account.DisplayName())
	}
	now := time.Now().UTC()
	age := now.Sub(observation.ObservedAt.UTC())
	if age < 0 {
		age = 0
	}
	ageMs := age.Milliseconds()
	stale := age > 15*time.Minute
	provider.AccountID = account.ID
	provider.AccountAlias = account.Alias
	credentialsAvailable := account.CredentialsAvailable
	provider.CredentialsAvailable = &credentialsAvailable
	provider.ObservedAt = observation.ObservedAt.UTC().Format(time.RFC3339Nano)
	provider.ObservedAgeMs = &ageMs
	provider.ObservationStale = stale
	provider.Lines = append(metricLinesFromObservation(observation), provider.Lines...)
	provider.Provenance = append(provider.Provenance, model.SourceProvenance{Kind: "resident-store", ProviderID: "codex", FetchedAt: provider.ObservedAt, CacheAgeMs: &ageMs, Stale: stale})
	if stale {
		provider.State = "degraded"
	}
	return nil
}

func metricLinesFromObservation(observation resetwatch.Observation) []model.MetricLine {
	lines := make([]model.MetricLine, 0, len(observation.Windows)+2)
	limit := 100.0
	for _, window := range observation.Windows {
		line := model.MetricLine{Type: "progress", Label: window.Label, Limit: &limit, ResetsAt: window.ResetAt.UTC().Format(time.RFC3339Nano), PeriodDurationMs: window.PeriodDurationMs}
		if window.UsedPercent != nil {
			used := *window.UsedPercent
			line.Used = &used
		}
		lines = append(lines, line)
	}
	if observation.ResetGrants.AvailableCount != nil {
		lines = append(lines, model.MetricLine{Type: "amount", Label: resetwatch.LabelResetGrants, Value: *observation.ResetGrants.AvailableCount, Format: &model.MetricFormat{Kind: "count", Suffix: "available"}})
	}
	if !observation.ResetGrants.ExpiresAt.IsZero() {
		lines = append(lines, model.MetricLine{Type: "text", Label: resetwatch.LabelGrantExpiry, Value: observation.ResetGrants.ExpiresAt.UTC().Format(time.RFC3339Nano)})
	}
	return lines
}

func Save(c *cache.Cache, built Built) error {
	if err := c.SaveSnapshot("status", built.Snapshot, built.Snapshot.GeneratedAt); err != nil {
		return err
	}
	for providerID, stats := range built.ScanStats {
		if err := c.SaveScanStats(providerID, stats, built.Snapshot.GeneratedAt); err != nil {
			return err
		}
	}
	return nil
}

func MarkStale(snapshot model.StatusSnapshot, err error) model.StatusSnapshot {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i := range snapshot.Providers {
		if snapshot.Providers[i].State != "broken" {
			snapshot.Providers[i].State = "degraded"
		}
		snapshot.Providers[i].Provenance = append(snapshot.Providers[i].Provenance, model.SourceProvenance{
			Kind: "cache", ProviderID: snapshot.Providers[i].ProviderID, FetchedAt: now, Stale: true, Error: err.Error(),
		})
	}
	return snapshot
}

func providerFromDaily(providerID, displayName string, daily []model.DailyReportRow, stats model.ScannerStats, generatedAt string, location *time.Location) model.ProviderSnapshot {
	now := time.Now().In(location)
	todayKey := now.Format("2006-01-02")
	yesterdayKey := now.AddDate(0, 0, -1).Format("2006-01-02")
	var today, yesterday model.TokenUsage
	var last30 model.TokenUsage
	for i, row := range daily {
		if row.Date == todayKey {
			today = row.TokenUsage
		}
		if row.Date == yesterdayKey {
			yesterday = row.TokenUsage
		}
		if i < 30 {
			last30.EffectiveTokens += row.EffectiveTokens
			last30.TotalTokens += row.TotalTokens
		}
	}
	prov := []model.SourceProvenance{{Kind: "local-log", ProviderID: providerID, FetchedAt: generatedAt}}
	state := "ok"
	if stats.Files == 0 && len(stats.MissingDirectories) > 0 {
		state = "degraded"
	}
	return model.ProviderSnapshot{
		ProviderID:  providerID,
		DisplayName: displayName,
		State:       state,
		Lines: []model.MetricLine{
			{Type: "text", Label: "Today", Value: formatUsage(today), Provenance: prov},
			{Type: "text", Label: "Yesterday", Value: formatUsage(yesterday), Provenance: prov},
			{Type: "text", Label: "Last 30 Days", Value: formatUsage(last30), Provenance: prov},
		},
		Provenance: prov,
	}
}

func formatUsage(usage model.TokenUsage) string {
	if usage.EffectiveTokens == 0 && usage.TotalTokens > 0 {
		return fmt.Sprintf("%s traffic", formatInt(usage.TotalTokens))
	}
	return fmt.Sprintf("%s effective · %s traffic", formatInt(usage.EffectiveTokens), formatInt(usage.TotalTokens))
}

func appendRemote(provider *model.ProviderSnapshot, probe func(bool) (remote.ProbeResult, error)) {
	result, err := probe(true)
	if err != nil {
		appendProviderError(provider, err)
		return
	}
	provider.Lines = append(result.Lines, provider.Lines...)
	provider.Provenance = append(provider.Provenance, result.Provenance...)
	for _, provenance := range result.Provenance {
		if provenance.Error != "" && provider.State != "broken" {
			provider.State = "degraded"
		}
	}
}

func appendProviderError(provider *model.ProviderSnapshot, err error) {
	provider.Provenance = append(provider.Provenance, model.SourceProvenance{
		Kind:       "provider-api",
		ProviderID: provider.ProviderID,
		FetchedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		Error:      err.Error(),
	})
	if provider.State != "broken" {
		provider.State = "degraded"
	}
}

func formatInt(value int64) string {
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}
	text := fmt.Sprintf("%d", value)
	for i := len(text) - 3; i > 0; i -= 3 {
		text = text[:i] + "," + text[i:]
	}
	return sign + text
}
