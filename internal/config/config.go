package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

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
	Enabled                          bool   `json:"enabled"`
	StatePath                        string `json:"statePath,omitempty"`
	Environment                      string `json:"environment"`
	AccountLabel                     string `json:"accountLabel"`
	StartupHeartbeatRateLimitMinutes int    `json:"startupHeartbeatRateLimitMinutes"`
}

type Config struct {
	SchemaVersion int    `json:"schemaVersion"`
	CacheDir      string `json:"cacheDir,omitempty"`
	Timezone      string `json:"timezone,omitempty"`
	Locale        string `json:"locale"`
	Providers     struct {
		Claude ProviderConfig `json:"claude"`
		Codex  ProviderConfig `json:"codex"`
	} `json:"providers"`
	Server   ServerConfig   `json:"server"`
	Telegram TelegramConfig `json:"telegram"`
}

func Default() Config {
	var cfg Config
	cfg.SchemaVersion = 1
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
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = 1
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
	if cfg.Server.AccountLabel == "" {
		cfg.Server.AccountLabel = "personal"
	}
	if cfg.Server.StartupHeartbeatRateLimitMinutes == 0 {
		cfg.Server.StartupHeartbeatRateLimitMinutes = 30
	}
	if cfg.Telegram.Alerts.SessionPercent == 0 {
		cfg.Telegram.Alerts.SessionPercent = 80
	}
	if cfg.Telegram.Alerts.WeeklyPercent == 0 {
		cfg.Telegram.Alerts.WeeklyPercent = 80
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if path == "" {
		path = DefaultPath()
	}
	if path == "" {
		return errors.New("could not resolve config path")
	}
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
	if cfg.SchemaVersion != 1 {
		return errors.New("unsupported config schemaVersion")
	}
	return nil
}
