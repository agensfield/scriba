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

func ApplyFilters(events []model.LocalUsageEvent, filters Filters) []model.LocalUsageEvent {
	since := normalizeBoundary(filters.Since, false)
	until := normalizeBoundary(filters.Until, true)
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
	rows := map[string]*model.DailyReportRow{}
	for _, event := range events {
		key := event.Timestamp
		if len(key) >= 10 {
			key = key[:10]
		}
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
	rows := map[string]*model.WeeklyReportRow{}
	for _, event := range events {
		key := weekKey(event.Timestamp)
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
	rows := map[string]*model.MonthlyReportRow{}
	for _, event := range events {
		key := event.Timestamp
		if len(key) >= 7 {
			key = key[:7]
		}
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
	totals.OutputTokens += event.OutputTokens
	totals.CacheCreationTokens += event.CacheCreationTokens
	totals.CacheReadTokens += event.CacheReadTokens
	totals.CachedInputTokens += event.CachedInputTokens
	totals.ReasoningOutputTokens += event.ReasoningOutputTokens
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
	target.OutputTokens += event.OutputTokens
	target.CacheCreationTokens += event.CacheCreationTokens
	target.CacheReadTokens += event.CacheReadTokens
	target.CachedInputTokens += event.CachedInputTokens
	target.ReasoningOutputTokens += event.ReasoningOutputTokens
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

func normalizeBoundary(value string, endOfDay bool) string {
	if len(value) == 10 && value[4] == '-' && value[7] == '-' {
		if endOfDay {
			return value + "T23:59:59.999Z"
		}
		return value + "T00:00:00.000Z"
	}
	return value
}

func weekKey(timestamp string) string {
	t, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		if len(timestamp) >= 10 {
			return timestamp[:10]
		}
		return timestamp
	}
	weekday := int(t.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	start := t.AddDate(0, 0, -(weekday - 1))
	return start.Format("2006-01-02")
}

func parseTime(value string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, value)
	return t
}
