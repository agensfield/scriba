package telegram

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"strings"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/agensfield/scriba/internal/radar"
	"github.com/agensfield/scriba/internal/remote"
	remotecodex "github.com/agensfield/scriba/internal/remote/codex"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server"
	"github.com/agensfield/scriba/internal/server/store"
)

func RenderBaseline(notice server.BaselineNotice) string {
	grants := resetwatch.ResetGrantsFromSnapshotJSON(notice.SnapshotJSON)
	return "<b>Scriba is alive</b>\nstarted tracking Codex limits.\n" + renderFreshness(notice.ObservedAt) + "\n\n" + renderAccount(notice.Account) + "\n\n" + renderLimitDetails(notice.Windows, grants, "current")
}

func RenderLimits(obs resetwatch.Observation) string {
	return "<b>Codex limits</b>\n" + renderFreshness(obs.ObservedAt) + "\n\n" + renderAccount(obs.Account) + "\n\n" + renderLimitDetails(obs.Windows, obs.ResetGrants, "current")
}

func RenderResetGrantDetails(obs resetwatch.Observation) string {
	var b strings.Builder
	b.WriteString("<b>Codex reset grants</b>\n")
	b.WriteString(renderFreshness(obs.ObservedAt))
	b.WriteString("\n")
	b.WriteString(renderAccount(obs.Account))

	available := len(obs.ResetGrants.Credits)
	if obs.ResetGrants.AvailableCount != nil {
		available = *obs.ResetGrants.AvailableCount
	}
	fmt.Fprintf(&b, "\n\n<b>%d available</b>", available)
	if len(obs.ResetGrants.Credits) == 0 {
		b.WriteString("\nNo available reset-grant details were returned by Codex.")
		return b.String()
	}

	for i, credit := range obs.ResetGrants.Credits {
		title := credit.Title
		if title == "" {
			title = "Reset grant"
		}
		rows := []string{}
		if credit.ResetType != "" {
			rows = append(rows, fmt.Sprintf("%-9s %s", "type", credit.ResetType))
		}
		if credit.Status != "" {
			rows = append(rows, fmt.Sprintf("%-9s %s", "status", credit.Status))
		}
		if !credit.GrantedAt.IsZero() {
			rows = append(rows, fmt.Sprintf("%-9s %s", "granted", formatGrantExpiry(credit.GrantedAt)))
		}
		if !credit.ExpiresAt.IsZero() {
			rows = append(rows, fmt.Sprintf("%-9s %s", "expires", formatGrantExpiry(credit.ExpiresAt)))
			if left := time.Until(credit.ExpiresAt); left > 0 {
				rows = append(rows, fmt.Sprintf("%-9s %s", "left", formatGrantTimeLeft(left)))
			}
		}
		if credit.ID != "" {
			rows = append(rows, fmt.Sprintf("%-9s %s", "id", credit.ID))
		}
		fmt.Fprintf(&b, "\n\n<b>%d. %s</b>\n<pre>%s</pre>", i+1, html.EscapeString(title), html.EscapeString(strings.Join(rows, "\n")))
	}
	return b.String()
}

