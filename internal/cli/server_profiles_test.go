package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/agensfield/scriba/internal/config"
	"github.com/agensfield/scriba/internal/server/store"
)

func TestServerProfilesPreserveOrderFilterDisabledAndSyncMetadata(t *testing.T) {
	cfg := config.Default()
	cfg.DefaultProfileID = "work"
	cfg.Profiles = []config.Profile{
		{ID: "personal", Label: "Personal", Enabled: true, CodexAuthPaths: []string{"/auth/personal.json"}},
		{ID: "disabled", Label: "Disabled", Enabled: false, CodexAuthPaths: []string{"/auth/disabled.json"}},
		{ID: "work", Label: "Work", Enabled: true, CodexAuthPaths: []string{"/auth/work.json"}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	runtime, specs := serverProfiles(cfg)
	if len(runtime) != 2 || runtime[0].Ref != "personal" || runtime[1].Ref != "work" || runtime[0].Default || !runtime[1].Default {
		t.Fatalf("runtime=%+v", runtime)
	}
	if len(specs) != 3 || specs[1].ProfileRef != "disabled" || specs[1].Enabled || !specs[2].IsDefault {
		t.Fatalf("specs=%+v", specs)
	}
	if runtime[0].AllowAuthDiscovery || runtime[1].AllowAuthDiscovery {
		t.Fatal("explicit profile enabled ambient auth discovery")
	}
	cfg.Profiles[0].CodexAuthPaths[0] = "/mutated"
	if runtime[0].AuthPaths[0] != "/auth/personal.json" {
		t.Fatal("runtime auth paths alias config memory")
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err = st.SyncProfiles(context.Background(), specs); err != nil {
		t.Fatal(err)
	}
	health, err := st.ListProfileHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byRef := map[string]store.ProfileHealth{}
	for _, item := range health {
		byRef[item.ProfileRef] = item
	}
	if byRef["work"].Label != "Work" || !byRef["work"].IsDefault || !byRef["personal"].Enabled || byRef["disabled"].Enabled || byRef["default"].Enabled {
		t.Fatalf("health=%+v", byRef)
	}
}

func TestServerProfilesPreserveLegacyDefaultCompatibility(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(dir, "codex-home"))
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"server":{"accountLabel":"Legacy"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	runtime, specs := serverProfiles(cfg)
	if len(runtime) != 1 || runtime[0].Ref != "default" || runtime[0].Label != "Legacy" || !runtime[0].Default || len(runtime[0].AuthPaths) == 0 || runtime[0].AllowAuthDiscovery {
		t.Fatalf("runtime=%+v", runtime)
	}
	if len(specs) != 1 || specs[0].ProfileRef != "default" || !specs[0].Enabled || !specs[0].IsDefault {
		t.Fatalf("specs=%+v", specs)
	}
}
