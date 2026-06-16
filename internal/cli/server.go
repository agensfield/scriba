package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/agensfield/scriba/internal/buildinfo"
	"github.com/agensfield/scriba/internal/config"
	"github.com/agensfield/scriba/internal/radar"
	servercore "github.com/agensfield/scriba/internal/server"
	"github.com/agensfield/scriba/internal/server/store"
	"github.com/agensfield/scriba/internal/telegram"
)

func runServer(command string, opts options) error {
	cfg, err := load(opts)
	if err != nil {
		return err
	}
	if opts.statePath != "" {
		cfg.Server.StatePath = opts.statePath
	}
	if opts.env != "" {
		cfg.Server.Environment = opts.env
	}
	switch command {
	case "run":
		return runServerRun(cfg, opts)
	case "status":
		return runServerStatus(cfg, opts)
	case "health":
		return runServerHealth(cfg, opts)
	case "stats":
		return runServerStats(cfg, opts)
	case "refresh":
		return runServerRefresh(cfg, opts)
	case "radar":
		return runServerRadar(opts)
	case "prune":
		return runServerPrune(cfg, opts)
	default:
		return fmt.Errorf("unknown server command: %s", command)
	}
}

func runServerRun(cfg config.Config, opts options) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(resolveServerStatePath(cfg.Server.StatePath))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	heartbeat, err := shouldSendStartupHeartbeat(ctx, st, cfg.Server)
	if err != nil {
		return err
	}
	srv := servercore.New(st, nil, nil, servercore.Config{
		AccountLabel:             cfg.Server.AccountLabel,
		JokeTone:                 cfg.Telegram.ResetJokeTone,
		StartupHeartbeat:         heartbeat,
		ObservationRetentionDays: cfg.Server.ObservationRetentionDays,
	})
	srv.SetRadarFetcher(radar.Client{})
	if cfg.Telegram.Enabled {
		token := cfg.Telegram.BotToken
		if token == "" {
			token = os.Getenv(cfg.Telegram.BotTokenEnv)
		}
		if token == "" {
			return fmt.Errorf("missing Telegram bot token; set telegram.botToken or env %s", cfg.Telegram.BotTokenEnv)
		}
		chatID, err := strconv.ParseInt(cfg.Telegram.ChatID, 10, 64)
		if err != nil || chatID == 0 {
			return fmt.Errorf("invalid telegram.chatId: %q", cfg.Telegram.ChatID)
		}
		tg, err := telegram.NewBotService(telegram.BotConfig{
			Token:          token,
			ChatID:         chatID,
			AllowedUserIDs: cfg.Telegram.AllowedUserIDs,
		}, srv, st, st, radar.Client{})
		if err != nil {
			return err
		}
		srv.SetNotifier(tg)
		go func() {
			if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintln(os.Stderr, err)
				stop()
			}
		}()
		tg.Start(ctx)
		return nil
	}
	return srv.Run(ctx)
}

func runServerStatus(cfg config.Config, opts options) error {
	st, err := store.Open(resolveServerStatePath(cfg.Server.StatePath))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	version, err := st.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	srv := servercore.New(st, nil, nil, servercore.Config{
		AccountLabel:             cfg.Server.AccountLabel,
		JokeTone:                 cfg.Telegram.ResetJokeTone,
		ObservationRetentionDays: cfg.Server.ObservationRetentionDays,
	})
	interval, err := srv.PollInterval(ctx)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"statePath":                st.Path(),
		"schemaVersion":            version,
		"version":                  buildinfo.Version,
		"commit":                   buildinfo.Commit,
		"pollInterval":             servercore.FormatDuration(interval),
		"telegramEnabled":          cfg.Telegram.Enabled,
		"environment":              cfg.Server.Environment,
		"observationRetentionDays": cfg.Server.ObservationRetentionDays,
	}
	return output(opts, payload, fmt.Sprintf("scriba server · %s · %s · poll %s", buildinfo.Version, st.Path(), servercore.FormatDuration(interval)))
}

func runServerHealth(cfg config.Config, opts options) error {
	st, err := store.Open(resolveServerStatePath(cfg.Server.StatePath))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	srv := servercore.New(st, nil, nil, servercore.Config{
		AccountLabel:             cfg.Server.AccountLabel,
		JokeTone:                 cfg.Telegram.ResetJokeTone,
		ObservationRetentionDays: cfg.Server.ObservationRetentionDays,
	})
	health, err := srv.Health(context.Background())
	if err != nil {
		return err
	}
	return output(opts, healthPayload(health), renderServerHealth(health))
}