func RenderProfile(profile remotecodex.ProfileResult) string {
	var b strings.Builder
	b.WriteString("<b>Codex profile</b>\n")
	if identity := profileIdentity(profile); identity != "" {
		b.WriteString("<b>")
		b.WriteString(html.EscapeString(identity))
		b.WriteString("</b>")
		if profile.Profile.Username != "" && profile.Profile.Username != identity {
			b.WriteString(" <code>@")
			b.WriteString(html.EscapeString(profile.Profile.Username))
			b.WriteString("</code>")
		}
		b.WriteString("\n")
	}
	if !profile.AuthState.OK {
		message := profile.AuthState.Error
		if message == "" {
			message = "profile unavailable"
		}
		b.WriteString("<code>")
		b.WriteString(html.EscapeString(message))
		b.WriteString("</code>")
		return b.String()
	}
	if freshness := renderProfileFreshness(profile.Metadata); freshness != "" {
		b.WriteString(freshness)
		b.WriteString("\n")
	}
	if profile.Metadata.StatsError != nil {
		b.WriteString("<code>stats error: ")
		b.WriteString(html.EscapeString(fmt.Sprint(profile.Metadata.StatsError)))
		b.WriteString("</code>\n")
	}
	stats := profile.Stats
	rows := []string{
		fmt.Sprintf("%-13s %s lifetime", "tokens", compactTokens(stats.LifetimeTokens)),
		fmt.Sprintf("%-13s %s", "peak day", compactTokens(stats.PeakDailyTokens)),
		fmt.Sprintf("%-13s %dd now · %dd best", "streak", stats.CurrentStreakDays, stats.LongestStreakDays),
		fmt.Sprintf("%-13s %s", "longest turn", humanDurationSeconds(stats.LongestRunningTurnSec)),
		fmt.Sprintf("%-13s %s · %s", "reasoning", emptyAsUnset(stats.MostUsedReasoningEffort), formatPercent1(stats.MostUsedReasoningEffortPct)),
		fmt.Sprintf("%-13s %s", "fast mode", formatPercent1(stats.FastModeUsagePercentage)),
		fmt.Sprintf("%-13s %s", "threads", humanInt(stats.TotalThreads)),
		fmt.Sprintf("%-13s %s uses · %s unique", "skills", humanInt(stats.TotalSkillsUsed), humanInt(stats.UniqueSkillsUsed)),
	}
	if workspace := workspaceRank(stats); workspace != "" {
		rows = append(rows, fmt.Sprintf("%-13s %s", "workspace", workspace))
	}
	b.WriteString("\n<b>Overview</b>\n<pre>")
	b.WriteString(html.EscapeString(strings.Join(rows, "\n")))
	b.WriteString("</pre>")
	if daily := renderProfileBuckets("Daily tokens", stats.DailyUsageBuckets, 7); daily != "" {
		b.WriteString("\n\n")
		b.WriteString(daily)
	}
	if weekly := renderProfileBuckets("Weekly tokens", stats.WeeklyUsageBuckets, 6); weekly != "" {
		b.WriteString("\n\n")
		b.WriteString(weekly)
	}
	if top := renderProfileTopInvocations(stats.TopInvocations, 5); top != "" {
		b.WriteString("\n\n")
		b.WriteString(top)
	}
	return b.String()
}

func RenderReset(event resetwatch.Event) string {
	var b strings.Builder
	b.WriteString("<b>Codex reset notification</b>\n")
	if joke := resetwatch.JokeText(event.JokeID); joke != "" {
		b.WriteString(html.EscapeString(joke))
		b.WriteString("\n")
	}
	b.WriteString(renderAccount(event.Account))
	b.WriteString("\n\n")
	b.WriteString("<b>Trigger</b>\n")
	b.WriteString("<pre>")
	b.WriteString(html.EscapeString(fmt.Sprintf("%-8s %s", "window", sectionLabel(event.PrimaryTriggerLabel))))
	b.WriteString("\n")
	b.WriteString(html.EscapeString(fmt.Sprintf("%-8s %s", "kind", event.ResetKind)))
	b.WriteString("\n")
	b.WriteString(html.EscapeString(fmt.Sprintf("%-8s %s", "before", formatTime(event.PreviousResetAt))))
	b.WriteString("\n")
	b.WriteString(html.EscapeString(fmt.Sprintf("%-8s %s", "after", formatTime(event.CurrentResetAt))))
	if !event.DetectedAt.IsZero() {
		b.WriteString("\n")
		b.WriteString(html.EscapeString(fmt.Sprintf("%-8s %s", "seen", formatFreshTime(event.DetectedAt))))
	}
	b.WriteString("</pre>")

	prev := windowsFromSnapshot(event.PreviousSnapshotJSON)
	current := windowsFromSnapshot(event.CurrentSnapshotJSON)
	if len(prev) > 0 || len(current) > 0 {
		b.WriteString("\n\n")
		b.WriteString(renderBeforeAfter(prev, current))
	}
	return b.String()
}

