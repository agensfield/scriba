package render

import (
	"fmt"
	"math"
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
		for _, line := range provider.Lines {
			fmt.Fprintf(&b, "  %s\n", MetricLine(line))
		}
		for _, source := range provider.Provenance {
			if source.Error != "" {
				fmt.Fprintf(&b, "  %s %s\n", red("error"), source.Error)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func Report(title string, rows int) string {
	return fmt.Sprintf("%s · %d rows", title, rows)
}

func CodexLimits(lines []model.MetricLine, cached bool) string {
	var b strings.Builder
	suffix := "live"
	if cached {
		suffix = "cached"
	}
	fmt.Fprintf(&b, "%s\n%s\n", header("Codex Limits"), muted(suffix+" ChatGPT/Codex backend usage"))
	for _, line := range lines {
		fmt.Fprintf(&b, "  %s\n", MetricLine(line))
	}
	if len(lines) == 0 {
		fmt.Fprintf(&b, "  %s\n", yellow("no limit lines available"))
	}
	return strings.TrimRight(b.String(), "\n")
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
	switch line.Type {
	case "text":
		return fmt.Sprintf("%s %s", label(line.Label), value(fmt.Sprint(line.Value)))
	case "badge":
		return fmt.Sprintf("%s %s", label(line.Label), badge(line.Text))
	case "amount":
		return fmt.Sprintf("%s %s", label(line.Label), value(formatMetricValue(line.Value, line.Format)))
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
		return fmt.Sprintf("%s %s %s %s", label(line.Label), bar(percent), percentValue(percent), muted(details))
	default:
		return fmt.Sprintf("%s %s", label(line.Label), value(fmt.Sprint(line.Value)))
	}
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
