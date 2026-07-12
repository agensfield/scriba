package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/agensfield/scriba/internal/codexauth"
)

var profileIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type ProviderConfig struct {
	Enabled bool     `json:"enabled"`
	Paths   []string `json:"paths"`
}

type TelegramAlertsConfig struct {
	SessionPercent  float64  `json:"sessionPercent"`
	WeeklyPercent   float64  `json:"weeklyPercent"`
	CreditsBelowUSD *float64 `json:"creditsBelowUSD,omitempty"`
	IncludeErrors   bool     `json:"includeErrors"`
}

type TelegramConfig struct {
	Enabled        bool                 `json:"enabled"`
	BotToken       string               `json:"botToken,omitempty"`
	BotTokenEnv    string               `json:"botTokenEnv"`
	ChatID         string               `json:"chatId,omitempty"`
	AllowedUserIDs []int64              `json:"allowedUserIds,omitempty"`
	ResetJokeTone  string               `json:"resetJokeTone"`
	Alerts         TelegramAlertsConfig `json:"alerts"`
}

type ServerConfig struct {
	Enabled                          bool             `json:"enabled"`
	StatePath                        string           `json:"statePath,omitempty"`
	Environment                      string           `json:"environment"`
	AccountLabel                     string           `json:"accountLabel,omitempty"`
	StartupHeartbeatRateLimitMinutes int              `json:"startupHeartbeatRateLimitMinutes"`
	ObservationRetentionDays         int              `json:"observationRetentionDays"`
	ContextAPI                       ContextAPIConfig `json:"contextAPI"`
}

type ContextAPIConfig struct {
	Enabled    bool   `json:"enabled"`
	SocketPath string `json:"socketPath,omitempty"`
}

type Config struct {
	SchemaVersion    int       `json:"schemaVersion"`
	DefaultProfileID string    `json:"defaultProfileId"`
	Profiles         []Profile `json:"profiles"`
	CacheDir         string    `json:"cacheDir,omitempty"`
	Timezone         string    `json:"timezone,omitempty"`
	Locale           string    `json:"locale"`
	Providers        struct {
		Claude ProviderConfig `json:"claude"`
		Codex  ProviderConfig `json:"codex"`
	} `json:"providers"`
	Server   ServerConfig   `json:"server"`
	Telegram TelegramConfig `json:"telegram"`
}

type Profile struct {
	ID             string   `json:"id"`
	Label          string   `json:"label"`
	Enabled        bool     `json:"enabled"`
	CodexAuthPaths []string `json:"codexAuthPaths"`
}

func Default() Config {
	var cfg Config
	cfg.SchemaVersion = 2
	cfg.DefaultProfileID = "default"
	cfg.Profiles = []Profile{{ID: "default", Label: "personal", Enabled: true, CodexAuthPaths: codexauth.AuthPaths()}}
	cfg.Locale = "en-US"
	cfg.Providers.Claude = ProviderConfig{Enabled: true}
	cfg.Providers.Codex = ProviderConfig{Enabled: true}
	cfg.Telegram = TelegramConfig{
		Enabled:       false,
		BotTokenEnv:   "SCRIBA_TELEGRAM_BOT_TOKEN",
		ResetJokeTone: "spicy",
		Alerts: TelegramAlertsConfig{
			SessionPercent: 80,
			WeeklyPercent:  80,
			IncludeErrors:  true,
		},
	}
	cfg.Server = ServerConfig{
		Enabled:                          false,
		Environment:                      "dev",
		AccountLabel:                     "personal",
		StartupHeartbeatRateLimitMinutes: 30,
		ObservationRetentionDays:         120,
	}
	return cfg
}

