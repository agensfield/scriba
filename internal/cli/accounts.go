package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	accountresolver "github.com/agensfield/scriba/internal/accounts"
	"github.com/agensfield/scriba/internal/cache"
	"github.com/agensfield/scriba/internal/config"
	"github.com/agensfield/scriba/internal/model"
	remotecodex "github.com/agensfield/scriba/internal/remote/codex"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
)

const (
	accountsSchemaVersion = "scriba.accounts.v1"
	accountStaleAfter     = 15 * time.Minute
)

type accountsPayload struct {
	SchemaVersion string            `json:"schemaVersion"`
	Accounts      []accountListItem `json:"accounts"`
}

type accountListItem struct {
	AccountID            string `json:"accountId"`
	ProviderID           string `json:"providerId"`
	Alias                string `json:"alias,omitempty"`
	Email                string `json:"email,omitempty"`
	Plan                 string `json:"plan,omitempty"`
	CredentialsAvailable bool   `json:"credentialsAvailable"`
	LastObservedAt       string `json:"lastObservedAt,omitempty"`
	LastObservedAgeMs    *int64 `json:"lastObservedAgeMs,omitempty"`
	Stale                bool   `json:"stale,omitempty"`
}

func dispatchAccounts(args []string) error {
	command := "list"
	if len(args) > 0 && !isHelpArg(args[0]) {
		command = args[0]
		args = args[1:]
	}
	if command == "list" {
		opts, rest, err := parse(args, flagSpec{Use: "scriba accounts list [flags]", Flags: []string{"json", "config", "state-path", "redact"}})
		if err != nil {
			return err
		}
		if len(rest) > 0 {
			return errors.New("scriba accounts list does not accept positional arguments")
		}
		return runAccountsList(opts)
	}
	if command != "alias" {
		if isHelpArg(command) {
			fmt.Println(groupHelp("accounts"))
			return nil
		}
		return fmt.Errorf("unknown accounts command: %s", command)
	}
	opts, rest, err := parse(args, flagSpec{Use: "scriba accounts alias <id-or-alias> <new-alias> [flags]", Flags: []string{"json", "config", "state-path", "redact"}})
	if err != nil {
		return err
	}
	if len(rest) != 2 {
		return errors.New("scriba accounts alias requires an account selector and alias")
	}
	return runAccountsAlias(opts, rest[0], rest[1])
}

func openAccountRegistry(opts options) (*accountresolver.Resolver, *store.Store, config.Config, error) {
	cfg, err := load(opts)
	if err != nil {
		return nil, nil, cfg, err
	}
	if opts.statePath != "" {
		cfg.Server.StatePath = opts.statePath
	}
	st, err := store.OpenReadOnly(resolveServerStatePath(cfg.Server.StatePath))
	if err != nil {
		return nil, nil, cfg, err
	}
	return accountresolver.New(st, accountresolver.Sources(cfg)), st, cfg, nil
}

func openWritableAccountRegistry(opts options) (*accountresolver.Resolver, *store.Store, config.Config, error) {
	cfg, err := load(opts)
	if err != nil {
		return nil, nil, cfg, err
	}
	if opts.statePath != "" {
		cfg.Server.StatePath = opts.statePath
	}
	st, err := store.Open(resolveServerStatePath(cfg.Server.StatePath))
	if err != nil {
		return nil, nil, cfg, err
	}
	return accountresolver.New(st, accountresolver.Sources(cfg)), st, cfg, nil
}

