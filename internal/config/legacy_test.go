package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLegacySourcesKeepDefaultOrderWithoutRewriting(t *testing.T) {
	t.Setenv("CODEX_HOME", "/unrelated")
	file := filepath.Join(t.TempDir(), "config.json")
	data := `{"schemaVersion":2,"defaultProfileId":"work","profiles":[{"id":"home","label":"Home","enabled":true,"codexAuthPaths":["/auth/home.json"]},{"id":"off","label":"Off","enabled":false,"codexAuthPaths":["/auth/off.json"]},{"id":"work","label":"Work","enabled":true,"codexAuthPaths":["/auth/work.json"]}]}`
	if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.AuthPaths(), []string{"/auth/work.json", "/auth/home.json"}) {
		t.Fatalf("sources=%v", cfg.AuthPaths())
	}
	unchanged, err := os.ReadFile(file)
	if err != nil || string(unchanged) != data {
		t.Fatalf("read changed config: %v", err)
	}
}

func TestExplicitSourcesRemainExplicitWhenMissing(t *testing.T) {
	t.Setenv("CODEX_HOME", "/unrelated")
	cfg := Default()
	cfg.CodexAuthPaths = []string{"/missing/auth.json"}
	if got := cfg.AuthPaths(); len(got) != 1 || got[0] != "/missing/auth.json" {
		t.Fatalf("sources=%v", got)
	}
}
