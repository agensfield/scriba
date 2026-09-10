package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
)

func TestAccountListItemsKeepCredentialAndObservationFactsDistinct(t *testing.T) {
	now := time.Date(2026, 9, 10, 20, 0, 0, 0, time.UTC)
	accounts := []store.Account{
		{ID: "acct-live", ProviderID: "codex", Alias: "personal", Email: "a@example.com", Plan: "pro", CredentialsAvailable: true, LastSeenAt: now.Add(-2 * time.Minute)},
		{ID: "acct-old", ProviderID: "codex", Email: "old@example.com", CredentialsAvailable: false, LastSeenAt: now.Add(-2 * time.Hour)},
		{ID: "acct-never", ProviderID: "codex", CredentialsAvailable: false},
	}
	items := accountListItems(accounts, now)
	if len(items) != 3 {
		t.Fatalf("items=%+v", items)
	}
	if !items[0].CredentialsAvailable || items[0].Stale || items[0].LastObservedAgeMs == nil || *items[0].LastObservedAgeMs != 120000 {
		t.Fatalf("fresh account=%+v", items[0])
	}
	if items[1].CredentialsAvailable || !items[1].Stale || items[1].LastObservedAt == "" {
		t.Fatalf("historical account=%+v", items[1])
	}
	if items[2].LastObservedAt != "" || items[2].LastObservedAgeMs != nil || items[2].Stale {
		t.Fatalf("never-observed account=%+v", items[2])
	}
}

func TestRedactAccountsHidesHumanFieldsButKeepsSafeIdentity(t *testing.T) {
	payload := accountsPayload{SchemaVersion: accountsSchemaVersion, Accounts: []accountListItem{{
		AccountID: "acct-0123456789abcdef0123", ProviderID: "codex", Alias: "personal", Email: "a@example.com", Plan: "pro", CredentialsAvailable: true,
	}}}
	redacted := redactAccounts(payload)
	raw, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"personal", "a@example.com"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("redacted payload contains %q: %s", forbidden, text)
		}
	}
	for _, required := range []string{"acct-0123456789abcdef0123", "credentialsAvailable"} {
		if !strings.Contains(text, required) {
			t.Fatalf("redacted payload missing %q: %s", required, text)
		}
	}
}

func TestRenderAccountsShowsAvailabilityAndFreshness(t *testing.T) {
	payload := accountsPayload{SchemaVersion: accountsSchemaVersion, Accounts: []accountListItem{
		{AccountID: "acct-live", ProviderID: "codex", Alias: "personal", Email: "a@example.com", CredentialsAvailable: true, LastObservedAt: "2026-09-10T19:58:00Z", LastObservedAgeMs: ptrInt64(120000)},
		{AccountID: "acct-old", ProviderID: "codex", CredentialsAvailable: false, LastObservedAt: "2026-09-10T18:00:00Z", LastObservedAgeMs: ptrInt64(7200000), Stale: true},
	}}
	text := renderAccounts(payload)
	for _, want := range []string{"Scriba accounts", "personal", "available", "unavailable", "fresh", "stale", "age 2m0s"} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q:\n%s", want, text)
		}
	}
}

func TestDispatchAccountsSupportsShorthandAndListFlags(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "missing.sqlite")
	configPath := filepath.Join(dir, "config.json")
	configJSON := []byte(`{"schemaVersion":3,"codexAuthPaths":["` + filepath.Join(dir, "missing-auth.json") + `"]}`)
	if err := os.WriteFile(configPath, configJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--json", "--config", configPath, "--state-path", statePath},
		{"list", "--json", "--config", configPath, "--state-path", statePath},
	} {
		var dispatchErr error
		stdout := captureCLIStdout(t, func() { dispatchErr = dispatchAccounts(args) })
		if dispatchErr != nil {
			t.Fatalf("dispatchAccounts(%q): %v", args, dispatchErr)
		}
		var payload accountsPayload
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatalf("dispatchAccounts(%q) output=%q: %v", args, stdout, err)
		}
		if payload.SchemaVersion != accountsSchemaVersion || payload.Accounts == nil {
			t.Fatalf("dispatchAccounts(%q) payload=%+v", args, payload)
		}
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("read-only account listing created state: %v", err)
	}
}

func TestAccountsCleanInstallDiscoversAuthWithoutCreatingState(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "missing.sqlite")
	authPath := filepath.Join(dir, "auth.json")
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"token-clean-install","account_id":"private-clean-install"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	configJSON := []byte(`{"schemaVersion":3,"codexAuthPaths":["` + authPath + `"]}`)
	if err := os.WriteFile(configPath, configJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	var dispatchErr error
	stdout := captureCLIStdout(t, func() {
		dispatchErr = dispatchAccounts([]string{"--json", "--config", configPath, "--state-path", statePath})
	})
	if dispatchErr != nil {
		t.Fatalf("clean-install accounts: %v", dispatchErr)
	}
	var payload accountsPayload
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode accounts output=%q: %v", stdout, err)
	}
	if len(payload.Accounts) != 1 || payload.Accounts[0].AccountID != store.AccountID("codex", "private-clean-install") || !payload.Accounts[0].CredentialsAvailable || payload.Accounts[0].LastObservedAt != "" {
		t.Fatalf("clean-install payload=%+v", payload)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("clean-install listing created state: %v", err)
	}
}

func TestLiveOptionsCleanInstallPinColdAccountWithoutState(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "missing.sqlite")
	authPath := filepath.Join(dir, "auth.json")
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"token-clean-live","account_id":"private-clean-live"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"schemaVersion":3,"codexAuthPaths":["`+authPath+`"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fetchOpts, cleanup, err := resolveLiveCodexOptions(context.Background(), options{config: configPath, statePath: statePath})
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if len(fetchOpts.AuthPaths) != 1 || fetchOpts.AuthPaths[0] != authPath || fetchOpts.ExpectedAccountID != "private-clean-live" {
		t.Fatalf("fetch options=%+v", fetchOpts)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("clean-install live selection created state: %v", err)
	}
}

func TestDispatchAccountsAliasAcceptsFlagsAfterSelector(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "server.sqlite")
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"schemaVersion":3,"codexAuthPaths":["`+filepath.Join(dir, "missing-auth.json")+`"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	used := 20.0
	at := time.Now().UTC().Add(-time.Minute)
	_, err = st.ApplyCodexPoll(context.Background(), store.CodexPollInput{Observation: resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: "private-alias"}, ObservedAt: at, Windows: []resetwatch.Window{{Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: at.Add(5 * time.Hour)}}}, CommittedAt: at.Add(time.Second)})
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	_ = st.Close()
	selector := store.AccountID("codex", "private-alias")
	var dispatchErr error
	stdout := captureCLIStdout(t, func() {
		dispatchErr = dispatchAccounts([]string{"alias", selector, "personal", "--json", "--config", configPath, "--state-path", statePath})
	})
	if dispatchErr != nil {
		t.Fatalf("alias dispatch: %v", dispatchErr)
	}
	var payload accountsPayload
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode alias output=%q: %v", stdout, err)
	}
	if len(payload.Accounts) != 1 || payload.Accounts[0].AccountID != selector || payload.Accounts[0].Alias != "personal" {
		t.Fatalf("alias payload=%+v", payload)
	}
}

func ptrInt64(value int64) *int64 { return &value }
