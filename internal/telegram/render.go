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
	b.WriteString(renderDeliveryStats("Radar deliveries", stats.Store.RadarDeliveries))
	if stats.Store.LastReset != nil || stats.Store.LastWarning != nil {
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
	return "<b>Recent</b>\n<pre>" + html.EscapeString(strings.Join(rows, "\n")) + "</pre>"
}

func formatBytes(value int64) string {
	if value <= 0 {
		return "0 B"
	}
	return humanize.Bytes(uint64(value))
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
