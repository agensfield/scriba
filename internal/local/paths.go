package local

import (
	"os"
	"path/filepath"
	"strings"
)

func DefaultClaudeProjectDirs() []string {
	if configured := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); configured != "" {
		parts := strings.Split(configured, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				out = append(out, filepath.Join(trimmed, "projects"))
			}
		}
		return out
	}
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".config", "claude", "projects"),
		filepath.Join(home, ".claude", "projects"),
	}
}

func DefaultCodexSessionDirs() []string {
	home, _ := os.UserHomeDir()
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	return []string{filepath.Join(codexHome, "sessions")}
}