func RenderLimitWarning(warning resetwatch.WarningEvent) string {
	var b strings.Builder
	b.WriteString("<b>Codex limit warning</b>\n")
	b.WriteString(renderAccount(warning.Account))
	b.WriteString("\n\n")
	b.WriteString("<b>")
	b.WriteString(html.EscapeString(sectionLabel(warning.Label)))
	b.WriteString("</b>\n")
	b.WriteString("<pre>")
	b.WriteString(html.EscapeString(fmt.Sprintf("%-10s %s", "left", formatPercent(warning.RemainingPercent))))
	b.WriteString("\n")
	b.WriteString(html.EscapeString(fmt.Sprintf("%-10s %d%%", "checkpoint", warning.ThresholdRemaining)))
	b.WriteString("\n")
	b.WriteString(html.EscapeString(fmt.Sprintf("%-10s %s", "used", formatPercent(warning.UsedPercent))))
	b.WriteString("\n")
	b.WriteString(html.EscapeString(fmt.Sprintf("%-10s %s", "reset", formatTime(warning.ResetAt))))
	if !warning.DetectedAt.IsZero() {
		b.WriteString("\n")
		b.WriteString(html.EscapeString(fmt.Sprintf("%-10s %s", "seen", formatFreshTime(warning.DetectedAt))))
	}
	b.WriteString("</pre>")
	return b.String()
}

func RenderGrantExpiryWarning(warning resetwatch.GrantExpiryWarning) string {
	var b strings.Builder
	b.WriteString("<b>Codex reset grant expiry</b>\n")
	b.WriteString(renderAccount(warning.Account))
	b.WriteString("\n\n")
	rows := []string{
		fmt.Sprintf("%-10s %dd", "checkpoint", warning.ThresholdDays),
		fmt.Sprintf("%-10s %s", "expires", formatGrantExpiry(warning.ExpiresAt)),
	}
	if left := warning.ExpiresAt.Sub(warning.DetectedAt); !warning.DetectedAt.IsZero() && left > 0 {
		rows = append(rows, fmt.Sprintf("%-10s %s", "left", formatGrantTimeLeft(left)))
	}
	if warning.CreditTitle != "" {
		rows = append(rows, fmt.Sprintf("%-10s %s", "grant", warning.CreditTitle))
	}
	if warning.CreditID != "" {
		rows = append(rows, fmt.Sprintf("%-10s %s", "id", shortID(warning.CreditID)))
	}
	if !warning.DetectedAt.IsZero() {
		rows = append(rows, fmt.Sprintf("%-10s %s", "seen", formatFreshTime(warning.DetectedAt)))
	}
	b.WriteString("<pre>")
	b.WriteString(html.EscapeString(strings.Join(rows, "\n")))
	b.WriteString("</pre>")
	return b.String()
}

func RenderResetGrant(event resetwatch.ResetGrantEvent) string {
	var b strings.Builder
	b.WriteString("<b>Codex reset grant loaded</b>\n")
	b.WriteString("Tibo loaded a reset grant.\n")
	b.WriteString(renderAccount(event.Account))
	b.WriteString("\n\n")
	rows := []string{}
	if event.AvailableCount > 0 {
		rows = append(rows, fmt.Sprintf("%-10s %d", "available", event.AvailableCount))
	}
	if event.CreditTitle != "" {
		rows = append(rows, fmt.Sprintf("%-10s %s", "grant", event.CreditTitle))
	}
	if event.ResetType != "" {
		rows = append(rows, fmt.Sprintf("%-10s %s", "type", event.ResetType))
	}
	if !event.GrantedAt.IsZero() {
		rows = append(rows, fmt.Sprintf("%-10s %s", "granted", formatGrantExpiry(event.GrantedAt)))
	}
	if !event.ExpiresAt.IsZero() {
		rows = append(rows, fmt.Sprintf("%-10s %s", "expires", formatGrantExpiry(event.ExpiresAt)))
	}
	if event.CreditID != "" {
		rows = append(rows, fmt.Sprintf("%-10s %s", "id", shortID(event.CreditID)))
	}
	if !event.DetectedAt.IsZero() {
		rows = append(rows, fmt.Sprintf("%-10s %s", "seen", formatFreshTime(event.DetectedAt)))
	}
	b.WriteString("<pre>")
	b.WriteString(html.EscapeString(strings.Join(rows, "\n")))
	b.WriteString("</pre>")
	return b.String()
}

