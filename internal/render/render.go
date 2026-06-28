package render

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/doctor"
	"github.com/agensfield/scriba/internal/model"
)

func Status(snapshot model.StatusSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s\n", header("Scriba Status"), muted("generated "+snapshot.GeneratedAt))
	for _, provider := range snapshot.Providers {
		fmt.Fprintf(&b, "\n%s\n", providerHeader(provider.DisplayName))
		lines := visibleMetricLines(provider.Lines)
		labelWidth := metricLabelWidth(lines)
		for _, line := range lines {
			fmt.Fprintf(&b, "  %s\n", metricLine(line, labelWidth))
		}
		for _, source := range provider.Provenance {
			if source.Error != "" {
				fmt.Fprintf(&b, "  %s %s\n", red("error"), humanError(source.Error))
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func Report(title string, rows any) string {
	switch typed := rows.(type) {
	case []model.DailyReportRow:
		return reportRows(title, "days", len(typed), typed, dailyReportLine)
	case []model.WeeklyReportRow:
		return reportRows(title, "weeks", len(typed), typed, weeklyReportLine)
	case []model.MonthlyReportRow:
		return reportRows(title, "months", len(typed), typed, monthlyReportLine)
	case []model.SessionReportRow:
		return reportRows(title, "sessions", len(typed), typed, sessionReportLine)
	case []model.BlockReportRow:
		return reportRows(title, "blocks", len(typed), typed, blockReportLine)
	default:
		return fmt.Sprintf("%s\n%s", header(title), yellow("no human renderer for this report; use --json"))
	}
}

func CodexLimits(lines []model.MetricLine, cached bool) string {
	var b strings.Builder
	suffix := "live"
	if cached {
		suffix = "cached"
	}
	fmt.Fprintf(&b, "%s\n%s\n", header("Codex Limits"), muted(suffix+" ChatGPT/Codex backend usage"))
	rendered := 0
	lines = visibleMetricLines(lines)
	labelWidth := metricLabelWidth(lines)
	for _, line := range lines {
		fmt.Fprintf(&b, "  %s\n", metricLine(line, labelWidth))
		rendered++
	}
	if rendered == 0 {
		fmt.Fprintf(&b, "  %s\n", yellow("no limit lines available"))
	}
	return strings.TrimRight(b.String(), "\n")
}

func showMetricLine(line model.MetricLine) bool {
	label := strings.ToLower(line.Label)
	return !strings.Contains(label, "spark")
}

func visibleMetricLines(lines []model.MetricLine) []model.MetricLine {
	var visible []model.MetricLine
	for _, line := range lines {
		if showMetricLine(line) {
			visible = append(visible, line)
		}
	}
	return visible
}

func metricLabelWidth(lines []model.MetricLine) int {
	width := 0
	for _, line := range lines {
		if len(line.Label) > width {
			width = len(line.Label)
		}
	}
	return width
}

func Doctor(state string) string {
	return "Scriba doctor: " + state
}

func DoctorPayload(payload doctor.Payload) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s %s\n", header("Scriba Doctor"), stateLabel(payload.State), muted("generated "+payload.GeneratedAt))
	fmt.Fprintf(&b, "\n%s\n", providerHeader("Cache"))
	fmt.Fprintf(&b, "  %s %s\n", label("State"), stateLabel(payload.Cache.State))
	fmt.Fprintf(&b, "  %s %s\n", label("Path"), payload.Cache.DatabasePath)
	fmt.Fprintf(&b, "  %s %s\n", label("Size"), formatBytes(payload.Cache.SizeBytes))
	fmt.Fprintf(&b, "  %s %s\n", label("Schema"), value(fmt.Sprint(payload.Cache.SchemaVersion)))
	wal := yellow("disabled")
	if payload.Cache.WALEnabled {
		wal = badge("enabled")
	}
	fmt.Fprintf(&b, "  %s %s\n", label("WAL"), wal)
	age := yellow("none")
	if payload.Cache.LatestSnapshotAgeMs != nil {
		age = value(duration(*payload.Cache.LatestSnapshotAgeMs))
	}
	fmt.Fprintf(&b, "  %s %s\n", label("Snapshot age"), age)
	for _, provider := range payload.Providers {
		fmt.Fprintf(&b, "\n%s\n", providerHeader(provider.DisplayName))
		fmt.Fprintf(&b, "  %s %s\n", label("State"), stateLabel(provider.State))
		for _, path := range provider.LocalPaths {
			state := yellow("missing")
			if path.Exists {
				state = badge("found")
			}
			fmt.Fprintf(&b, "  %s %s %s\n", label("Source"), state, path.Path)
		}
		var authPaths []string
		for _, path := range provider.Auth.Paths {
			if path.Exists {
				authPaths = append(authPaths, path.Path)
			}
		}
		if len(authPaths) > 0 {
			fmt.Fprintf(&b, "  %s %s %s\n", label("Auth"), badge("found"), strings.Join(authPaths, ", "))
		} else {
			fmt.Fprintf(&b, "  %s %s %s\n", label("Auth"), yellow("missing"), provider.Auth.Hint)
		}
		remote := blue("skipped")
		if provider.Remote.State == "ok" {
			remote = badge("ok")
		} else if provider.Remote.State != "skipped" {
			if provider.Remote.Error != "" {
				remote = yellow(provider.Remote.Error)
			} else {
				remote = yellow(provider.Remote.State)
			}
		}
		fmt.Fprintf(&b, "  %s %s\n", label("Remote"), remote)
	}
	return strings.TrimRight(b.String(), "\n")
}

func MetricLine(line model.MetricLine) string {
	return metricLine(line, 0)
}

func metricLine(line model.MetricLine, labelWidth int) string {
	switch line.Type {
	case "text":
		return fmt.Sprintf("%s %s", metricLabel(line.Label, labelWidth), value(formatMetricText(line)))
	case "badge":
		return fmt.Sprintf("%s %s", metricLabel(line.Label, labelWidth), badge(line.Text))
	case "amount":
		return fmt.Sprintf("%s %s", metricLabel(line.Label, labelWidth), value(formatMetricValue(line.Value, line.Format)))
	case "progress":
		used := 0.0
		limit := 100.0
		if line.Used != nil {
			used = *line.Used
		}
		if line.Limit != nil && *line.Limit != 0 {
			limit = *line.Limit
		}
		percent := int(math.Round(used / limit * 100))
		details := "used"
		if line.ResetsAt != "" {
			details += " · " + resetLabel(line.ResetsAt)
		}
		return fmt.Sprintf("%s %s %s %s", metricLabel(line.Label, labelWidth), bar(percent), percentValue(percent), muted(details))
	default:
		return fmt.Sprintf("%s %s", metricLabel(line.Label, labelWidth), value(fmt.Sprint(line.Value)))
	}
}

func formatMetricText(line model.MetricLine) string {
	text := fmt.Sprint(line.Value)
	if strings.EqualFold(line.Label, "Grant expiry") {
		return formatTimeLocal(text)
	}
	return humanError(text)
}

func reportRows[T any](title, noun string, total int, rows []T, renderLine func(T) string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", header(title))
	if total == 0 {
		fmt.Fprintf(&b, "%s\n", yellow("no usage rows found"))
		return strings.TrimRight(b.String(), "\n")
	}
	limit := total
	if limit > 10 {
		limit = 10
	}
	summary := fmt.Sprintf("%d %s", total, noun)
	if total > limit {
		summary += fmt.Sprintf(" · showing latest %d", limit)
	}
	fmt.Fprintf(&b, "%s\n\n", muted(summary))
	for i := 0; i < limit; i++ {
		line := renderLine(rows[i])
		if line == "" {
			continue
		}
		fmt.Fprintf(&b, "%s %s\n", muted(fmt.Sprintf("%2d.", i+1)), line)
	}
	return strings.TrimRight(b.String(), "\n")
}

func dailyReportLine(row model.DailyReportRow) string {
	return periodReportLine(row.Date, row.ReportTotals, row.Models)
}

func weeklyReportLine(row model.WeeklyReportRow) string {
	return periodReportLine("week of "+row.Week, row.ReportTotals, row.Models)
}

func monthlyReportLine(row model.MonthlyReportRow) string {
	return periodReportLine(row.Month, row.ReportTotals, row.Models)
}

func sessionReportLine(row model.SessionReportRow) string {
	name, includeID := sessionLabel(row)
	id := shortID(row.SessionID)
	if includeID && id != "" && id != name {
		name += " · " + id
	}
	return fmt.Sprintf("%-18s %s", formatReportTime(row.LastActivity), totalsSummary(row.ReportTotals, row.Models, name))
}

func sessionLabel(row model.SessionReportRow) (string, bool) {
	if row.ProjectPath != "" {
		return filepath.Base(strings.TrimRight(row.ProjectPath, "/")), true
	}
	if row.Directory != "" && !looksLikeDateDirectory(row.Directory) {
		return filepath.Base(strings.TrimRight(row.Directory, "/")), true
	}
	if row.SessionFile != "" {
		return cleanSessionFile(row.SessionFile), false
	}
	if row.Directory != "" {
		return row.Directory, false
	}
	return row.SessionID, false
}

func looksLikeDateDirectory(value string) bool {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	if len(parts) != 3 {
		return false
	}
	return len(parts[0]) == 4 && len(parts[1]) == 2 && len(parts[2]) == 2
}

func cleanSessionFile(value string) string {
	name := filepath.Base(strings.TrimRight(value, "/"))
	name = strings.TrimSuffix(name, ".jsonl")
	re := regexp.MustCompile(`^rollout-(\d{4}-\d{2}-\d{2})T(\d{2})-(\d{2})-\d{2}-([0-9a-f]{8})`)
	if match := re.FindStringSubmatch(name); match != nil {
		return fmt.Sprintf("rollout %s %s:%s · %s", match[1], match[2], match[3], match[4])
	}
	return name
}

func blockReportLine(row model.BlockReportRow) string {
	label := formatReportTime(row.StartTime)
	if row.IsActive {
		label += " active"
	}
	return fmt.Sprintf("%-18s %s · %d entries", label, totalsSummary(row.ReportTotals, row.Models, ""), row.Entries)
}

func periodReportLine(period string, totals model.ReportTotals, models []model.ModelBreakdown) string {
	return fmt.Sprintf("%-18s %s", period, totalsSummary(totals, models, ""))
}

func totalsSummary(totals model.ReportTotals, models []model.ModelBreakdown, suffix string) string {
	parts := []string{
		value(fmt.Sprintf("%s tokens", compact(float64(totals.TotalTokens)))),
		muted(fmt.Sprintf("in %s", compact(float64(totals.InputTokens)))),
		muted(fmt.Sprintf("out %s", compact(float64(totals.OutputTokens)))),
	}
	if totals.CostUSD != nil {
		parts = append(parts, green(formatCost(*totals.CostUSD)))
	}
	if model := topModel(models); model != "" {
		parts = append(parts, cyan(shortModel(model)))
	}
	if suffix != "" {
		parts = append(parts, muted(suffix))
	}
	return strings.Join(parts, " · ")
}

func topModel(models []model.ModelBreakdown) string {
	for _, model := range models {
		if strings.TrimSpace(model.Model) != "" {
			return model.Model
		}
	}
	return ""
}

func shortModel(model string) string {
	if len(model) <= 36 {
		return model
	}
	return model[:33] + "..."
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if len(id) <= 12 {
		return id
	}
	return id[:8]
}

func formatCost(cost float64) string {
	if cost < 1 {
		return fmt.Sprintf("$%.4f", cost)
	}
	if cost < 10 {
		return fmt.Sprintf("$%.2f", cost)
	}
	return fmt.Sprintf("$%.0f", cost)
}

func formatReportTime(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, strings.TrimSpace(value))
	}
	if err != nil {
		if len(value) > 16 {
			return value[:16]
		}
		return value
	}
	return parsed.Local().Format("Jan 2 15:04")
}

func metricLabel(text string, width int) string {
	pad := width - len(text)
	if pad < 0 {
		pad = 0
	}
	return label(text) + strings.Repeat(" ", pad)
}

func formatMetricValue(input any, format *model.MetricFormat) string {
	value, ok := input.(float64)
	if !ok {
		return fmt.Sprint(input)
	}
	if format == nil {
		return compact(value)
	}
	switch format.Kind {
	case "percent":
		return fmt.Sprintf("%.0f%%", value)
	case "dollars":
		if value < 10 {
			return fmt.Sprintf("$%.2f", value)
		}
		return fmt.Sprintf("$%.0f", value)
	default:
		if format.Suffix != "" {
			return compact(value) + " " + format.Suffix
		}
		return compact(value)
	}
}

func header(text string) string         { return cyanBright(text) }
func providerHeader(text string) string { return bold(text) }
func muted(text string) string          { return blue(text) }
func label(text string) string          { return cyan(text) }
func value(text string) string          { return text }
func badge(text string) string          { return green(text) }
func red(text string) string            { return ansi("31;1", text) }
func yellow(text string) string         { return ansi("33;1", text) }
func green(text string) string          { return ansi("32;1", text) }
func blue(text string) string           { return ansi("94", text) }
func cyan(text string) string           { return ansi("36", text) }
func cyanBright(text string) string     { return ansi("96;1", text) }
func bold(text string) string           { return ansi("1", text) }

func ansi(code, text string) string {
	if text == "" {
		return ""
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func stateLabel(state string) string {
	switch state {
	case "ok":
		return badge("ok")
	case "broken":
		return red("broken")
	case "skipped":
		return blue("skipped")
	default:
		return yellow(state)
	}
}

func bar(percent int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	width := 20
	filled := int(math.Round(float64(percent) / 5))
	color := green
	if percent >= 90 {
		color = red
	} else if percent >= 70 {
		color = yellow
	}
	return color(strings.Repeat("▰", filled)) + strings.Repeat("▱", width-filled)
}

func percentValue(percent int) string {
	if percent >= 90 {
		return red(fmt.Sprintf("%d%%", percent))
	}
	if percent >= 70 {
		return yellow(fmt.Sprintf("%d%%", percent))
	}
	return value(fmt.Sprintf("%d%%", percent))
}

func compact(value float64) string {
	abs := math.Abs(value)
	switch {
	case abs >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", value/1_000_000_000)
	case abs >= 1_000_000:
		return fmt.Sprintf("%.1fM", value/1_000_000)
	case abs >= 1_000:
		return fmt.Sprintf("%.1fK", value/1_000)
	default:
		return fmt.Sprintf("%.0f", value)
	}
}

func formatBytes(value int64) string {
	v := float64(value)
	switch {
	case value >= 1_000_000_000:
		return fmt.Sprintf("%.1f GB", v/1_000_000_000)
	case value >= 1_000_000:
		return fmt.Sprintf("%.1f MB", v/1_000_000)
	case value >= 1_000:
		return fmt.Sprintf("%.1f KB", v/1_000)
	default:
		return fmt.Sprintf("%d B", value)
	}
}

func formatTimeLocal(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, strings.TrimSpace(value))
	}
	if err != nil {
		return value
	}
	return parsed.Local().Format("2006-01-02 15:04 MST")
}

func humanError(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return value
	}
	start := strings.Index(trimmed, "{")
	if start < 0 {
		return value
	}
	var payload struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		Message          string `json:"message"`
	}
	if err := json.Unmarshal([]byte(trimmed[start:]), &payload); err != nil {
		return value
	}
	detail := payload.ErrorDescription
	if detail == "" {
		detail = payload.Message
	}
	if detail == "" {
		detail = payload.Error
	}
	if detail == "" {
		return value
	}
	prefix := strings.TrimSpace(trimmed[:start])
	prefix = strings.TrimSuffix(prefix, ":")
	if strings.Contains(strings.ToLower(prefix), "refresh failed") {
		return "OAuth refresh failed: " + detail
	}
	if prefix == "" {
		return detail
	}
	return prefix + ": " + detail
}

func duration(ms int64) string {
	seconds := int64(math.Round(float64(ms) / 1000))
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := int64(math.Round(float64(seconds) / 60))
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	hours := int64(math.Round(float64(minutes) / 60))
	if hours < 24 {
		return fmt.Sprintf("%dh", hours)
	}
	days := hours / 24
	remainder := hours % 24
	if remainder == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd %dh", days, remainder)
}

func resetLabel(resetsAt string) string {
	reset, err := time.Parse(time.RFC3339Nano, resetsAt)
	if err != nil {
		reset, err = time.Parse(time.RFC3339, resetsAt)
	}
	if err != nil {
		return "resets " + resetsAt
	}
	absolute := reset.Local().Format("Jan 2 15:04")
	delta := time.Until(reset)
	if delta <= 0 {
		return "reset due " + absolute
	}
	return "resets in " + duration(delta.Milliseconds()) + " (" + absolute + ")"
}