func runServerStats(cfg config.Config, opts options) error {
	st, err := store.Open(resolveServerStatePath(cfg.Server.StatePath))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	srv := servercore.New(st, nil, nil, servercore.Config{
		AccountLabel:             cfg.Server.AccountLabel,
		JokeTone:                 cfg.Telegram.ResetJokeTone,
		ObservationRetentionDays: cfg.Server.ObservationRetentionDays,
	})
	stats, err := srv.Stats(context.Background())
	if err != nil {
		return err
	}
	payload := serverStatsPayload(stats, cfg.Server.Environment, cfg.Telegram.Enabled)
	human := renderServerStats(stats, cfg.Server.Environment, cfg.Telegram.Enabled)
	return output(opts, payload, human)
}

func runServerRefresh(cfg config.Config, opts options) error {
	st, err := store.Open(resolveServerStatePath(cfg.Server.StatePath))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	srv := servercore.New(st, nil, nil, servercore.Config{
		AccountLabel:             cfg.Server.AccountLabel,
		JokeTone:                 cfg.Telegram.ResetJokeTone,
		ObservationRetentionDays: cfg.Server.ObservationRetentionDays,
	})
	result, err := srv.RefreshNow(context.Background())
	if err != nil {
		return err
	}
	return output(opts, serverRefreshPayload(result), telegram.RenderLimits(result.Observation))
}

func runServerPrune(cfg config.Config, opts options) error {
	st, err := store.Open(resolveServerStatePath(cfg.Server.StatePath))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	srv := servercore.New(st, nil, nil, servercore.Config{
		AccountLabel:             cfg.Server.AccountLabel,
		JokeTone:                 cfg.Telegram.ResetJokeTone,
		ObservationRetentionDays: cfg.Server.ObservationRetentionDays,
	})
	result, err := srv.PruneObservations(context.Background(), true)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"cutoff":              result.Cutoff.Format(time.RFC3339),
		"deletedObservations": result.DeletedObservations,
		"deletedWindows":      result.DeletedWindows,
		"checkpointed":        result.Checkpointed,
		"vacuumed":            result.Vacuumed,
	}
	return output(opts, payload, fmt.Sprintf("pruned %d observations and %d windows before %s", result.DeletedObservations, result.DeletedWindows, result.Cutoff.Format(time.RFC3339)))
}

func runServerRadar(opts options) error {
	client := radar.Client{}
	current, err := client.Fetch(context.Background())
	if err != nil {
		return err
	}
	return output(opts, current, client.RenderText(current))
}

func shouldSendStartupHeartbeat(ctx context.Context, st *store.Store, cfg config.ServerConfig) (bool, error) {
	if cfg.Environment == "dev" {
		return true, nil
	}
	limit := time.Duration(cfg.StartupHeartbeatRateLimitMinutes) * time.Minute
	if limit <= 0 {
		limit = 30 * time.Minute
	}
	const key = "startup_heartbeat_at"
	value, ok, err := st.GetSetting(ctx, key)
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if ok {
		last, err := time.Parse(time.RFC3339Nano, value)
		if err == nil && now.Sub(last) < limit {
			return false, nil
		}
	}
	return true, st.SetSetting(ctx, key, now.Format(time.RFC3339Nano))
}

func resolveServerStatePath(configured string) string {
	if configured != "" {
		return configured
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "scriba", "server.sqlite")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "scriba", "server.sqlite")
}

