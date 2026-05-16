package config

import (
	"encoding/json"
	"errors"
	"os"
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
	Enabled     bool                 `json:"enabled"`
	BotTokenEnv string               `json:"botTokenEnv"`
	ChatID      string               `json:"chatId,omitempty"`
	Alerts      TelegramAlertsConfig `json:"alerts"`
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
	Telegram TelegramConfig `json:"telegram"`
}

func Default() Config {
	var cfg Config
	cfg.SchemaVersion = 1
	cfg.Locale = "en-US"
	cfg.Providers.Claude = ProviderConfig{Enabled: true}
	cfg.Providers.Codex = ProviderConfig{Enabled: true}
	cfg.Telegram = TelegramConfig{
		Enabled:     false,
		BotTokenEnv: "SCRIBA_TELEGRAM_BOT_TOKEN",
		Alerts: TelegramAlertsConfig{
			SessionPercent: 80,
			WeeklyPercent:  80,
			IncludeErrors:  true,
		},
	}
	return cfg
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
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
	if cfg.Telegram.Alerts.SessionPercent == 0 {
		cfg.Telegram.Alerts.SessionPercent = 80
	}
	if cfg.Telegram.Alerts.WeeklyPercent == 0 {
		cfg.Telegram.Alerts.WeeklyPercent = 80
	}
	return cfg, nil
}

func Validate(cfg Config) error {
	if cfg.SchemaVersion != 1 {
		return errors.New("unsupported config schemaVersion")
	}
	return nil
}
