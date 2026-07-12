package config

import (
	"os"
	"path/filepath"
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