func RenderRadarProbability(alert radar.ProbabilityAlert) string {
	rows := []string{
		fmt.Sprintf("%-12s %d%%", "checkpoint", alert.Milestone),
		fmt.Sprintf("%-12s %.0f%%", "24h", alert.Probability24H*100),
		fmt.Sprintf("%-12s %.0f%%", "48h", alert.Probability48H*100),
	}
	if alert.Level != "" {
		rows = append(rows, fmt.Sprintf("%-12s %s", "level", alert.Level))
	}
	if window := radarExpectedWindow(alert.ExpectedWindow); window != "" {
		rows = append(rows, fmt.Sprintf("%-12s %s", "window", window))
	}
	if alert.CheckedAt != "" {
		rows = append(rows, fmt.Sprintf("%-12s %s", "checked", alert.CheckedAt))
	}
	if !alert.DetectedAt.IsZero() {
		rows = append(rows, fmt.Sprintf("%-12s %s", "seen", formatFreshTime(alert.DetectedAt)))
	}
	text := "<b>Codex reset radar alert</b>\n<pre>" + html.EscapeString(strings.Join(rows, "\n")) + "</pre>"
	text += "\n\n" + html.EscapeString(radarProbabilitySummary(alert))
	return text
}

func radarExpectedWindow(value string) string {
	switch strings.TrimSpace(value) {
	case "未来 24-48 小时":
		return "next 24-48h"
	default:
		return value
	}
}

func radarProbabilitySummary(alert radar.ProbabilityAlert) string {
	level := strings.TrimSpace(alert.Level)
	if level == "" {
		level = "unknown"
	}
	window := radarExpectedWindow(alert.ExpectedWindow)
	if window == "" {
		window = "the near term"
	}
	return fmt.Sprintf("Codex Radar estimates a %s reset chance for %s. This is a prediction signal, not an official reset confirmation.", level, window)
}

func RenderStats(stats server.Stats, environment string, telegramEnabled bool) string {
	var b strings.Builder
	b.WriteString("<b>Scriba stats</b>\n")
	b.WriteString(renderRuntimeStats(stats, environment, telegramEnabled))
	b.WriteString("\n\n")
	b.WriteString(renderHealthStats(stats.Health))
	if stats.Store.LatestObservation != nil {
		b.WriteString("\n\n")
		b.WriteString(renderObservationStats(*stats.Store.LatestObservation))
	}
	b.WriteString("\n\n")
	b.WriteString(renderStorageStats(stats.Store))
	b.WriteString("\n\n")
	b.WriteString(renderDeliveryStats("Reset deliveries", stats.Store.ResetDeliveries))
	b.WriteString("\n\n")
	b.WriteString(renderDeliveryStats("Warning deliveries", stats.Store.WarningDeliveries))
	b.WriteString("\n\n")
	b.WriteString(renderDeliveryStats("Grant warning deliveries", stats.Store.GrantWarningDeliveries))
	b.WriteString("\n\n")
	b.WriteString(renderDeliveryStats("Grant deliveries", stats.Store.GrantDeliveries))
	b.WriteString("\n\n")
	b.WriteString(renderDeliveryStats("Radar deliveries", stats.Store.RadarDeliveries))
	if stats.Store.LastReset != nil || stats.Store.LastWarning != nil || stats.Store.LastGrantWarning != nil || stats.Store.LastGrant != nil {
		b.WriteString("\n\n")
		b.WriteString(renderRecentStats(stats.Store))
	}
	return b.String()
}

func renderRuntimeStats(stats server.Stats, environment string, telegramEnabled bool) string {
	rows := []string{
		fmt.Sprintf("%-12s %s", "version", stats.Version),
		fmt.Sprintf("%-12s %s", "commit", stats.Commit),
		fmt.Sprintf("%-12s %s", "poll", server.FormatDuration(stats.PollInterval)),
		fmt.Sprintf("%-12s %dd", "retention", stats.ObservationRetentionDays),
	}
	if environment != "" {
		rows = append(rows, fmt.Sprintf("%-12s %s", "env", environment))
		rows = append(rows, fmt.Sprintf("%-12s %t", "telegram", telegramEnabled))
	}
	return "<pre>" + html.EscapeString(strings.Join(rows, "\n")) + "</pre>"
}

func RenderHealth(health server.Health) string {
	return "<b>Scriba health</b>\n" + renderHealthStats(health)
}

