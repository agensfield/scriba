package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContextAPIDefaultAndBackwardCompatibility(t *testing.T) {
	if Default().Server.ContextAPI.Enabled {
		t.Fatal("context API must default disabled")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{"schemaVersion":1,"locale":"en-US","providers":{},"server":{},"telegram":{}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.ContextAPI.Enabled || cfg.Server.ContextAPI.SocketPath != "" {
		t.Fatalf("legacy config enabled context API: %+v", cfg.Server.ContextAPI)
	}
}

func TestContextAPIRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.Server.ContextAPI = ContextAPIConfig{Enabled: true, SocketPath: "/tmp/scriba.sock"}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Server.ContextAPI != cfg.Server.ContextAPI {
		t.Fatalf("context API = %+v, want %+v", got.Server.ContextAPI, cfg.Server.ContextAPI)
	}
}

func TestContextAPISocketPathMustBeAbsolute(t *testing.T) {
	cfg := Default()
	cfg.Server.ContextAPI.SocketPath = "relative/context.sock"
	if err := Validate(cfg); err == nil {
		t.Fatal("relative context API socket path accepted")
	}
}

func TestObservationRetentionMustBePositive(t *testing.T) {
	for _, days := range []int{-1, int(^uint(0) >> 1)} {
		cfg := Default()
		cfg.Server.ObservationRetentionDays = days
		if err := Validate(cfg); err == nil {
			t.Fatalf("invalid observation retention %d accepted", days)
		}
	}
}

func TestDefaultDiscoversAuthPaths(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	cfg := Default()
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(codexHome, "auth.json")
	if cfg.SchemaVersion != 3 || len(cfg.AuthPaths()) != 1 || cfg.AuthPaths()[0] != want {
		t.Fatalf("unexpected default: %+v", cfg)
	}
}

func TestLoadV1NormalizesWithoutRewriting(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{"schemaVersion":1,"locale":"en-US","providers":{},"server":{"accountLabel":"work"},"telegram":{}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SchemaVersion != 3 || cfg.AuthPaths()[0] != filepath.Join(codexHome, "auth.json") || len(cfg.Providers.Codex.Paths) != 0 {
		t.Fatalf("unexpected normalized config: %+v", cfg)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != legacy {
		t.Fatalf("legacy file rewritten: %q, %v", got, err)
	}
}

func TestLoadVersionlessV1NormalizesWithoutRewriting(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{"providers":{},"server":{"accountLabel":"legacy-work"},"telegram":{}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SchemaVersion != 3 || cfg.AuthPaths()[0] != filepath.Join(codexHome, "auth.json") {
		t.Fatalf("unexpected normalized config: %+v", cfg)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != legacy {
		t.Fatalf("legacy file rewritten: %q, %v", got, err)
	}
}

func TestV2LoadDoesNotDiscoverMissingProfilesOrPaths(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	missingProfiles := `{"schemaVersion":2,"defaultProfileId":"default","locale":"en-US","providers":{},"server":{},"telegram":{}}`
	if err := os.WriteFile(path, []byte(missingProfiles), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("v2 missing profiles accepted via discovery fallback")
	}
	noPaths := `{"schemaVersion":2,"defaultProfileId":"default","profiles":[{"id":"default","label":"Personal","enabled":true}],"providers":{},"server":{},"telegram":{}}`
	if err := os.WriteFile(path, []byte(noPaths), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("v2 enabled profile without explicit paths accepted")
	}
}

func TestAuthSourcesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.CodexAuthPaths = []string{"/auth/work.json", "/auth/personal.json"}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.AuthPaths()) != 2 || got.AuthPaths()[0] != "/auth/work.json" || got.AuthPaths()[1] != "/auth/personal.json" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil || raw["schemaVersion"] != float64(3) {
		t.Fatalf("invalid saved config: %v, %s", err, data)
	}
}

func TestAuthSourceValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"relative auth", func(c *Config) { c.CodexAuthPaths = []string{"auth.json"} }},
		{"empty explicit sources", func(c *Config) { c.CodexAuthPaths = []string{} }},
		{"duplicate auth", func(c *Config) { c.CodexAuthPaths = []string{"/tmp/auth.json", "/tmp/auth.json"} }},
		{"cleaned path overlap", func(c *Config) { c.CodexAuthPaths = []string{"/tmp/auth.json", "/tmp/x/../auth.json"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(&cfg)
			if err := Validate(cfg); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestDeliveryConfigRoundTripAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.Deliveries = DeliveryConfig{
		Webhooks: []WebhookConfig{{ID: "deploy", Enabled: true, URL: "https://example.com/scriba", SecretEnv: "SCRIBA_WEBHOOK_DEPLOY_SECRET"}},
		Ntfy:     []NtfyConfig{{ID: "phone", Enabled: true, URL: "https://ntfy.sh", Topic: "scriba-private", TokenEnv: "SCRIBA_NTFY_TOKEN"}},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Deliveries.Webhooks) != 1 || got.Deliveries.Webhooks[0].ID != "deploy" || len(got.Deliveries.Ntfy) != 1 || got.Deliveries.Ntfy[0].Topic != "scriba-private" {
		t.Fatalf("deliveries=%+v", got.Deliveries)
	}

	tests := []func(*Config){
		func(c *Config) { c.Deliveries.Webhooks[0].ID = "Bad_ID" },
		func(c *Config) { c.Deliveries.Webhooks[0].URL = "/relative" },
		func(c *Config) { c.Deliveries.Webhooks[0].SecretEnv = "" },
		func(c *Config) { c.Deliveries.Ntfy[0].Topic = " bad " },
		func(c *Config) { c.Deliveries.Ntfy[0].Topic = strings.Repeat("a", 65) },
		func(c *Config) { c.Deliveries.Ntfy[0].URL = "https://user:secret@ntfy.sh" },
		func(c *Config) { c.Deliveries.Ntfy[0].URL = "https://ntfy.sh?token=secret" },
		func(c *Config) { c.Deliveries.Ntfy[0].TokenEnv = "BAD ENV" },
		func(c *Config) { c.Deliveries.Ntfy = append(c.Deliveries.Ntfy, c.Deliveries.Ntfy[0]) },
	}
	for i, mutate := range tests {
		broken := cfg
		broken.Deliveries.Webhooks = append([]WebhookConfig(nil), cfg.Deliveries.Webhooks...)
		broken.Deliveries.Ntfy = append([]NtfyConfig(nil), cfg.Deliveries.Ntfy...)
		mutate(&broken)
		if err := Validate(broken); err == nil {
			t.Fatalf("invalid delivery config %d accepted", i)
		}
	}
}
