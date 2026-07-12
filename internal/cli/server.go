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
	"github.com/agensfield/scriba/internal/localapi"
	"github.com/agensfield/scriba/internal/radar"
	"github.com/agensfield/scriba/internal/resetwatch"
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
	case "backup":
		return runServerBackup(cfg, opts)
	default:
		return fmt.Errorf("unknown server command: %s", command)
	}
}

func runServerBackup(cfg config.Config, opts options) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.OpenExisting(resolveServerStatePath(cfg.Server.StatePath))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	result, err := st.Backup(ctx, opts.backupDir, opts.retention)
	if err != nil {
		return err
	}
	if result.SizeBytes < 0 {
		return fmt.Errorf("backup reported a negative size: %d", result.SizeBytes)
	}
	human := fmt.Sprintf("backup verified · %s · %s · sha256 %s · schema %d · quick_check %s · pruned %d", result.Path, humanize.Bytes(uint64(result.SizeBytes)), result.SHA256, result.SchemaVersion, result.QuickCheck, result.Pruned) // #nosec G115 -- negative sizes are rejected above.
	return output(opts, result, human)
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
	chatID, notificationTarget, err := telegramDeliveryTarget(cfg)
	if err != nil {
		return err
	}
	srv := servercore.New(st, nil, nil, servercore.Config{
		NotificationTarget:       notificationTarget,
		AccountLabel:             cfg.Server.AccountLabel,
		JokeTone:                 cfg.Telegram.ResetJokeTone,
		StartupHeartbeat:         heartbeat,
		ObservationRetentionDays: cfg.Server.ObservationRetentionDays,
	})
	srv.SetRadarFetcher(radar.Client{})
	children := []func(context.Context) error{srv.Run}
	if cfg.Server.ContextAPI.Enabled {
		socketPath := resolveContextAPISocketPath(st.Path(), cfg.Server.ContextAPI.SocketPath)
		listener, err := localapi.Listen(ctx, socketPath)
		if err != nil {
			return err
		}
		defer func() { _ = listener.Close() }()
		api := localapi.NewHTTPServer(listener, agentContextService(cfg), localapi.HTTPConfig{})
		children = append(children, api.Run)
	}
	if cfg.Telegram.Enabled {
		token := cfg.Telegram.BotToken
		if token == "" {
			token = os.Getenv(cfg.Telegram.BotTokenEnv)
		}
		if token == "" {
			return fmt.Errorf("missing Telegram bot token; set telegram.botToken or env %s", cfg.Telegram.BotTokenEnv)
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
		children = append(children, func(ctx context.Context) error { tg.Start(ctx); return nil })
	}
	if len(children) == 1 {
		return srv.Run(ctx)
	}
	return supervise(ctx, children...)
}

func resolveContextAPISocketPath(statePath, configured string) string {
	if configured != "" {
		return configured
	}
	if absolute, err := filepath.Abs(statePath); err == nil {
		statePath = absolute
	}
	return filepath.Join(filepath.Dir(statePath), "context.sock")
}

func supervise(ctx context.Context, children ...func(context.Context) error) error {
	return superviseWithTimeout(ctx, 5*time.Second, children...)
}

func superviseWithTimeout(ctx context.Context, joinTimeout time.Duration, children ...func(context.Context) error) error {
	if len(children) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, len(children))
	for _, child := range children {
		child := child
		go func() { errs <- child(ctx) }()
	}
	var result error
	completed := 0
	select {
	case first := <-errs:
		completed = 1
		if first != nil && !errors.Is(first, context.Canceled) {
			result = first
		} else if ctx.Err() == nil {
			result = errors.New("resident service child exited unexpectedly")
		}
	case <-ctx.Done():
	}
	cancel()
	deadline := time.NewTimer(joinTimeout)
	defer deadline.Stop()
	for i := completed; i < len(children); i++ {
		select {
		case err := <-errs:
			if result == nil && err != nil && !errors.Is(err, context.Canceled) {
				result = err
			}
		case <-deadline.C:
			if result == nil {
				result = errors.New("resident service shutdown timed out")
			}
			return result
		}
	}
	if ctx.Err() != nil && result == nil {
		return nil
	}
	return result
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
	_, notificationTarget, err := telegramDeliveryTarget(cfg)
	if err != nil {
		return err
	}
	srv := servercore.New(st, nil, nil, servercore.Config{
		NotificationTarget:       notificationTarget,
		AccountLabel:             cfg.Server.AccountLabel,
		JokeTone:                 cfg.Telegram.ResetJokeTone,
		ObservationRetentionDays: cfg.Server.ObservationRetentionDays,
	})
	result, err := srv.RefreshNow(context.Background())
	if err != nil {
		return err
	}
	return output(opts, serverRefreshPayload(result), renderServerRefresh(result))
}