func runAccountsList(opts options) error {
	resolver, st, _, err := openAccountRegistry(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	items, err := resolver.Accounts(context.Background())
	if err != nil {
		return err
	}
	payload := accountsPayload{SchemaVersion: accountsSchemaVersion, Accounts: accountListItems(items, time.Now().UTC())}
	if opts.redact {
		payload = redactAccounts(payload)
	}
	return output(opts, payload, renderAccounts(payload))
}

func runAccountsAlias(opts options, selector, alias string) error {
	resolver, st, _, err := openWritableAccountRegistry(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	account, err := resolver.Resolve(context.Background(), selector)
	if err != nil {
		return err
	}
	if err := resolver.SetAlias(context.Background(), account.ID, strings.TrimSpace(alias)); err != nil {
		return err
	}
	account, err = resolver.Resolve(context.Background(), account.ID)
	if err != nil {
		return err
	}
	payload := accountsPayload{SchemaVersion: accountsSchemaVersion, Accounts: accountListItems([]store.Account{account}, time.Now().UTC())}
	if opts.redact {
		payload = redactAccounts(payload)
	}
	return output(opts, payload, renderAccounts(payload))
}

func accountListItems(accounts []store.Account, now time.Time) []accountListItem {
	items := make([]accountListItem, 0, len(accounts))
	for _, account := range accounts {
		item := accountListItem{
			AccountID:            account.ID,
			ProviderID:           account.ProviderID,
			Alias:                account.Alias,
			Email:                account.Email,
			Plan:                 account.Plan,
			CredentialsAvailable: account.CredentialsAvailable,
		}
		if !account.LastSeenAt.IsZero() {
			observed := account.LastSeenAt.UTC()
			item.LastObservedAt = observed.Format(time.RFC3339Nano)
			age := now.Sub(observed)
			if age < 0 {
				age = 0
			}
			ageMs := age.Milliseconds()
			item.LastObservedAgeMs = &ageMs
			item.Stale = age > accountStaleAfter
		}
		items = append(items, item)
	}
	return items
}

func redactAccounts(payload accountsPayload) accountsPayload {
	for i := range payload.Accounts {
		payload.Accounts[i].Alias = ""
		payload.Accounts[i].Email = ""
	}
	return payload
}

func renderAccounts(payload accountsPayload) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%d accounts", cliHeader("Scriba accounts"), len(payload.Accounts))
	for _, account := range payload.Accounts {
		name := account.Alias
		if name == "" {
			name = account.Email
		}
		if name == "" {
			name = account.AccountID
		}
		fmt.Fprintf(&b, "\n\n%s\n  id          %s\n  provider    %s\n  credentials %s", cliBold(name), account.AccountID, account.ProviderID, accountCredentialState(account.CredentialsAvailable))
		if account.Email != "" && account.Email != name {
			fmt.Fprintf(&b, "\n  email       %s", account.Email)
		}
		if account.Plan != "" {
			fmt.Fprintf(&b, "\n  plan        %s", account.Plan)
		}
		if account.LastObservedAt == "" {
			b.WriteString("\n  observation  never")
		} else {
			freshness := "fresh"
			if account.Stale {
				freshness = "stale"
			}
			fmt.Fprintf(&b, "\n  observation  %s · %s · %s", account.LastObservedAt, formatAccountAge(account.LastObservedAgeMs), freshness)
		}
	}
	return b.String()
}

func accountCredentialState(available bool) string {
	if available {
		return cliGreen("available")
	}
	return cliYellow("unavailable")
}

func formatAccountAge(ageMs *int64) string {
	if ageMs == nil {
		return "unknown age"
	}
	return "age " + (time.Duration(*ageMs) * time.Millisecond).Round(time.Second).String()
}

func resolveLiveCodexOptions(ctx context.Context, opts options) (remotecodex.FetchOptions, func(), error) {
	resolver, st, _, err := openAccountRegistry(opts)
	if err != nil {
		return remotecodex.FetchOptions{}, nil, err
	}
	live, err := resolver.ResolveLive(ctx, opts.account)
	if err != nil {
		_ = st.Close()
		return remotecodex.FetchOptions{}, nil, err
	}
	return live.FetchOptions(), func() { _ = st.Close() }, nil
}

func fastCodexLimitsPayload(ctx context.Context, opts options) (codexLimitsPayload, error) {
	cfg, err := load(opts)
	if err != nil {
		return codexLimitsPayload{}, err
	}
	if opts.statePath != "" {
		cfg.Server.StatePath = opts.statePath
	}
	st, err := store.OpenReadOnly(resolveServerStatePath(cfg.Server.StatePath))
	if err != nil {
		return codexLimitsPayload{}, err
	}
	defer func() { _ = st.Close() }()
	account, ok, err := st.ResolveAccount(ctx, opts.account)
	if err != nil {
		return codexLimitsPayload{}, err
	}
	if !ok {
		return codexLimitsPayload{}, accountresolver.ErrAccountNotFound
	}
	observation, ok, err := st.LoadLatestObservationForAccount(ctx, account.ID)
	if err != nil {
		return codexLimitsPayload{}, err
	}
	if !ok {
		return codexLimitsPayload{}, fmt.Errorf("no stored Codex limits for account %s", account.DisplayName())
	}
	now := time.Now().UTC()
	age := now.Sub(observation.ObservedAt.UTC())
	if age < 0 {
		age = 0
	}
	ageMs := age.Milliseconds()
	accountAlias := ""
	if !opts.redact {
		accountAlias = account.Alias
	}
	return codexLimitsPayload{
		SchemaVersion: model.SchemaVersion,
		ProviderID:    "codex",
		Source:        "status-cache",
		Mode:          "fast",
		GeneratedAt:   observation.ObservedAt.UTC().Format(time.RFC3339Nano),
		Lines:         metricLinesFromObservation(observation),
		ResetCredits:  resetCreditsFromObservation(observation.ResetGrants.Credits),
		Provenance: []model.SourceProvenance{{
			Kind:       "resident-store",
			ProviderID: "codex",
			FetchedAt:  observation.ObservedAt.UTC().Format(time.RFC3339Nano),
			CacheAgeMs: &ageMs,
			Stale:      age > accountStaleAfter,
		}},
		AccountID:        account.ID,
		AccountAlias:     accountAlias,
		ObservedAt:       observation.ObservedAt.UTC().Format(time.RFC3339Nano),
		ObservedAgeMs:    &ageMs,
		ObservationStale: age > accountStaleAfter,
	}, nil
}

func fastAccountStatusSnapshot(cfg config.Config, opts options) (model.StatusSnapshot, error) {
	payload, err := fastCodexLimitsPayload(context.Background(), opts)
	if err != nil {
		return model.StatusSnapshot{}, err
	}
	snapshot := model.StatusSnapshot{SchemaVersion: model.SchemaVersion, GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), Timezone: cfg.Timezone, Providers: []model.ProviderSnapshot{}}
	if c, openErr := cache.OpenReadOnly(cfg.CacheDir); openErr == nil {
		defer func() { _ = c.Close() }()
		if cached, loadErr := c.LoadStatusSnapshot(); loadErr == nil && cached != nil {
			snapshot = *cached
			if snapshot.Providers == nil {
				snapshot.Providers = []model.ProviderSnapshot{}
			}
		}
	} else if !errors.Is(openErr, os.ErrNotExist) {
		return model.StatusSnapshot{}, openErr
	}
	provider := model.ProviderSnapshot{ProviderID: "codex", DisplayName: "Codex", State: "ok", Lines: payload.Lines, Provenance: payload.Provenance}
	found := false
	for i := range snapshot.Providers {
		if snapshot.Providers[i].ProviderID == "codex" {
			snapshot.Providers[i] = provider
			found = true
			break
		}
	}
	if !found {
		snapshot.Providers = append(snapshot.Providers, provider)
	}
	return snapshot, nil
}

