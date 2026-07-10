package codex

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/agensfield/scriba/internal/local"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/pricing"
)

type entry struct {
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp string          `json:"timestamp"`
}

type rawUsage struct {
	InputTokens           int64
	CachedInputTokens     int64
	OutputTokens          int64
	ReasoningOutputTokens int64
	TotalTokens           int64
}

func Scan(paths []string) ([]model.LocalUsageEvent, model.ScannerStats, error) {
	stats := model.ScannerStats{}
	var events []model.LocalUsageEvent
	for _, dir := range pathsOrDefault(paths) {
		abs, _ := filepath.Abs(dir)
		if !local.IsDirectory(abs) {
			stats.MissingDirectories = append(stats.MissingDirectories, abs)
			continue
		}
		files, err := local.WalkJSONLFiles(abs)
		if err != nil {
			return events, stats, err
		}
		for _, file := range files {
			parsed, fileStats, err := ParseFile(abs, file)
			if err != nil {
				return events, stats, err
			}
			addStats(&stats, fileStats)
			events = append(events, parsed...)
		}
	}
	return events, stats, nil
}

func ParseFile(baseDir, filePath string) ([]model.LocalUsageEvent, model.ScannerStats, error) {
	stats := model.ScannerStats{Files: 1, Bytes: local.FileSize(filePath)}
	var events []model.LocalUsageEvent
	var previous *rawUsage
	currentModel := ""
	err := local.ReadJSONLLines(filePath, func(line []byte) {
		stats.Lines++
		if !strings.Contains(string(line), "token_count") && !strings.Contains(string(line), "turn_context") {
			return
		}
		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			stats.InvalidLines++
			return
		}
		if e.Type == "turn_context" {
			if modelName := extractModel(e.Payload); modelName != "" {
				currentModel = modelName
			}
			return
		}
		if e.Type != "event_msg" || e.Timestamp == "" {
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(e.Payload, &payload); err != nil || payload["type"] != "token_count" {
			return
		}
		info, _ := payload["info"].(map[string]any)
		lastUsage := parseRawUsage(info["last_token_usage"])
		totalUsage := parseRawUsage(info["total_token_usage"])
		var usage *rawUsage
		if lastUsage != nil {
			usage = lastUsage
		} else if totalUsage != nil {
			diff := subtract(*totalUsage, previous)
			usage = &diff
		}
		if totalUsage != nil {
			copy := *totalUsage
			previous = &copy
		}
		if usage == nil || usage.TotalTokens == 0 {
			return
		}
		normalizeUsage(usage)
		extractedModel := extractModelFromMap(map[string]any{"payload": payload, "info": info})
		fallback := extractedModel == "" && currentModel == ""
		modelName := extractedModel
		if modelName == "" {
			modelName = currentModel
		}
		if modelName == "" {
			modelName = "gpt-5"
		}
		currentModel = modelName
		cost, priced := pricing.Cost(modelName, pricing.Usage{
			InputTokens:           usage.InputTokens,
			CachedInputTokens:     usage.CachedInputTokens,
			OutputTokens:          usage.OutputTokens,
			ReasoningOutputTokens: usage.ReasoningOutputTokens,
		}, pricing.StandardSpeedMultiplier)
		var costUSD *float64
		if priced {
			costUSD = &cost
		}
		pricingState := "missing"
		if priced {
			pricingState = "calculated"
		}
		rel, _ := filepath.Rel(baseDir, filePath)
		rel = filepath.ToSlash(rel)
		events = append(events, model.LocalUsageEvent{
			ProviderID:            "codex",
			SessionID:             strings.TrimSuffix(rel, ".jsonl"),
			Timestamp:             e.Timestamp,
			Model:                 modelName,
			InputTokens:           usage.InputTokens,
			UncachedInputTokens:   usage.InputTokens - usage.CachedInputTokens,
			OutputTokens:          usage.OutputTokens,
			CacheCreationTokens:   0,
			CacheReadTokens:       usage.CachedInputTokens,
			CachedInputTokens:     usage.CachedInputTokens,
			ReasoningOutputTokens: usage.ReasoningOutputTokens,
			EffectiveTokens:       usage.InputTokens - usage.CachedInputTokens + usage.OutputTokens,
			TotalTokens:           usage.TotalTokens,
			CostUSD:               costUSD,
			PricingState:          pricingState,
			SourcePath:            filePath,
			Directory:             filepath.ToSlash(filepath.Dir(rel)),
			SessionFile:           filepath.Base(rel),
			IsFallbackModel:       fallback,
		})
		stats.Events++
	})
	return events, stats, err
}

func normalizeUsage(usage *rawUsage) {
	usage.InputTokens = max(usage.InputTokens, 0)
	usage.CachedInputTokens = min(max(usage.CachedInputTokens, 0), usage.InputTokens)
	usage.OutputTokens = max(usage.OutputTokens, 0)
	usage.ReasoningOutputTokens = min(max(usage.ReasoningOutputTokens, 0), usage.OutputTokens)
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
}

func pathsOrDefault(paths []string) []string {
	if len(paths) > 0 {
		return paths
	}
	return local.DefaultCodexSessionDirs()
}

func parseRawUsage(value any) *rawUsage {
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	input := number(record, "input_tokens")
	cached := number(record, "cached_input_tokens")
	if cached == 0 {
		cached = number(record, "cache_read_input_tokens")
	}
	output := number(record, "output_tokens")
	reasoning := number(record, "reasoning_output_tokens")
	total := number(record, "total_tokens")
	if total == 0 {
		total = input + output
	}
	return &rawUsage{InputTokens: input, CachedInputTokens: cached, OutputTokens: output, ReasoningOutputTokens: reasoning, TotalTokens: total}
}

func subtract(current rawUsage, previous *rawUsage) rawUsage {
	if previous == nil {
		return current
	}
	return rawUsage{
		InputTokens:           max(current.InputTokens-previous.InputTokens, 0),
		CachedInputTokens:     max(current.CachedInputTokens-previous.CachedInputTokens, 0),
		OutputTokens:          max(current.OutputTokens-previous.OutputTokens, 0),
		ReasoningOutputTokens: max(current.ReasoningOutputTokens-previous.ReasoningOutputTokens, 0),
		TotalTokens:           max(current.TotalTokens-previous.TotalTokens, 0),
	}
}

func number(record map[string]any, key string) int64 {
	if value, ok := record[key].(float64); ok {
		return int64(value)
	}
	return 0
}

func extractModel(raw json.RawMessage) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return extractModelValue(value)
}

func extractModelFromMap(record map[string]any) string {
	for _, value := range record {
		if modelName := extractModelValue(value); modelName != "" {
			return modelName
		}
	}
	return ""
}

func extractModelValue(value any) string {
	record, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"model", "model_name"} {
		if text, ok := record[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	for _, key := range []string{"info", "metadata"} {
		if modelName := extractModelValue(record[key]); modelName != "" {
			return modelName
		}
	}
	return ""
}

func addStats(target *model.ScannerStats, stats model.ScannerStats) {
	target.Files += stats.Files
	target.Bytes += stats.Bytes
	target.Lines += stats.Lines
	target.Events += stats.Events
	target.InvalidLines += stats.InvalidLines
	target.MissingDirectories = append(target.MissingDirectories, stats.MissingDirectories...)
}