func serverRefreshPayload(result servercore.PollResult) map[string]any {
	events := make([]map[string]any, 0, len(result.Decision.Events))
	for _, event := range result.Decision.Events {
		events = append(events, map[string]any{
			"id":              event.ID,
			"resetKind":       event.ResetKind,
			"trigger":         event.PrimaryTriggerLabel,
			"previousResetAt": event.PreviousResetAt,
			"currentResetAt":  event.CurrentResetAt,
			"detectedAt":      event.DetectedAt,
			"jokeId":          event.JokeID,
		})
	}
	warnings := make([]map[string]any, 0, len(result.Warnings))
	for _, warning := range result.Warnings {
		warnings = append(warnings, map[string]any{
			"id":                 warning.ID,
			"label":              warning.Label,
			"thresholdRemaining": warning.ThresholdRemaining,
			"usedPercent":        warning.UsedPercent,
			"remainingPercent":   warning.RemainingPercent,
			"resetAt":            warning.ResetAt,
			"detectedAt":         warning.DetectedAt,
		})
	}
	grantWarnings := make([]map[string]any, 0, len(result.GrantWarnings))
	for _, warning := range result.GrantWarnings {
		grantWarnings = append(grantWarnings, map[string]any{
			"id":            warning.ID,
			"creditId":      warning.CreditID,
			"creditTitle":   warning.CreditTitle,
			"thresholdDays": warning.ThresholdDays,
			"expiresAt":     warning.ExpiresAt,
			"detectedAt":    warning.DetectedAt,
		})
	}
	return map[string]any{
		"baseline":      result.Baseline,
		"inserted":      result.Inserted,
		"account":       result.Observation.Account,
		"windows":       result.Observation.Windows,
		"events":        events,
		"warnings":      warnings,
		"grantWarnings": grantWarnings,
	}
}

func serverStatsPayload(stats servercore.Stats, environment string, telegramEnabled bool) map[string]any {
	return map[string]any{
		"environment":              environment,
		"telegramEnabled":          telegramEnabled,
		"pollInterval":             servercore.FormatDuration(stats.PollInterval),
		"observationRetentionDays": stats.ObservationRetentionDays,
		"version":                  stats.Version,
		"commit":                   stats.Commit,
		"health":                   healthPayload(stats.Health),
		"store":                    stats.Store,
	}
}

func healthPayload(health servercore.Health) map[string]any {
	return map[string]any{
		"status":                   health.Status,
		"version":                  health.Version,
		"commit":                   health.Commit,
		"pollInterval":             servercore.FormatDuration(health.PollInterval),
		"observationRetentionDays": health.ObservationRetentionDays,
		"lastSuccessAt":            health.LastSuccessAt,
		"lastFailureAt":            health.LastFailureAt,
		"lastError":                health.LastError,
		"failureKind":              health.FailureKind,
		"consecutiveFailures":      health.ConsecutiveFailures,
		"nextPollEstimateAt":       health.NextPollEstimateAt,
		"staleAfter":               servercore.FormatDuration(health.StaleAfter),
		"isStale":                  health.IsStale,
	}
}