func RenderHealthNotice(notice server.HealthNotice) string {
	title := "<b>Scriba health alert</b>"
	if notice.Recovery {
		title = "<b>Scriba recovered</b>"
	}
	return title + "\n" + renderHealthStats(notice.Health)
}

func renderHealthStats(health server.Health) string {
	rows := []string{
		fmt.Sprintf("%-12s %s", "status", health.Status),
		fmt.Sprintf("%-12s %s", "version", health.Version),
		fmt.Sprintf("%-12s %s", "poll", server.FormatDuration(health.PollInterval)),
	}
	if health.LastSuccessAt != nil {
		rows = append(rows, fmt.Sprintf("%-12s %s", "last ok", formatFreshTime(*health.LastSuccessAt)))
	}
	if health.LastFailureAt != nil {
		rows = append(rows, fmt.Sprintf("%-12s %s", "last fail", formatFreshTime(*health.LastFailureAt)))
	}
	if health.NextPollEstimateAt != nil {
		rows = append(rows, fmt.Sprintf("%-12s %s", "next", formatFreshTime(*health.NextPollEstimateAt)))
	}
	rows = append(rows, fmt.Sprintf("%-12s %d", "failures", health.ConsecutiveFailures))
	if health.FailureKind != "" {
		rows = append(rows, fmt.Sprintf("%-12s %s", "kind", health.FailureKind))
	}
	if health.LastError != "" {
		rows = append(rows, fmt.Sprintf("%-12s %s", "error", truncate(health.LastError, 120)))
	}
	return "<b>Health</b>\n<pre>" + html.EscapeString(strings.Join(rows, "\n")) + "</pre>"
}

func renderObservationStats(latest store.ObservationSummary) string {
	rows := []string{
		fmt.Sprintf("%-12s %s", "latest", formatFreshTime(latest.ObservedAt)),
		fmt.Sprintf("%-12s %d", "latest win", latest.Windows),
		fmt.Sprintf("%-12s %s", "account", latest.AccountLabel),
	}
	if latest.AccountEmail != "" || latest.AccountPlan != "" {
		account := latest.AccountEmail
		if latest.AccountEmail != "" && latest.AccountPlan != "" {
			account += " · "
		}
		account += latest.AccountPlan
		rows = append(rows, fmt.Sprintf("%-12s %s", "plan", account))
	}
	return "<b>Observation</b>\n<pre>" + html.EscapeString(strings.Join(rows, "\n")) + "</pre>"
}

func profileIdentity(profile remotecodex.ProfileResult) string {
	if profile.Profile.DisplayName != "" {
		return profile.Profile.DisplayName
	}
	if profile.Profile.Username != "" {
		return profile.Profile.Username
	}
	return profile.AuthState.Email
}

func renderProfileFreshness(metadata remotecodex.ProfileMetadata) string {
	parts := []string{}
	if metadata.StatsAsOf != "" {
		parts = append(parts, "stats as of "+metadata.StatsAsOf)
	}
	if metadata.GeneratedAt != "" {
		parts = append(parts, "generated "+formatProfileTime(metadata.GeneratedAt))
	}
	if len(parts) == 0 {
		return ""
	}
	return "<i>" + html.EscapeString(strings.Join(parts, " · ")) + "</i>"
}

func renderProfileBuckets(title string, buckets []remotecodex.UsageBucket, limit int) string {
	if len(buckets) == 0 {
		return ""
	}
	if limit > 0 && len(buckets) > limit {
		buckets = buckets[len(buckets)-limit:]
	}
	var maxTokens int64
	for _, bucket := range buckets {
		if bucket.Tokens > maxTokens {
			maxTokens = bucket.Tokens
		}
	}
	rows := make([]string, 0, len(buckets))
	for _, bucket := range buckets {
		rows = append(rows, fmt.Sprintf("%-10s %s %8s", bucket.StartDate, tokenBar(bucket.Tokens, maxTokens, 10), compactTokens(bucket.Tokens)))
	}
	return "<b>" + html.EscapeString(title) + "</b>\n<pre>" + html.EscapeString(strings.Join(rows, "\n")) + "</pre>"
}