func telegramDeliveryTarget(cfg config.Config) (int64, string, error) {
	if !cfg.Telegram.Enabled {
		return 0, "", nil
	}
	chatID, err := strconv.ParseInt(cfg.Telegram.ChatID, 10, 64)
	if err != nil || chatID == 0 {
		return 0, "", fmt.Errorf("invalid telegram.chatId: %q", cfg.Telegram.ChatID)
	}
	return chatID, fmt.Sprintf("telegram:%d", chatID), nil
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
		"lastAttemptAt":            health.LastAttemptAt,
		"lastFailureAt":            health.LastFailureAt,
		"lastError":                health.LastError,
		"failureKind":              health.FailureKind,
		"consecutiveFailures":      health.ConsecutiveFailures,
		"nextPollEstimateAt":       health.NextPollEstimateAt,
		"staleAfter":               servercore.FormatDuration(health.StaleAfter),
		"isStale":                  health.IsStale,
		"queueReason":              health.QueueReason,
		"outbox":                   health.Outbox,
		"telegramInbox":            health.TelegramInbox,
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
	b.WriteString("\nOutbox\n")
	writeQueueRows(&b, stats.Store.Outbox)
	b.WriteString("\nTelegram inbox\n")
	writeInboxRows(&b, stats.Store.TelegramInbox)
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
		fmt.Sprintf("%-13s %d", "grants", counts["reset_grant_events"]),
		fmt.Sprintf("%-13s %d", "grant warn", counts["reset_grant_warning_events"]),
	})
	b.WriteString("\nReset deliveries\n")
	writeDeliveryRows(&b, stats.Store.ResetDeliveries)
	b.WriteString("\nWarning deliveries\n")
	writeDeliveryRows(&b, stats.Store.WarningDeliveries)
	b.WriteString("\nGrant warning deliveries\n")
	writeDeliveryRows(&b, stats.Store.GrantWarningDeliveries)
	b.WriteString("\nGrant deliveries\n")
	writeDeliveryRows(&b, stats.Store.GrantDeliveries)
	if stats.Store.LastReset != nil || stats.Store.LastWarning != nil || stats.Store.LastGrantWarning != nil || stats.Store.LastGrant != nil {
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
			rows = append(rows, fmt.Sprintf("%-13s %dd warning · %s · %s", "grant", warning.ThresholdDays, warning.ExpiresAt.Local().Format("2006-01-02 15:04 MST"), formatCLIStatsTime(warning.DetectedAt)))
		}
		if stats.Store.LastGrant != nil {
			grant := stats.Store.LastGrant
			rows = append(rows, fmt.Sprintf("%-13s %d available · %s · %s", "grant", grant.AvailableCount, grant.ExpiresAt.Local().Format("2006-01-02 15:04 MST"), formatCLIStatsTime(grant.DetectedAt)))
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

func renderServerRefresh(result servercore.PollResult) string {
	obs := result.Observation
	var b strings.Builder
	b.WriteString(cliHeader("Codex limits"))
	b.WriteString("\n")
	if !obs.ObservedAt.IsZero() {
		fmt.Fprintf(&b, "%s\n", cliMuted("observed "+formatCLIStatsTime(obs.ObservedAt)))
	}
	if obs.Account.Email != "" || obs.Account.Plan != "" || obs.Account.Label != "" {
		b.WriteString("\n")
		b.WriteString(cliBold("Account"))
		b.WriteString("\n")
		account := obs.Account.Label
		if account == "" {
			account = obs.Account.Email
		}
		if obs.Account.Plan != "" {
			account += " · " + obs.Account.Plan
		}
		fmt.Fprintf(&b, "%s\n", account)
	}
	if len(obs.Windows) > 0 {
		b.WriteString("\n")
		b.WriteString(cliBold("Windows"))
		b.WriteString("\n")
		for _, window := range obs.Windows {
			fmt.Fprintf(&b, "%s\n", renderServerWindow(window))
		}
	}
	if grants := renderServerResetGrants(obs.ResetGrants); grants != "" {
		b.WriteString("\n")
		b.WriteString(grants)
		b.WriteString("\n")
	}
	if len(result.Decision.Events) > 0 || len(result.Warnings) > 0 || len(result.GrantWarnings) > 0 || len(result.ResetGrants) > 0 {
		b.WriteString("\n")
		b.WriteString(cliBold("Notifications"))
		b.WriteString("\n")
		if len(result.Decision.Events) > 0 {
			fmt.Fprintf(&b, "%-13s %d reset events\n", "resets", len(result.Decision.Events))
		}
		if len(result.Warnings) > 0 {
			fmt.Fprintf(&b, "%-13s %d limit warnings\n", "limits", len(result.Warnings))
		}
		if len(result.GrantWarnings) > 0 {
			fmt.Fprintf(&b, "%-13s %d grant warnings\n", "grants", len(result.GrantWarnings))
		}
		if len(result.ResetGrants) > 0 {
			fmt.Fprintf(&b, "%-13s %d grant loaded\n", "grants", len(result.ResetGrants))
		}
	} else {
		b.WriteString("\n")
		b.WriteString(cliMuted("no notifications emitted"))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderServerWindow(window resetwatch.Window) string {
	percent := 0.0
	if window.UsedPercent != nil {
		percent = *window.UsedPercent
	}
	reset := "unknown"
	if !window.ResetAt.IsZero() {
		reset = window.ResetAt.Local().Format("Mon 15:04")
	}
	return fmt.Sprintf("%-13s %s %3.0f%% used · resets %s", serverWindowLabel(window.Label), cliBar(percent), percent, reset)
}

func renderServerResetGrants(grants resetwatch.ResetGrants) string {
	if grants.AvailableCount == nil && grants.ExpiresAt.IsZero() && len(grants.Credits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(cliBold("Reset grants"))
	b.WriteString("\n")
	if grants.AvailableCount != nil {
		fmt.Fprintf(&b, "%-13s %s\n", "available", cliGreen(fmt.Sprint(*grants.AvailableCount)))
	}
	if !grants.ExpiresAt.IsZero() {
		fmt.Fprintf(&b, "%-13s %s\n", "earliest", formatGrantExpiry(grants.ExpiresAt.Format(time.RFC3339Nano), time.Now().UTC()))
	}
	for i, credit := range grants.Credits {
		title := credit.Title
		if title == "" {
			title = "Reset grant"
		}
		fmt.Fprintf(&b, "\n%s %s\n", cliMuted(fmt.Sprintf("%d.", i+1)), cliBold(title))
		if !credit.ExpiresAt.IsZero() {
			fmt.Fprintf(&b, "   %-9s %s\n", cliLabel("expires"), formatGrantExpiry(credit.ExpiresAt.Format(time.RFC3339Nano), time.Now().UTC()))
		}
		if !credit.GrantedAt.IsZero() {
			fmt.Fprintf(&b, "   %-9s %s\n", cliLabel("granted"), cliValue(formatGrantTime(credit.GrantedAt.Format(time.RFC3339Nano))))
		}
		if credit.Status != "" {
			status := cliValue(credit.Status)
			if strings.EqualFold(credit.Status, "available") {
				status = cliGreen(credit.Status)
			}
			fmt.Fprintf(&b, "   %-9s %s\n", cliLabel("status"), status)
		}
		if credit.ID != "" {
			fmt.Fprintf(&b, "   %-9s %s\n", cliLabel("id"), cliMuted(credit.ID))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func serverWindowLabel(label string) string {
	switch label {
	case resetwatch.LabelWeeklyLimit:
		return "Weekly"
	case resetwatch.LabelFiveHour:
		return "5h"
	case resetwatch.LabelSparkWeekly:
		return "Spark weekly"
	case resetwatch.LabelSparkFive:
		return "Spark 5h"
	case resetwatch.LabelReviewFive:
		return "Review 5h"
	case resetwatch.LabelReviewWeek:
		return "Review weekly"
	default:
		return label
	}
}

func cliBar(percent float64) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	width := 10
	filled := int((percent + 5) / 10)
	color := cliGreen
	if percent >= 90 {
		color = func(text string) string { return cliANSI("31;1", text) }
	} else if percent >= 70 {
		color = cliYellow
	}
	return color(strings.Repeat("▰", filled)) + strings.Repeat("▱", width-filled)
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
	if health.QueueReason != "" {
		rows = append(rows, fmt.Sprintf("%-13s %s", "queue", health.QueueReason))
	}
	writeRows(b, rows)
}

func writeQueueRows(b *strings.Builder, q store.QueueStats) {
	writeRows(b, []string{
		fmt.Sprintf("%-13s %d", "pending", q.Pending), fmt.Sprintf("%-13s %d", "due", q.DuePending),
		fmt.Sprintf("%-13s %d", "leased", q.Leased), fmt.Sprintf("%-13s %d", "expired", q.ExpiredLeases),
		fmt.Sprintf("%-13s %d", "delivered", q.Delivered), fmt.Sprintf("%-13s %d", "dead letter", q.DeadLetter),
		fmt.Sprintf("%-13s %d", "attempts", q.Attempts), fmt.Sprintf("%-13s %s", "oldest", formatQueueAge(q.OldestPendingAt, q.OldestPendingAge)),
	})
}

func writeInboxRows(b *strings.Builder, q store.InboxStats) {
	writeRows(b, []string{
		fmt.Sprintf("%-13s %d", "pending", q.Pending), fmt.Sprintf("%-13s %d", "due", q.Due),
		fmt.Sprintf("%-13s %d", "processed", q.Processed), fmt.Sprintf("%-13s %d", "dead", q.Dead),
		fmt.Sprintf("%-13s %d", "attempts", q.Attempts), fmt.Sprintf("%-13s %s", "oldest", formatQueueAge(q.OldestPendingAt, q.OldestPendingAge)),
	})
}

func formatQueueAge(at *time.Time, age time.Duration) string {
	if at == nil {
		return "none"
	}
	return servercore.FormatDuration(age) + " · " + at.Local().Format("Mon 15:04:05")
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
