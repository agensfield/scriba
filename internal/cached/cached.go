package cached

import (
	"path/filepath"

	"github.com/agensfield/scriba/internal/cache"
	"github.com/agensfield/scriba/internal/local"
	"github.com/agensfield/scriba/internal/local/claude"
	"github.com/agensfield/scriba/internal/local/codex"
	"github.com/agensfield/scriba/internal/model"
)

func ScanClaude(c *cache.Cache, paths []string) ([]model.LocalUsageEvent, model.ScannerStats, error) {
	stats := model.ScannerStats{}
	var events []model.LocalUsageEvent
	seen := map[string]struct{}{}
	for _, dir := range pathsOr(paths, local.DefaultClaudeProjectDirs()) {
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
			fp, err := local.FileFingerprint(file)
			if err != nil {
				return events, stats, err
			}
			parsed, fileStats, ok, err := c.LoadFileEvents("claude", file, fp.Size, fp.MtimeMs)
			if err != nil {
				return events, stats, err
			}
			if !ok {
				parsed, fileStats, err = claude.ParseFile(abs, file)
				if err != nil {
					return events, stats, err
				}
				if err := c.SaveFileEvents("claude", file, fp.Size, fp.MtimeMs, parsed, fileStats); err != nil {
					return events, stats, err
				}
			}
			addStats(&stats, fileStats)
			for _, event := range parsed {
				if event.UniqueKey != "" {
					if _, exists := seen[event.UniqueKey]; exists {
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

func ScanCodex(c *cache.Cache, paths []string) ([]model.LocalUsageEvent, model.ScannerStats, error) {
	stats := model.ScannerStats{}
	var events []model.LocalUsageEvent
	for _, dir := range pathsOr(paths, local.DefaultCodexSessionDirs()) {
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
			fp, err := local.FileFingerprint(file)
			if err != nil {
				return events, stats, err
			}
			parsed, fileStats, ok, err := c.LoadFileEvents("codex", file, fp.Size, fp.MtimeMs)
			if err != nil {
				return events, stats, err
			}
			if !ok {
				parsed, fileStats, err = codex.ParseFile(abs, file)
				if err != nil {
					return events, stats, err
				}
				if err := c.SaveFileEvents("codex", file, fp.Size, fp.MtimeMs, parsed, fileStats); err != nil {
					return events, stats, err
				}
			}
			addStats(&stats, fileStats)
			events = append(events, parsed...)
		}
	}
	return events, stats, nil
}

func pathsOr(paths, fallback []string) []string {
	if len(paths) > 0 {
		return paths
	}
	return fallback
}

func addStats(target *model.ScannerStats, stats model.ScannerStats) {
	target.Files += stats.Files
	target.Bytes += stats.Bytes
	target.Lines += stats.Lines
	target.Events += stats.Events
	target.InvalidLines += stats.InvalidLines
	target.MissingDirectories = append(target.MissingDirectories, stats.MissingDirectories...)
}
