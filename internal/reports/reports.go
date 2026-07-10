package reports

import (
	"sort"
	"time"

	"github.com/agensfield/scriba/internal/model"
)

type Filters struct {
	Since string
	Until string
}

func Location(name string) (*time.Location, error) {
	if name == "" || name == "local" {
		return time.Local, nil
	}
	return time.LoadLocation(name)
}

func ApplyFilters(events []model.LocalUsageEvent, filters Filters) []model.LocalUsageEvent {
	return ApplyFiltersIn(events, filters, time.UTC)
}

func ApplyFiltersIn(events []model.LocalUsageEvent, filters Filters, location *time.Location) []model.LocalUsageEvent {
	since := normalizeBoundary(filters.Since, false, location)
	until := normalizeBoundary(filters.Until, true, location)
	out := events[:0]
	for _, event := range events {
		if since != "" && event.Timestamp < since {
			continue
		}
		if until != "" && event.Timestamp > until {
			continue
		}
		out = append(out, event)
	}
	return out
}

func Daily(events []model.LocalUsageEvent, orderDesc bool) []model.DailyReportRow {
	return DailyIn(events, orderDesc, time.UTC)
}

func DailyIn(events []model.LocalUsageEvent, orderDesc bool, location *time.Location) []model.DailyReportRow {
	rows := map[string]*model.DailyReportRow{}
	for _, event := range events {
		key := dateKey(event.Timestamp, "2006-01-02", location)
		row := rows[key]
		if row == nil {
			row = &model.DailyReportRow{Date: key, ProviderID: event.ProviderID}
			rows[key] = row
		}
		addEvent(&row.ReportTotals, &row.Models, event)
	}
	out := make([]model.DailyReportRow, 0, len(rows))
	for _, row := range rows {
		sortModels(row.Models)
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if orderDesc {
			return out[i].Date > out[j].Date
		}
		return out[i].Date < out[j].Date
	})
	return out
}

func Weekly(events []model.LocalUsageEvent, orderDesc bool) []model.WeeklyReportRow {
	return WeeklyIn(events, orderDesc, time.UTC)
}

func WeeklyIn(events []model.LocalUsageEvent, orderDesc bool, location *time.Location) []model.WeeklyReportRow {
	rows := map[string]*model.WeeklyReportRow{}
	for _, event := range events {
		key := weekKey(event.Timestamp, location)
		row := rows[key]
		if row == nil {
			row = &model.WeeklyReportRow{Week: key, ProviderID: event.ProviderID}
			rows[key] = row
		}
		addEvent(&row.ReportTotals, &row.Models, event)
	}
	out := make([]model.WeeklyReportRow, 0, len(rows))
	for _, row := range rows {
		sortModels(row.Models)
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if orderDesc {
			return out[i].Week > out[j].Week
		}
		return out[i].Week < out[j].Week
	})
	return out
}

func Monthly(events []model.LocalUsageEvent, orderDesc bool) []model.MonthlyReportRow {
	return MonthlyIn(events, orderDesc, time.UTC)
}

func MonthlyIn(events []model.LocalUsageEvent, orderDesc bool, location *time.Location) []model.MonthlyReportRow {
	rows := map[string]*model.MonthlyReportRow{}
	for _, event := range events {
		key := dateKey(event.Timestamp, "2006-01", location)
		row := rows[key]
		if row == nil {
			row = &model.MonthlyReportRow{Month: key, ProviderID: event.ProviderID}
			rows[key] = row
		}
		addEvent(&row.ReportTotals, &row.Models, event)
	}
	out := make([]model.MonthlyReportRow, 0, len(rows))
	for _, row := range rows {
		sortModels(row.Models)
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if orderDesc {
			return out[i].Month > out[j].Month
		}
		return out[i].Month < out[j].Month
	})
	return out
}

func Sessions(events []model.LocalUsageEvent, orderDesc bool) []model.SessionReportRow {
	rows := map[string]*model.SessionReportRow{}
	for _, event := range events {
		row := rows[event.SessionID]
		if row == nil {
			row = &model.SessionReportRow{
				SessionID:   event.SessionID,
				ProviderID:  event.ProviderID,
				ProjectPath: event.ProjectPath,
				Directory:   event.Directory,
				SessionFile: event.SessionFile,
			}
			rows[event.SessionID] = row
		}
		if event.Timestamp > row.LastActivity {
			row.LastActivity = event.Timestamp
		}
		addEvent(&row.ReportTotals, &row.Models, event)
	}
	out := make([]model.SessionReportRow, 0, len(rows))
	for _, row := range rows {
		sortModels(row.Models)
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if orderDesc {
			return out[i].LastActivity > out[j].LastActivity
		}
		return out[i].LastActivity < out[j].LastActivity
	})
	return out
}

