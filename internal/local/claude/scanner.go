package claude

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/agensfield/scriba/internal/local"
	"github.com/agensfield/scriba/internal/model"
)

type usageRecord struct {
	Cwd               string   `json:"cwd"`
	SessionID         string   `json:"sessionId"`
	Timestamp         string   `json:"timestamp"`
	RequestID         string   `json:"requestId"`
	CostUSD           *float64 `json:"costUSD"`
	IsAPIErrorMessage bool     `json:"isApiErrorMessage"`
	Message           struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func Scan(paths []string) ([]model.LocalUsageEvent, model.ScannerStats, error) {
	stats := model.ScannerStats{}
	var events []model.LocalUsageEvent
	seen := map[string]struct{}{}
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
			for _, event := range parsed {
				if event.UniqueKey != "" {
					if _, ok := seen[event.UniqueKey]; ok {
						stats.Duplicates++
						continue
					}
					seen[event.UniqueKey] = struct{}{}
				}
				stats.Events++
				events = append(events, event)
			}
		}
	}
	return events, stats, nil
}

func ParseFile(projectsDir, filePath string) ([]model.LocalUsageEvent, model.ScannerStats, error) {
	stats := model.ScannerStats{Files: 1, Bytes: local.FileSize(filePath)}
	var events []model.LocalUsageEvent
	err := local.ReadJSONLLines(filePath, func(line []byte) {
		stats.Lines++
		var rec usageRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			stats.InvalidLines++
			return
		}
		if rec.Timestamp == "" || rec.Message.Usage.InputTokens+rec.Message.Usage.OutputTokens+rec.Message.Usage.CacheCreationInputTokens+rec.Message.Usage.CacheReadInputTokens == 0 {
			return
		}
		uniqueKey := ""
		if rec.Message.ID != "" && rec.RequestID != "" {
			uniqueKey = rec.Message.ID + ":" + rec.RequestID
		}
		sessionID := rec.SessionID
		if sessionID == "" {
			sessionID = sessionFromPath(projectsDir, filePath)
		}
		modelName := rec.Message.Model
		if modelName == "" {
			modelName = "unknown"
		}
		usage := rec.Message.Usage
		cacheRead := usage.CacheReadInputTokens
		events = append(events, model.LocalUsageEvent{
			ProviderID:            "claude",
			SessionID:             sessionID,
			Timestamp:             rec.Timestamp,
			Model:                 modelName,
			Project:               projectFromPath(projectsDir, filePath),
			ProjectPath:           rec.Cwd,
			InputTokens:           usage.InputTokens,
			OutputTokens:          usage.OutputTokens,
			CacheCreationTokens:   usage.CacheCreationInputTokens,
			CacheReadTokens:       cacheRead,
			CachedInputTokens:     cacheRead,
			ReasoningOutputTokens: 0,
			TotalTokens:           usage.InputTokens + usage.OutputTokens + usage.CacheCreationInputTokens + cacheRead,
			CostUSD:               rec.CostUSD,
			UniqueKey:             uniqueKey,
			SourcePath:            filePath,
		})
	})
	return events, stats, err
}

func pathsOrDefault(paths []string) []string {
	if len(paths) > 0 {
		return paths
	}
	return local.DefaultClaudeProjectDirs()
}

func projectFromPath(projectsDir, filePath string) string {
	rel, err := filepath.Rel(projectsDir, filePath)
	if err != nil {
		return ""
	}
	parts := strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 || parts[0] == "." {
		return ""
	}
	return parts[0]
}

func sessionFromPath(projectsDir, filePath string) string {
	rel, err := filepath.Rel(projectsDir, filePath)
	if err != nil {
		return strings.TrimSuffix(filepath.Base(filePath), ".jsonl")
	}
	rel = filepath.ToSlash(strings.TrimSuffix(rel, ".jsonl"))
	return rel
}

func addStats(target *model.ScannerStats, stats model.ScannerStats) {
	target.Files += stats.Files
	target.Bytes += stats.Bytes
	target.Lines += stats.Lines
	target.InvalidLines += stats.InvalidLines
	target.MissingDirectories = append(target.MissingDirectories, stats.MissingDirectories...)
}