func renderProfileTopInvocations(invocations []remotecodex.Invocation, limit int) string {
	if len(invocations) == 0 {
		return ""
	}
	if limit > 0 && len(invocations) > limit {
		invocations = invocations[:limit]
	}
	rows := make([]string, 0, len(invocations))
	for i, invocation := range invocations {
		rows = append(rows, fmt.Sprintf("%d. %-22s %s", i+1, truncate(invocationName(invocation), 22), humanInt(invocation.UsageCount)))
	}
	return "<b>Top invocations</b>\n<pre>" + html.EscapeString(strings.Join(rows, "\n")) + "</pre>"
}

func tokenBar(tokens, maxTokens int64, width int) string {
	if width <= 0 {
		width = 10
	}
	if maxTokens <= 0 || tokens <= 0 {
		return strings.Repeat("▱", width)
	}
	filled := int((tokens*int64(width) + maxTokens - 1) / maxTokens)
	if filled < 1 {
		filled = 1
	}
	if filled > width {
		filled = width
	}
	return strings.Repeat("▰", filled) + strings.Repeat("▱", width-filled)
}

func workspaceRank(stats remotecodex.ProfileStats) string {
	if stats.WorkspaceRank == nil || stats.WorkspaceTotalUserCount == nil {
		return ""
	}
	return fmt.Sprintf("#%d of %d", *stats.WorkspaceRank, *stats.WorkspaceTotalUserCount)
}

func invocationName(invocation remotecodex.Invocation) string {
	switch {
	case invocation.SkillName != "":
		return invocation.SkillName
	case invocation.PluginName != "":
		return invocation.PluginName
	case invocation.SkillID != "":
		return invocation.SkillID
	case invocation.PluginID != "":
		return invocation.PluginID
	case invocation.Type != "":
		return invocation.Type
	default:
		return "unknown"
	}
}

func renderStorageStats(stats store.Stats) string {
	counts := stats.Counts
	rows := []string{
		fmt.Sprintf("%-13s %s", "db", formatBytes(stats.DBFiles.TotalBytes)),
		fmt.Sprintf("%-13s %s", "main", formatBytes(stats.DBFiles.MainBytes)),
		fmt.Sprintf("%-13s %s", "wal", formatBytes(stats.DBFiles.WALBytes)),
		fmt.Sprintf("%-13s %d", "accounts", counts["accounts"]),
		fmt.Sprintf("%-13s %d", "stored polls", counts["limit_observations"]),
		fmt.Sprintf("%-13s %d", "stored win", counts["observed_windows"]),
		fmt.Sprintf("%-13s %d", "tracked win", counts["limit_windows"]),
		fmt.Sprintf("%-13s %d", "resets", counts["reset_events"]),
		fmt.Sprintf("%-13s %d", "warnings", counts["limit_warning_events"]),
		fmt.Sprintf("%-13s %d", "grants", counts["reset_grant_events"]),
		fmt.Sprintf("%-13s %d", "grant warn", counts["reset_grant_warning_events"]),
		fmt.Sprintf("%-13s %d", "radar", counts["radar_alert_events"]),
	}
	return "<b>Storage</b>\n<pre>" + html.EscapeString(strings.Join(rows, "\n")) + "</pre>"
}

func renderDeliveryStats(title string, counts map[string]store.DeliveryCounts) string {
	rows := make([]string, 0, len(counts)+1)
	for _, status := range []string{"pending", "failed", "delivered"} {
		count := counts[status]
		rows = append(rows, fmt.Sprintf("%-10s %3d  attempts %d", status, count.Count, count.Attempts))
	}
	return "<b>" + html.EscapeString(title) + "</b>\n<pre>" + html.EscapeString(strings.Join(rows, "\n")) + "</pre>"
}