func metricLinesFromObservation(observation resetwatch.Observation) []model.MetricLine {
	lines := make([]model.MetricLine, 0, len(observation.Windows)+2)
	limit := 100.0
	for _, window := range observation.Windows {
		line := model.MetricLine{Type: "progress", Label: window.Label, Limit: &limit, ResetsAt: window.ResetAt.UTC().Format(time.RFC3339Nano), PeriodDurationMs: window.PeriodDurationMs}
		if window.UsedPercent != nil {
			used := *window.UsedPercent
			line.Used = &used
		}
		lines = append(lines, line)
	}
	if observation.ResetGrants.AvailableCount != nil {
		lines = append(lines, model.MetricLine{Type: "amount", Label: resetwatch.LabelResetGrants, Value: *observation.ResetGrants.AvailableCount, Format: &model.MetricFormat{Kind: "count", Suffix: "available"}})
	}
	if !observation.ResetGrants.ExpiresAt.IsZero() {
		lines = append(lines, model.MetricLine{Type: "text", Label: resetwatch.LabelGrantExpiry, Value: observation.ResetGrants.ExpiresAt.UTC().Format(time.RFC3339Nano)})
	}
	return lines
}

func resetCreditsFromObservation(credits []resetwatch.ResetCredit) []remote.ResetCredit {
	result := make([]remote.ResetCredit, 0, len(credits))
	for _, credit := range credits {
		result = append(result, remote.ResetCredit{ID: credit.ID, Status: credit.Status, ResetType: credit.ResetType, Title: credit.Title, GrantedAt: credit.GrantedAt.UTC().Format(time.RFC3339Nano), ExpiresAt: credit.ExpiresAt.UTC().Format(time.RFC3339Nano)})
	}
	return result
}
