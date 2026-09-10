package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
	remotecodex "github.com/agensfield/scriba/internal/remote/codex"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
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
	if err := os.WriteFile(configPath, []byte(`{"schemaVersion":3,"codexAuthPaths":["`+authPath+`"],"server":{"statePath":"`+statePath+`"}}`), 0o600); err != nil {
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

func TestCleanInstallFastCommandsReturnBoundedNoObservation(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	configPath := filepath.Join(dir, "config.json")
	statePath := filepath.Join(dir, "missing.sqlite")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"token-clean-fast","account_id":"private-clean-fast"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"schemaVersion":3,"codexAuthPaths":["`+authPath+`"],"server":{"statePath":"`+statePath+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"--json", "--config", configPath}
	for _, command := range []string{"codex limits --fast", "codex reset-grants --fast", "status --fast"} {
		args := append([]string{}, strings.Split(command, " ")...)
		args = append(args, base...)
		if err := dispatch(args); err == nil || !strings.Contains(err.Error(), "no stored Codex limits") {
			t.Fatalf("dispatch(%q) error=%v", args, err)
		}
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("clean-install fast command created state: %v", err)
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

func TestFastResetGrantsCommandMatchesSchemaAndReportsCredentials(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "server.sqlite")
	authPath := filepath.Join(dir, "missing-auth.json")
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"schemaVersion":3,"codexAuthPaths":["`+authPath+`"],"server":{"statePath":"`+statePath+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	used := 84.0
	at := time.Now().UTC().Add(-time.Hour)
	_, err = st.ApplyCodexPoll(context.Background(), store.CodexPollInput{Observation: resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: "private-grants"}, ObservedAt: at, ResetGrants: resetwatch.ResetGrants{AvailableCount: ptrInt(1)}, Windows: []resetwatch.Window{{Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: at.Add(5 * time.Hour)}}}, CommittedAt: at.Add(time.Second)})
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	_ = st.Close()
	selector := store.AccountID("codex", "private-grants")
	var dispatchErr error
	stdout := captureCLIStdout(t, func() {
		dispatchErr = dispatch([]string{"codex", "reset-grants", "--fast", "--json", "--config", configPath, "--account", selector})
	})
	if dispatchErr != nil {
		t.Fatal(dispatchErr)
	}
	var payload any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode grants output=%q: %v", stdout, err)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "schemas", "codex-reset-grants.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	const schemaURL = "https://agensfield.dev/scriba/schemas/codex-reset-grants.schema.json"
	limitsURL := "https://agensfield.dev/scriba/schemas/codex-limits.schema.json"
	limitsData, err := os.ReadFile(filepath.Join("..", "..", "schemas", "codex-limits.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var limitsDocument any
	if err := json.Unmarshal(limitsData, &limitsDocument); err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource(limitsURL, limitsDocument); err != nil {
		t.Fatal(err)
	}
	statusURL := "https://agensfield.dev/scriba/schemas/status.schema.json"
	statusData, err := os.ReadFile(filepath.Join("..", "..", "schemas", "status.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var statusDocument any
	if err := json.Unmarshal(statusData, &statusDocument); err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource(statusURL, statusDocument); err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource(schemaURL, document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(payload); err != nil {
		t.Fatalf("fast grants schema: %v\n%s", err, stdout)
	}
	object := payload.(map[string]any)
	if object["accountId"] != selector || object["credentialsAvailable"] != false {
		t.Fatalf("account state missing: %#v", object)
	}
	if auth, ok := object["authState"].(map[string]any); !ok || auth["ok"] != false {
		t.Fatalf("auth state=%#v", object["authState"])
	}
}

func TestInactiveAccountLimitsFallBackToStoredObservation(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "server.sqlite")
	authPath := filepath.Join(dir, "missing-auth.json")
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"schemaVersion":3,"codexAuthPaths":["`+authPath+`"],"server":{"statePath":"`+statePath+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour)
	used := 72.0
	_, err = st.ApplyCodexPoll(context.Background(), store.CodexPollInput{Observation: resetwatch.Observation{
		ProviderID: "codex",
		Account:    resetwatch.Account{Ref: "private-inactive"},
		ObservedAt: at,
		Windows:    []resetwatch.Window{{Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: at.Add(5 * time.Hour)}},
	}, CommittedAt: at.Add(time.Second)})
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	_ = st.Close()
	selector := store.AccountID("codex", "private-inactive")
	var dispatchErr error
	stdout := captureCLIStdout(t, func() {
		dispatchErr = dispatch([]string{"codex", "limits", "--json", "--config", configPath, "--account", selector})
	})
	if dispatchErr != nil {
		t.Fatal(dispatchErr)
	}
	var payload codexLimitsPayload
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode limits output=%q: %v", stdout, err)
	}
	if payload.Mode != "fast" || payload.AccountID != selector || payload.CredentialsAvailable == nil || *payload.CredentialsAvailable {
		t.Fatalf("stored fallback payload=%+v", payload)
	}
	if payload.ObservedAt == "" || payload.ObservedAgeMs == nil || !payload.ObservationStale {
		t.Fatalf("stored fallback freshness=%+v", payload)
	}
}

func TestLiveLimitsPayloadCarriesResolvedIdentity(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	configPath := filepath.Join(dir, "config.json")
	statePath := filepath.Join(dir, "missing.sqlite")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"token-live-limits","account_id":"private-live-limits"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"schemaVersion":3,"codexAuthPaths":["`+authPath+`"],"server":{"statePath":"`+statePath+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldFetch := fetchCodexLimits
	fetchCodexLimits = func(_ context.Context, _ *http.Client, opts remotecodex.FetchOptions) (remote.ProbeResult, error) {
		if opts.ExpectedAccountID != "private-live-limits" || len(opts.AuthPaths) != 1 || opts.AuthPaths[0] != authPath {
			t.Fatalf("fetch options=%+v", opts)
		}
		return remote.ProbeResult{ProviderID: "codex", AuthState: remote.AuthState{OK: true}, Lines: []model.MetricLine{{Type: "progress", Label: "5h limit"}}}, nil
	}
	t.Cleanup(func() { fetchCodexLimits = oldFetch })
	payload, cleanup, err := liveCodexLimitsPayloadFor(context.Background(), options{config: configPath})
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if payload.AccountID != store.AccountID("codex", "private-live-limits") || payload.CredentialsAvailable == nil || !*payload.CredentialsAvailable {
		t.Fatalf("live payload=%+v", payload)
	}
}

func TestLiveActivityCommandCarriesResolvedIdentity(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	configPath := filepath.Join(dir, "config.json")
	statePath := filepath.Join(dir, "missing.sqlite")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"token-live-activity","account_id":"private-live-activity"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"schemaVersion":3,"codexAuthPaths":["`+authPath+`"],"server":{"statePath":"`+statePath+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldFetch := fetchCodexActivity
	fetchCodexActivity = func(_ context.Context, _ *http.Client, opts remotecodex.FetchOptions) (remotecodex.ProfileResult, error) {
		if opts.ExpectedAccountID != "private-live-activity" {
			t.Fatalf("fetch options=%+v", opts)
		}
		return remotecodex.ProfileResult{ProviderID: "codex", Source: "chatgpt-codex-profile-backend", Profile: remotecodex.Profile{Username: "activity"}, AuthState: remote.AuthState{OK: true}}, nil
	}
	t.Cleanup(func() { fetchCodexActivity = oldFetch })
	var runErr error
	stdout := captureCLIStdout(t, func() { runErr = runCodexActivity(options{jsonOut: true, config: configPath}) })
	if runErr != nil {
		t.Fatal(runErr)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode activity output=%q: %v", stdout, err)
	}
	if payload["accountId"] != store.AccountID("codex", "private-live-activity") || payload["credentialsAvailable"] != true {
		t.Fatalf("activity payload=%+v", payload)
	}
}

func TestLiveResetCommandCarriesResolvedIdentity(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	configPath := filepath.Join(dir, "config.json")
	statePath := filepath.Join(dir, "missing.sqlite")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"token-live-reset","account_id":"private-live-reset"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"schemaVersion":3,"codexAuthPaths":["`+authPath+`"],"server":{"statePath":"`+statePath+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldPlan := planCodexReset
	planCodexReset = func(_ context.Context, _ *http.Client, opts remotecodex.FetchOptions, _ string) (remotecodex.RateLimitResetPlan, error) {
		if opts.ExpectedAccountID != "private-live-reset" {
			t.Fatalf("fetch options=%+v", opts)
		}
		return remotecodex.RateLimitResetPlan{ProviderID: "codex", Source: "chatgpt-codex-backend", Mode: "live", AvailableCount: 1, Credit: remote.ResetCredit{ID: "credit-1", Title: "reset"}, AuthState: remote.AuthState{OK: true}}, nil
	}
	t.Cleanup(func() { planCodexReset = oldPlan })
	var runErr error
	stdout := captureCLIStdout(t, func() { runErr = runCodexReset(options{jsonOut: true, dryRun: true, config: configPath}) })
	if runErr != nil {
		t.Fatal(runErr)
	}
	var payload codexResetPayload
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode reset output=%q: %v", stdout, err)
	}
	if payload.AccountID != store.AccountID("codex", "private-live-reset") || !payload.CredentialsAvailable {
		t.Fatalf("reset payload=%+v", payload)
	}
}

func ptrInt64(value int64) *int64 { return &value }

func ptrInt(value int) *int { return &value }