func renderRecentStats(stats store.Stats) string {
	rows := []string{}
	if stats.LastReset != nil {
		rows = append(rows, fmt.Sprintf("%-8s %s · %s · %s", "reset", sectionLabel(stats.LastReset.Trigger), stats.LastReset.Kind, formatFreshTime(stats.LastReset.DetectedAt)))
	}
	if stats.LastWarning != nil {
		rows = append(rows, fmt.Sprintf("%-8s %s · %d%% left · %s", "warning", sectionLabel(stats.LastWarning.Label), stats.LastWarning.ThresholdRemaining, formatFreshTime(stats.LastWarning.DetectedAt)))
	}
	if stats.LastGrantWarning != nil {
		rows = append(rows, fmt.Sprintf("%-8s %dd warning · %s · %s", "grant", stats.LastGrantWarning.ThresholdDays, formatGrantExpiry(stats.LastGrantWarning.ExpiresAt), formatFreshTime(stats.LastGrantWarning.DetectedAt)))
	}
	if stats.LastGrant != nil {
		rows = append(rows, fmt.Sprintf("%-8s %d available · %s · %s", "grant", stats.LastGrant.AvailableCount, formatGrantExpiry(stats.LastGrant.ExpiresAt), formatFreshTime(stats.LastGrant.DetectedAt)))
	}
	return "<b>Recent</b>\n<pre>" + html.EscapeString(strings.Join(rows, "\n")) + "</pre>"
}

func formatBytes(value int64) string {
	if value <= 0 {
		return "0 B"
	}
	return humanize.Bytes(uint64(value))
}

func compactTokens(value int64) string {
	negative := value < 0
	if negative {
		value = -value
	}
	var text string
	switch {
	case value >= 1_000_000_000:
		text = fmt.Sprintf("%.1fB", float64(value)/1_000_000_000)
	case value >= 1_000_000:
		text = fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case value >= 1_000:
		text = fmt.Sprintf("%.1fK", float64(value)/1_000)
	default:
		text = fmt.Sprint(value)
	}
	if negative {
		return "-" + text
	}
	return text
}

func humanInt(value int64) string {
	return humanize.Comma(value)
}

func humanDurationSeconds(seconds int64) string {
	if seconds <= 0 {
		return "unknown"
	}
	return time.Duration(seconds * int64(time.Second)).Round(time.Second).String()
}

func formatPercent1(value float64) string {
	return fmt.Sprintf("%.1f%%", value)
}

func emptyAsUnset(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func formatProfileTime(value string) string {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			if layout == "2006-01-02" {
				return parsed.Format("2006-01-02")
			}
			return parsed.Local().Format("2006-01-02 15:04 MST")
		}
	}
	return value
}

func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func renderFreshness(t time.Time) string {
	if t.IsZero() {
		return "<i>observed unknown</i>"
	}
	return "<i>observed " + html.EscapeString(formatFreshTime(t)) + "</i>"
}

func formatFreshTime(t time.Time) string {
	return humanize.Time(t) + " · " + t.Local().Format("Mon 15:04:05")
}

func formatPercent(value float64) string {
	return fmt.Sprintf("%.0f%%", value)
}

func renderAccount(account resetwatch.Account) string {
	label := account.Label
	if label == "" {
		label = "unknown"
	}
	var b strings.Builder
	b.WriteString("<b>Account</b> ")
	b.WriteString(html.EscapeString(label))
	if account.Email != "" || account.Plan != "" {
		b.WriteString("\n")
		b.WriteString("<code>")
		if account.Email != "" {
			b.WriteString(html.EscapeString(account.Email))
		}
		if account.Email != "" && account.Plan != "" {
			b.WriteString(" · ")
		}
		if account.Plan != "" {
			b.WriteString(html.EscapeString(account.Plan))
		}
		b.WriteString("</code>")
	}
	return b.String()
}

func renderWindows(windows []resetwatch.Window, rowLabel string) string {
	byLabel := map[string]resetwatch.Window{}
	for _, window := range windows {
		byLabel[window.Label] = window
	}
	primaryLabels := []string{resetwatch.LabelFiveHour, resetwatch.LabelWeeklyLimit}
	secondaryLabels := []string{resetwatch.LabelReviewFive, resetwatch.LabelReviewWeek}
	var b strings.Builder
	if section := renderWindowSection("Primary", primaryLabels, byLabel, rowLabel); section != "" {
		b.WriteString(section)
	}
	if section := renderWindowSection("Secondary", secondaryLabels, byLabel, rowLabel); section != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(section)
	}
	return b.String()
}

