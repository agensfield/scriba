package bench

import (
	"os"
	"time"

	"github.com/agensfield/scriba/internal/local"
)

type Payload struct {
	GeneratedAt string    `json:"generatedAt"`
	Provider    string    `json:"provider"`
	Execute     bool      `json:"execute"`
	Datasets    []Dataset `json:"datasets"`
	Commands    []Command `json:"commands"`
}

type Dataset struct {
	ProviderID string `json:"providerId"`
	Path       string `json:"path"`
	Files      int    `json:"files"`
	Bytes      int64  `json:"bytes"`
	Exists     bool   `json:"exists"`
}

type Command struct {
	ProviderID string   `json:"providerId"`
	Args       []string `json:"args"`
}

func Build(provider string, execute bool) Payload {
	payload := Payload{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), Provider: provider, Execute: execute}
	if provider == "all" || provider == "claude" {
		payload.Datasets = append(payload.Datasets, dataset("claude", local.DefaultClaudeProjectDirs()))
		payload.Commands = append(payload.Commands, Command{ProviderID: "claude", Args: []string{"bunx", "ccusage", "daily", "--json"}})
	}
	if provider == "all" || provider == "codex" {
		payload.Datasets = append(payload.Datasets, dataset("codex", local.DefaultCodexSessionDirs()))
		payload.Commands = append(payload.Commands, Command{ProviderID: "codex", Args: []string{"bunx", "-p", "@ccusage/codex", "ccusage-codex", "daily", "--json"}})
	}
	return payload
}

func dataset(providerID string, paths []string) Dataset {
	item := Dataset{ProviderID: providerID}
	if len(paths) > 0 {
		item.Path = paths[0]
	}
	for _, path := range paths {
		if !local.IsDirectory(path) {
			continue
		}
		item.Exists = true
		files, _ := local.WalkJSONLFiles(path)
		item.Files += len(files)
		for _, file := range files {
			if info, err := os.Stat(file); err == nil {
				item.Bytes += info.Size()
			}
		}
	}
	return item
}