func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "scriba", "config.json")
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		path = DefaultPath()
		if path == "" {
			return cfg, nil
		}
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
	}
	data, err := os.ReadFile(path) // #nosec G304 -- Explicit --config path is user-controlled by design.
	if err != nil {
		return cfg, err
	}
	var header struct {
		SchemaVersion *int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return cfg, err
	}
	loadedVersion := 1
	if header.SchemaVersion != nil && *header.SchemaVersion != 0 {
		loadedVersion = *header.SchemaVersion
	}
	if loadedVersion == 2 {
		cfg.DefaultProfileID = ""
		cfg.Profiles = nil
		cfg.Server.AccountLabel = ""
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Locale == "" {
		cfg.Locale = "en-US"
	}
	if cfg.Telegram.BotTokenEnv == "" {
		cfg.Telegram.BotTokenEnv = "SCRIBA_TELEGRAM_BOT_TOKEN"
	}
	if cfg.Telegram.ResetJokeTone == "" {
		cfg.Telegram.ResetJokeTone = "spicy"
	}
	if cfg.Server.Environment == "" {
		cfg.Server.Environment = "dev"
	}
	if cfg.Server.StartupHeartbeatRateLimitMinutes == 0 {
		cfg.Server.StartupHeartbeatRateLimitMinutes = 30
	}
	if cfg.Server.ObservationRetentionDays == 0 {
		cfg.Server.ObservationRetentionDays = 120
	}
	if cfg.Telegram.Alerts.SessionPercent == 0 {
		cfg.Telegram.Alerts.SessionPercent = 80
	}
	if cfg.Telegram.Alerts.WeeklyPercent == 0 {
		cfg.Telegram.Alerts.WeeklyPercent = 80
	}
	if loadedVersion == 1 {
		label := strings.TrimSpace(cfg.Server.AccountLabel)
		if label == "" {
			label = "personal"
		}
		cfg.SchemaVersion = 2
		cfg.DefaultProfileID = "default"
		cfg.Profiles = []Profile{{ID: "default", Label: label, Enabled: true, CodexAuthPaths: codexauth.AuthPaths()}}
		return cfg, Validate(cfg)
	}
	if loadedVersion != 2 {
		return cfg, errors.New("unsupported config schemaVersion")
	}
	cfg.SchemaVersion = 2
	if err := Validate(cfg); err != nil {
		return cfg, err
	}
	cfg.Server.AccountLabel = defaultProfileLabel(cfg)
	return cfg, nil
}

func defaultProfileLabel(cfg Config) string {
	for _, profile := range cfg.Profiles {
		if profile.ID == cfg.DefaultProfileID {
			return profile.Label
		}
	}
	return ""
}

func Save(path string, cfg Config) error {
	if path == "" {
		path = DefaultPath()
	}
	if path == "" {
		return errors.New("could not resolve config path")
	}
	cfg.Server.AccountLabel = ""
	if err := Validate(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func Validate(cfg Config) error {
	if cfg.SchemaVersion != 2 {
		return errors.New("unsupported config schemaVersion")
	}
	if cfg.Server.ContextAPI.SocketPath != "" && !filepath.IsAbs(cfg.Server.ContextAPI.SocketPath) {
		return errors.New("server.contextAPI.socketPath must be absolute")
	}
	if cfg.DefaultProfileID == "" {
		return errors.New("defaultProfileId is required")
	}
	enabled := 0
	ids := make(map[string]struct{}, len(cfg.Profiles))
	paths := make(map[string]string)
	defaultEnabled := false
	for i, profile := range cfg.Profiles {
		if len(profile.ID) > 32 || !profileIDPattern.MatchString(profile.ID) {
			return fmt.Errorf("profiles[%d].id must be a lowercase slug of at most 32 characters", i)
		}
		if _, exists := ids[profile.ID]; exists {
			return fmt.Errorf("duplicate profile id %q", profile.ID)
		}
		ids[profile.ID] = struct{}{}
		if strings.TrimSpace(profile.Label) == "" {
			return fmt.Errorf("profiles[%d].label must be nonempty", i)
		}
		if profile.Enabled {
			enabled++
			defaultEnabled = defaultEnabled || profile.ID == cfg.DefaultProfileID
			if len(profile.CodexAuthPaths) == 0 {
				return fmt.Errorf("profiles[%d].codexAuthPaths must contain an explicit auth path", i)
			}
		}
		for j, path := range profile.CodexAuthPaths {
			if !filepath.IsAbs(path) {
				return fmt.Errorf("profiles[%d].codexAuthPaths[%d] must be absolute", i, j)
			}
			clean := filepath.Clean(path)
			if owner, exists := paths[clean]; exists {
				return fmt.Errorf("codex auth path %q is duplicated by profiles %q and %q", clean, owner, profile.ID)
			}
			paths[clean] = profile.ID
		}
	}
	if enabled == 0 {
		return errors.New("at least one profile must be enabled")
	}
	if !defaultEnabled {
		return errors.New("defaultProfileId must identify an enabled profile")
	}
	return nil
}