func renderLimitDetails(windows []resetwatch.Window, grants resetwatch.ResetGrants, rowLabel string) string {
	sections := []string{}
	if rendered := renderWindows(windows, rowLabel); rendered != "" {
		sections = append(sections, rendered)
	}
	if rendered := renderResetGrants(grants); rendered != "" {
		sections = append(sections, rendered)
	}
	return strings.Join(sections, "\n\n")
}

func renderResetGrants(grants resetwatch.ResetGrants) string {
	rows := []string{}
	if grants.AvailableCount != nil {
		rows = append(rows, fmt.Sprintf("%-9s %d", "available", *grants.AvailableCount))
	}
	if !grants.ExpiresAt.IsZero() {
		rows = append(rows, fmt.Sprintf("%-9s %s", "expires", formatGrantExpiry(grants.ExpiresAt)))
	}
	if len(rows) == 0 {
		return ""
	}
	return "<b>Reset grants</b>\n<pre>" + html.EscapeString(strings.Join(rows, "\n")) + "</pre>"
}

func renderBeforeAfter(prev, current []resetwatch.Window) string {
	labels := []string{resetwatch.LabelFiveHour, resetwatch.LabelWeeklyLimit, resetwatch.LabelReviewFive, resetwatch.LabelReviewWeek}
	prevByLabel := mapWindows(prev)
	currentByLabel := mapWindows(current)
	var b strings.Builder
	for _, label := range labels {
		before, beforeOK := prevByLabel[label]
		after, afterOK := currentByLabel[label]
		if !beforeOK && !afterOK {
			continue
		}
		b.WriteString("<b>")
		b.WriteString(html.EscapeString(sectionLabel(label)))
		b.WriteString("</b>\n")
		b.WriteString("<pre>")
		if beforeOK {
			b.WriteString(renderWindow("before", before, ""))
			b.WriteString("\n")
		}
		if afterOK {
			b.WriteString(renderWindow("after", after, ""))
			b.WriteString("\n")
		}
		b.WriteString("</pre>")
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderWindowSection(title string, labels []string, byLabel map[string]resetwatch.Window, rowLabel string) string {
	var rows []string
	for _, label := range labels {
		window, ok := byLabel[label]
		if !ok {
			continue
		}
		rows = append(rows, renderWindow(rowLabel, window, sectionLabel(label)))
	}
	if len(rows) == 0 {
		return ""
	}
	return "<b>" + html.EscapeString(title) + "</b>\n<pre>" + html.EscapeString(strings.Join(rows, "\n\n")) + "</pre>"
}

func renderWindow(label string, window resetwatch.Window, prefix string) string {
	percent := 0.0
	if window.UsedPercent != nil {
		percent = *window.UsedPercent
	}
	title := prefix
	if title == "" {
		title = label
	}
	return fmt.Sprintf("%-13s %s %3.0f%% used\n%-13s %s", title, bar(percent), percent, "reset", formatTime(window.ResetAt))
}

func sectionLabel(label string) string {
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

func mapWindows(windows []resetwatch.Window) map[string]resetwatch.Window {
	byLabel := make(map[string]resetwatch.Window, len(windows))
	for _, window := range windows {
		byLabel[window.Label] = window
	}
	return byLabel
}

func windowsFromSnapshot(snapshot []byte) []resetwatch.Window {
	if len(snapshot) == 0 {
		return nil
	}
	var result remote.ProbeResult
	if err := unmarshalSnapshot(snapshot, &result); err != nil {
		return nil
	}
	return resetwatch.FromMetricLines(result.Lines)
}

func unmarshalSnapshot(data []byte, target any) error {
	return json.Unmarshal(data, target)
}

func bar(percent float64) string {
	const width = 10
	filled := int(math.Ceil(percent / (100 / width)))
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return strings.Repeat("▰", filled) + strings.Repeat("▱", width-filled)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Local().Format("Mon 15:04")
}

func formatGrantExpiry(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func formatGrantTimeLeft(d time.Duration) string {
	d = d.Round(time.Hour)
	if d < 24*time.Hour {
		hours := int(math.Ceil(d.Hours()))
		if hours < 1 {
			hours = 1
		}
		return fmt.Sprintf("%dh", hours)
	}
	days := int(math.Ceil(d.Hours() / 24))
	return fmt.Sprintf("%dd", days)
}

func shortID(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
