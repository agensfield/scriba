package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/agentcontext"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
)

type composedAuthFixture struct {
	Token     string  `json:"token"`
	AccountID string  `json:"accountId"`
	Used      float64 `json:"used"`
}

type composedAuthFetcher struct{ paths map[string]string }

func (f *composedAuthFetcher) FetchLimits(context.Context) (remote.ProbeResult, error) {
	return remote.ProbeResult{}, ErrProfileAuthPaths
}

func (f *composedAuthFetcher) FetchProfileLimits(_ context.Context, profile Profile) (remote.ProbeResult, error) {
	if len(profile.AuthPaths) != 1 {
		return remote.ProbeResult{}, ErrProfileAuthPaths
	}
	path := profile.AuthPaths[0]
	f.paths[profile.Ref] = path
	raw, err := os.ReadFile(path) // #nosec G304 -- test reads the explicit fixture path under t.TempDir.
	if err != nil {
		return remote.ProbeResult{}, err
	}
	var fixture composedAuthFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		return remote.ProbeResult{}, err
	}
	reset := time.Date(2026, 7, 20, 3, 0, 0, 0, time.UTC).Format(time.RFC3339)
	return remote.ProbeResult{
		ProviderID: "codex",
		Lines:      []model.MetricLine{{Type: "progress", Label: resetwatch.LabelWeeklyLimit, Used: &fixture.Used, ResetsAt: reset}},
		AuthState:  remote.AuthState{OK: true, Source: path, Error: "PRIVATE_DIAGNOSTIC", AccessToken: fixture.Token, AccountID: fixture.AccountID},
	}, nil
}

func TestComposedTwoAuthProfilesRemainIsolatedAndPrivate(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeFixture := func(name, token, account string, used float64) string {
		t.Helper()
		path := filepath.Join(dir, name+".json")
		raw, _ := json.Marshal(composedAuthFixture{Token: token, AccountID: account, Used: used})
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	personalPath := writeFixture("personal-auth", "PRIVATE_TOKEN_PERSONAL", "acct-personal", 21)
	workPath := writeFixture("work-auth", "PRIVATE_TOKEN_WORK", "acct-work", 82)
	profiles := []Profile{
		{Ref: "personal", Label: "Personal", AuthPaths: []string{personalPath}, Default: true},
		{Ref: "work", Label: "Work", AuthPaths: []string{workPath}},
	}
	statePath := filepath.Join(dir, "state.sqlite")
	st := openStoreAt(t, statePath)
	syncRuntimeProfiles(t, st, profiles)
	fetcher := &composedAuthFetcher{paths: map[string]string{}}
	srv := New(st, fetcher, nil, Config{Profiles: profiles})
	result, err := srv.RefreshProfilesNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Profiles) != 2 || result.Profiles[0].Observation.Account.Ref != "acct-personal" || result.Profiles[1].Observation.Account.Ref != "acct-work" {
		t.Fatalf("results=%+v", result.Profiles)
	}
	if fetcher.paths["personal"] != personalPath || fetcher.paths["work"] != workPath {
		t.Fatalf("paths=%+v", fetcher.paths)
	}

	svc := agentcontext.New(agentcontext.Config{CacheDir: filepath.Join(dir, "missing-cache"), StorePath: statePath, DefaultProfileID: "personal", ProfileIDs: []string{"personal", "work"}})
	for profile, wantUsed := range map[string]float64{"personal": 21, "work": 82} {
		got, err := svc.ContextForProfile(ctx, profile)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"profileId":"`+profile+`"`) || !strings.Contains(string(raw), `"usedPercent":`+formatUsed(wantUsed)) {
			t.Fatalf("%s context=%s", profile, raw)
		}
		for _, private := range []string{"acct-personal", "acct-work", "PRIVATE_TOKEN", personalPath, workPath, "PRIVATE_DIAGNOSTIC"} {
			if strings.Contains(string(raw), private) {
				t.Fatalf("%s leaked %q in %s", profile, private, raw)
			}
		}
	}

	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"PRIVATE_TOKEN_PERSONAL", "PRIVATE_TOKEN_WORK", personalPath, workPath, "PRIVATE_DIAGNOSTIC"} {
		if strings.Contains(string(database), private) {
			t.Fatalf("database contains private fixture %q", private)
		}
	}
}

func formatUsed(value float64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func openStoreAt(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}
