package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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

func ptrInt64(value int64) *int64 { return &value }