func renderServerStats(stats servercore.Stats, environment string, telegramEnabled bool) string {
	var b strings.Builder
	b.WriteString("Scriba stats\n")
	writeRows(&b, []string{
		fmt.Sprintf("%-13s %s", "version", stats.Version),
		fmt.Sprintf("%-13s %s", "commit", stats.Commit),
		fmt.Sprintf("%-13s %s", "poll", servercore.FormatDuration(stats.PollInterval)),
		fmt.Sprintf("%-13s %dd", "retention", stats.ObservationRetentionDays),
		fmt.Sprintf("%-13s %s", "env", environment),
		fmt.Sprintf("%-13s %t", "telegram", telegramEnabled),
	})
	b.WriteString("\nHealth\n")
	writeHealthRows(&b, stats.Health)
	if stats.Store.LatestObservation != nil {
		latest := stats.Store.LatestObservation
		b.WriteString("\nObservation\n")
		rows := []string{
			fmt.Sprintf("%-13s %s", "latest", formatCLIStatsTime(latest.ObservedAt)),
			fmt.Sprintf("%-13s %d", "latest win", latest.Windows),
			fmt.Sprintf("%-13s %s", "account", latest.AccountLabel),
		}
		if latest.AccountEmail != "" || latest.AccountPlan != "" {
			account := latest.AccountEmail
			if latest.AccountEmail != "" && latest.AccountPlan != "" {
				account += " · "
			}
			account += latest.AccountPlan
			rows = append(rows, fmt.Sprintf("%-13s %s", "plan", account))
		}
		writeRows(&b, rows)
	}
	counts := stats.Store.Counts
	b.WriteString("\nStorage\n")
	writeRows(&b, []string{
		fmt.Sprintf("%-13s %s", "db", formatCLIBytes(stats.Store.DBFiles.TotalBytes)),
		fmt.Sprintf("%-13s %s", "main", formatCLIBytes(stats.Store.DBFiles.MainBytes)),
		fmt.Sprintf("%-13s %s", "wal", formatCLIBytes(stats.Store.DBFiles.WALBytes)),
		fmt.Sprintf("%-13s %d", "accounts", counts["accounts"]),
		fmt.Sprintf("%-13s %d", "stored polls", counts["limit_observations"]),
		fmt.Sprintf("%-13s %d", "stored win", counts["observed_windows"]),
		fmt.Sprintf("%-13s %d", "tracked win", counts["limit_windows"]),
		fmt.Sprintf("%-13s %d", "resets", counts["reset_events"]),
		fmt.Sprintf("%-13s %d", "warnings", counts["limit_warning_events"]),
		fmt.Sprintf("%-13s %d", "grant warn", counts["reset_grant_warning_events"]),
	})
	b.WriteString("\nReset deliveries\n")
	writeDeliveryRows(&b, stats.Store.ResetDeliveries)
	b.WriteString("\nWarning deliveries\n")
	writeDeliveryRows(&b, stats.Store.WarningDeliveries)
	b.WriteString("\nGrant warning deliveries\n")
	writeDeliveryRows(&b, stats.Store.GrantWarningDeliveries)
	if stats.Store.LastReset != nil || stats.Store.LastWarning != nil || stats.Store.LastGrantWarning != nil {
		b.WriteString("\nRecent\n")
		var rows []string
		if stats.Store.LastReset != nil {
			reset := stats.Store.LastReset
			rows = append(rows, fmt.Sprintf("%-13s %s · %s · %s", "reset", reset.Trigger, reset.Kind, formatCLIStatsTime(reset.DetectedAt)))
		}
		if stats.Store.LastWarning != nil {
			warning := stats.Store.LastWarning
			rows = append(rows, fmt.Sprintf("%-13s %s · %d%% left · %s", "warning", warning.Label, warning.ThresholdRemaining, formatCLIStatsTime(warning.DetectedAt)))
		}
		if stats.Store.LastGrantWarning != nil {
			warning := stats.Store.LastGrantWarning
			rows = append(rows, fmt.Sprintf("%-13s %dd · %s · %s", "grant", warning.ThresholdDays, warning.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"), formatCLIStatsTime(warning.DetectedAt)))
		}
		writeRows(&b, rows)
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderServerHealth(health servercore.Health) string {
	var b strings.Builder
	b.WriteString("Scriba health\n")
	writeHealthRows(&b, health)
	return strings.TrimRight(b.String(), "\n")
}

func writeHealthRows(b *strings.Builder, health servercore.Health) {
	rows := []string{
		fmt.Sprintf("%-13s %s", "status", health.Status),
		fmt.Sprintf("%-13s %s", "version", health.Version),
		fmt.Sprintf("%-13s %s", "poll", servercore.FormatDuration(health.PollInterval)),
	}
	if health.LastSuccessAt != nil {
		rows = append(rows, fmt.Sprintf("%-13s %s", "last ok", formatCLIStatsTime(*health.LastSuccessAt)))
	}
	if health.LastFailureAt != nil {
		rows = append(rows, fmt.Sprintf("%-13s %s", "last fail", formatCLIStatsTime(*health.LastFailureAt)))
	}
	if health.NextPollEstimateAt != nil {
		rows = append(rows, fmt.Sprintf("%-13s %s", "next", formatCLIStatsTime(*health.NextPollEstimateAt)))
	}
	rows = append(rows, fmt.Sprintf("%-13s %d", "failures", health.ConsecutiveFailures))
	if health.FailureKind != "" {
		rows = append(rows, fmt.Sprintf("%-13s %s", "kind", health.FailureKind))
	}
	if health.LastError != "" {
		rows = append(rows, fmt.Sprintf("%-13s %s", "error", truncateCLI(health.LastError, 160)))
	}
	writeRows(b, rows)
}

func writeDeliveryRows(b *strings.Builder, counts map[string]store.DeliveryCounts) {
	rows := make([]string, 0, 3)
	for _, status := range []string{"pending", "failed", "delivered"} {
		count := counts[status]
		rows = append(rows, fmt.Sprintf("%-13s %3d  attempts %d", status, count.Count, count.Attempts))
	}
	writeRows(b, rows)
}

func writeRows(b *strings.Builder, rows []string) {
	for _, row := range rows {
		b.WriteString(row)
		b.WriteString("\n")
	}
}

func formatCLIStatsTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return humanize.Time(t) + " · " + t.Local().Format("Mon 15:04:05")
}

func formatCLIBytes(value int64) string {
	if value <= 0 {
		return "0 B"
	}
	return humanize.Bytes(uint64(value))
}

func truncateCLI(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}