func Blocks(events []model.LocalUsageEvent) []model.BlockReportRow {
	sort.Slice(events, func(i, j int) bool { return events[i].Timestamp < events[j].Timestamp })
	var rows []model.BlockReportRow
	var current *model.BlockReportRow
	for _, event := range events {
		t, err := time.Parse(time.RFC3339Nano, event.Timestamp)
		if err != nil {
			continue
		}
		if current == nil || t.Sub(parseTime(current.StartTime)) >= 5*time.Hour {
			end := t.Add(5 * time.Hour)
			rows = append(rows, model.BlockReportRow{
				ID:         event.Timestamp,
				ProviderID: "claude",
				StartTime:  event.Timestamp,
				EndTime:    end.UTC().Format(time.RFC3339Nano),
			})
			current = &rows[len(rows)-1]
		}
		current.ActualEndTime = event.Timestamp
		current.Entries++
		addEvent(&current.ReportTotals, &current.Models, event)
	}
	for i := range rows {
		sortModels(rows[i].Models)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].StartTime > rows[j].StartTime })
	return rows
}

func addEvent(totals *model.ReportTotals, models *[]model.ModelBreakdown, event model.LocalUsageEvent) {
	totals.InputTokens += event.InputTokens
	totals.UncachedInputTokens += event.UncachedInputTokens
	totals.OutputTokens += event.OutputTokens
	totals.CacheCreationTokens += event.CacheCreationTokens
	totals.CacheReadTokens += event.CacheReadTokens
	totals.CachedInputTokens += event.CachedInputTokens
	totals.ReasoningOutputTokens += event.ReasoningOutputTokens
	totals.EffectiveTokens += event.EffectiveTokens
	totals.TotalTokens += event.TotalTokens
	if event.CostUSD != nil {
		if totals.CostUSD == nil {
			v := 0.0
			totals.CostUSD = &v
		}
		*totals.CostUSD += *event.CostUSD
	}
	for i := range *models {
		if (*models)[i].Model == event.Model {
			addModel(&(*models)[i], event)
			return
		}
	}
	next := model.ModelBreakdown{Model: event.Model, PricingState: "missing"}
	addModel(&next, event)
	*models = append(*models, next)
}

func addModel(target *model.ModelBreakdown, event model.LocalUsageEvent) {
	target.InputTokens += event.InputTokens
	target.UncachedInputTokens += event.UncachedInputTokens
	target.OutputTokens += event.OutputTokens
	target.CacheCreationTokens += event.CacheCreationTokens
	target.CacheReadTokens += event.CacheReadTokens
	target.CachedInputTokens += event.CachedInputTokens
	target.ReasoningOutputTokens += event.ReasoningOutputTokens
	target.EffectiveTokens += event.EffectiveTokens
	target.TotalTokens += event.TotalTokens
	if event.CostUSD != nil {
		if target.CostUSD == nil {
			v := 0.0
			target.CostUSD = &v
		}
		*target.CostUSD += *event.CostUSD
		target.PricingState = "embedded"
	}
}

func sortModels(models []model.ModelBreakdown) {
	sort.Slice(models, func(i, j int) bool { return models[i].TotalTokens > models[j].TotalTokens })
}

func normalizeBoundary(value string, endOfDay bool, location *time.Location) string {
	if len(value) == 10 && value[4] == '-' && value[7] == '-' {
		boundary := "00:00:00.000000000"
		if endOfDay {
			boundary = "23:59:59.999999999"
		}
		parsed, err := time.ParseInLocation("2006-01-02T15:04:05.999999999", value+"T"+boundary, location)
		if err == nil {
			return parsed.UTC().Format(time.RFC3339Nano)
		}
	}
	return value
}

func weekKey(timestamp string, location *time.Location) string {
	t, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		if len(timestamp) >= 10 {
			return timestamp[:10]
		}
		return timestamp
	}
	t = t.In(location)
	weekday := int(t.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	start := t.AddDate(0, 0, -(weekday - 1))
	return start.Format("2006-01-02")
}

func dateKey(timestamp, layout string, location *time.Location) string {
	t, err := time.Parse(time.RFC3339Nano, timestamp)
	if err == nil {
		return t.In(location).Format(layout)
	}
	if len(timestamp) >= len(layout) {
		return timestamp[:len(layout)]
	}
	return timestamp
}

func parseTime(value string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, value)
	return t
}
