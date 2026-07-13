package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/agensfield/scriba/internal/codexauth"
)

var profileIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var ntfyTopicPattern = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)

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

const MaxObservationRetentionDays = 36500

type ContextAPIConfig struct {
	Enabled    bool   `json:"enabled"`
	SocketPath string `json:"socketPath,omitempty"`
}

type WebhookConfig struct {
	ID        string `json:"id"`
	Enabled   bool   `json:"enabled"`
	URL       string `json:"url"`
	SecretEnv string `json:"secretEnv"`
}

type NtfyConfig struct {
	ID       string `json:"id"`
	Enabled  bool   `json:"enabled"`
	URL      string `json:"url"`
	Topic    string `json:"topic"`
	TokenEnv string `json:"tokenEnv,omitempty"`
}

type DeliveryConfig struct {
	Webhooks []WebhookConfig `json:"webhooks,omitempty"`
	Ntfy     []NtfyConfig    `json:"ntfy,omitempty"`
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
	Server     ServerConfig   `json:"server"`
	Telegram   TelegramConfig `json:"telegram"`
	Deliveries DeliveryConfig `json:"deliveries,omitempty"`
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
	if cfg.Server.ObservationRetentionDays <= 0 || cfg.Server.ObservationRetentionDays > MaxObservationRetentionDays {
		return fmt.Errorf("server.observationRetentionDays must be between 1 and %d", MaxObservationRetentionDays)
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
	if err := validateDeliveries(cfg.Deliveries); err != nil {
		return err
	}
	return nil
}

func validateDeliveries(cfg DeliveryConfig) error {
	seen := make(map[string]struct{}, len(cfg.Webhooks)+len(cfg.Ntfy))
	validateID := func(kind, id string) error {
		if len(id) > 32 || !profileIDPattern.MatchString(id) {
			return fmt.Errorf("%s id must be a lowercase slug of at most 32 characters", kind)
		}
		target := kind + ":" + id
		if _, ok := seen[target]; ok {
			return fmt.Errorf("duplicate delivery target %q", target)
		}
		seen[target] = struct{}{}
		return nil
	}
	validateURL := func(kind, value string) error {
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("%s url must be an absolute HTTP(S) URL", kind)
		}
		return nil
	}
	for i, webhook := range cfg.Webhooks {
		kind := fmt.Sprintf("deliveries.webhooks[%d]", i)
		if err := validateID("webhook", webhook.ID); err != nil {
			return fmt.Errorf("%s: %w", kind, err)
		}
		if err := validateURL(kind, webhook.URL); err != nil {
			return err
		}
		if webhook.Enabled && !envNamePattern.MatchString(webhook.SecretEnv) {
			return fmt.Errorf("%s.secretEnv is required and must name an environment variable", kind)
		}
	}
	for i, ntfy := range cfg.Ntfy {
		kind := fmt.Sprintf("deliveries.ntfy[%d]", i)
		if err := validateID("ntfy", ntfy.ID); err != nil {
			return fmt.Errorf("%s: %w", kind, err)
		}
		if err := validateURL(kind, ntfy.URL); err != nil {
			return err
		}
		if u, _ := url.Parse(ntfy.URL); u.RawQuery != "" {
			return fmt.Errorf("%s.url must not contain a query string", kind)
		}
		if !ntfyTopicPattern.MatchString(ntfy.Topic) {
			return fmt.Errorf("%s.topic must contain 1-64 letters, numbers, underscores, or dashes", kind)
		}
		if ntfy.TokenEnv != "" && !envNamePattern.MatchString(ntfy.TokenEnv) {
			return fmt.Errorf("%s.tokenEnv must name an environment variable", kind)
		}
	}
	return nil
}
