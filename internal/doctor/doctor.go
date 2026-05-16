package doctor

import (
	"os"
	"time"

	"github.com/agensfield/scriba/internal/cache"
	"github.com/agensfield/scriba/internal/config"
	"github.com/agensfield/scriba/internal/local"
	"github.com/agensfield/scriba/internal/model"
	remoteclaude "github.com/agensfield/scriba/internal/remote/claude"
	remotecodex "github.com/agensfield/scriba/internal/remote/codex"
)

type Payload struct {
	GeneratedAt string `json:"generatedAt"`
	State       string `json:"state"`
	Cache       struct {
		State               string `json:"state"`
		CacheDir            string `json:"cacheDir"`
		DatabasePath        string `json:"databasePath"`
		SizeBytes           int64  `json:"sizeBytes"`
		SchemaVersion       int    `json:"schemaVersion"`
		WALEnabled          bool   `json:"walEnabled"`
		LatestSnapshotAgeMs *int64 `json:"latestSnapshotAgeMs"`
		Error               string `json:"error,omitempty"`
	} `json:"cache"`
	Providers []Provider `json:"providers"`
}

type Provider struct {
	ProviderID  string      `json:"providerId"`
	DisplayName string      `json:"displayName"`
	State       string      `json:"state"`
	LocalPaths  []PathCheck `json:"localPaths"`
	Auth        AuthCheck   `json:"auth"`
	Remote      RemoteCheck `json:"remote"`
}

type PathCheck struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

type AuthCheck struct {
	State string      `json:"state"`
	Paths []PathCheck `json:"paths"`
	Hint  string      `json:"hint"`
}

type RemoteCheck struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

func Build(cfg config.Config, c *cache.Cache, includeRemote bool) (Payload, error) {
	now := time.Now().UTC()
	payload := Payload{GeneratedAt: now.Format(time.RFC3339Nano)}
	cacheStatus, err := c.Status()
	if err != nil {
		payload.Cache.State = "broken"
		payload.Cache.Error = err.Error()
	} else {
		payload.Cache.State = "ok"
		if cacheStatus.SchemaVersion == 0 || !cacheStatus.WAL.Enabled {
			payload.Cache.State = "degraded"
		}
		payload.Cache.CacheDir = cacheStatus.CacheDir
		payload.Cache.DatabasePath = cacheStatus.DatabasePath
		payload.Cache.SizeBytes = cacheStatus.SizeBytes
		payload.Cache.SchemaVersion = cacheStatus.SchemaVersion
		payload.Cache.WALEnabled = cacheStatus.WAL.Enabled
		var latest int64 = -1
		for _, snapshot := range cacheStatus.Snapshots {
			if t, err := time.Parse(time.RFC3339Nano, snapshot.UpdatedAt); err == nil {
				age := now.Sub(t).Milliseconds()
				if latest == -1 || age < latest {
					latest = age
				}
			}
		}
		if latest >= 0 {
			payload.Cache.LatestSnapshotAgeMs = &latest
		}
	}
	payload.Providers = append(payload.Providers,
		buildClaude(cfg, includeRemote),
		buildCodex(cfg, includeRemote),
	)
	payload.State = worst(append([]string{payload.Cache.State}, providerStates(payload.Providers)...))
	return payload, nil
}

func buildClaude(cfg config.Config, includeRemote bool) Provider {
	paths := cfg.Providers.Claude.Paths
	if len(paths) == 0 {
		paths = local.DefaultClaudeProjectDirs()
	}
	provider := Provider{ProviderID: "claude", DisplayName: "Claude", Remote: RemoteCheck{State: "skipped"}}
	for _, path := range paths {
		provider.LocalPaths = append(provider.LocalPaths, PathCheck{Path: path, Exists: local.IsDirectory(path)})
	}
	for _, path := range remoteclaude.CredentialPaths() {
		_, err := os.Stat(path)
		provider.Auth.Paths = append(provider.Auth.Paths, PathCheck{Path: path, Exists: err == nil})
	}
	for _, service := range remoteclaude.KeychainServices() {
		provider.Auth.Paths = append(provider.Auth.Paths, PathCheck{Path: "macOS Keychain: " + service, Exists: remoteclaude.KeychainServiceExists(service)})
	}
	provider.Auth.Hint = "Run `claude` to authenticate."
	provider.Auth.State = existsState(provider.Auth.Paths)
	localState := existsState(provider.LocalPaths)
	if includeRemote {
		result, err := remoteclaude.Probe(true)
		provider.Remote = remoteState(result.Provenance, err)
	}
	provider.State = worst([]string{localState, provider.Auth.State, skippedOK(provider.Remote.State)})
	return provider
}

func buildCodex(cfg config.Config, includeRemote bool) Provider {
	paths := cfg.Providers.Codex.Paths
	if len(paths) == 0 {
		paths = local.DefaultCodexSessionDirs()
	}
	provider := Provider{ProviderID: "codex", DisplayName: "Codex", Remote: RemoteCheck{State: "skipped"}}
	for _, path := range paths {
		provider.LocalPaths = append(provider.LocalPaths, PathCheck{Path: path, Exists: local.IsDirectory(path)})
	}
	for _, path := range remotecodex.AuthPaths() {
		_, err := os.Stat(path)
		provider.Auth.Paths = append(provider.Auth.Paths, PathCheck{Path: path, Exists: err == nil})
	}
	provider.Auth.Hint = "Run `codex` to authenticate."
	provider.Auth.State = existsState(provider.Auth.Paths)
	localState := existsState(provider.LocalPaths)
	if includeRemote {
		result, err := remotecodex.Probe(true)
		provider.Remote = remoteState(result.Provenance, err)
	}
	provider.State = worst([]string{localState, provider.Auth.State, skippedOK(provider.Remote.State)})
	return provider
}

func existsState(paths []PathCheck) string {
	for _, path := range paths {
		if path.Exists {
			return "ok"
		}
	}
	return "degraded"
}

func remoteState(provenance []model.SourceProvenance, err error) RemoteCheck {
	if err != nil {
		return RemoteCheck{State: "degraded", Error: err.Error()}
	}
	for _, source := range provenance {
		if source.Error != "" {
			return RemoteCheck{State: "degraded", Error: source.Error}
		}
	}
	return RemoteCheck{State: "ok"}
}

func providerStates(providers []Provider) []string {
	states := make([]string, 0, len(providers))
	for _, provider := range providers {
		states = append(states, provider.State)
	}
	return states
}

func skippedOK(state string) string {
	if state == "skipped" {
		return "ok"
	}
	return state
}

func worst(states []string) string {
	for _, state := range states {
		if state == "broken" {
			return "broken"
		}
	}
	for _, state := range states {
		if state == "degraded" {
			return "degraded"
		}
	}
	return "ok"
}
